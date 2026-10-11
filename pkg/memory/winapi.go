package memory

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modkernel32 = windows.NewLazySystemDLL("kernel32.dll")
	modpsapi    = windows.NewLazySystemDLL("psapi.dll")

	procOpenProcess           = modkernel32.NewProc("OpenProcess")
	procCloseHandle           = modkernel32.NewProc("CloseHandle")
	procReadProcessMemory     = modkernel32.NewProc("ReadProcessMemory")
	procVirtualQueryEx        = modkernel32.NewProc("VirtualQueryEx")
	procCreateToolhelp32Snap  = modkernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW       = modkernel32.NewProc("Process32FirstW")
	procProcess32NextW        = modkernel32.NewProc("Process32NextW")
	procModule32FirstW        = modkernel32.NewProc("Module32FirstW")
	procModule32NextW         = modkernel32.NewProc("Module32NextW")
	procIsWow64Process        = modkernel32.NewProc("IsWow64Process")
)

const (
	PROCESS_VM_READ           = 0x0010
	PROCESS_QUERY_INFORMATION = 0x0400
	TH32CS_SNAPPROCESS        = 0x00000002
	TH32CS_SNAPMODULE         = 0x00000008
	TH32CS_SNAPMODULE32       = 0x00000010
)

type ProcessEntry32W struct {
	Size              uint32
	Usage             uint32
	ProcessID         uint32
	DefaultHeapID     uintptr
	ModuleID          uint32
	Threads           uint32
	ParentProcessID   uint32
	PriClassBase      int32
	Flags             uint32
	ExeFile           [windows.MAX_PATH]uint16
}

type ModuleEntry32W struct {
	Size         uint32
	ModuleID     uint32
	ProcessID    uint32
	GlblcntUsage uint32
	ProccntUsage uint32
	ModBaseAddr  uintptr
	ModBaseSize  uint32
	HModule      windows.Handle
	ModuleName   [256]uint16
	ExePath      [windows.MAX_PATH]uint16
}

type MemoryBasicInformation struct {
	BaseAddress       uintptr
	AllocationBase    uintptr
	AllocationProtect uint32
	RegionSize        uintptr
	State             uint32
	Protect           uint32
	Type              uint32
}

// MemoryReader wraps Windows API process memory reading capabilities
type MemoryReader struct {
	handle windows.Handle
	pid    uint32
	is64   bool
}

// NewMemoryReader creates a memory reader for a given process handle
func NewMemoryReader(h windows.Handle, pid uint32, is64 bool) *MemoryReader {
	return &MemoryReader{
		handle: h,
		pid:    pid,
		is64:   is64,
	}
}

// Close closes the process handle
func (m *MemoryReader) Close() error {
	if m.handle != 0 && m.handle != windows.InvalidHandle {
		err := windows.CloseHandle(m.handle)
		m.handle = 0
		return err
	}
	return nil
}

// ReadRaw reads raw bytes from process memory
func (m *MemoryReader) ReadRaw(addr uintptr, size uint32) ([]byte, error) {
	if addr == 0 || size == 0 {
		return nil, fmt.Errorf("invalid address or size: addr=0x%x size=%d", addr, size)
	}
	buf := make([]byte, size)
	var bytesRead uintptr
	ret, _, err := procReadProcessMemory.Call(
		uintptr(m.handle),
		addr,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(size),
		uintptr(unsafe.Pointer(&bytesRead)),
	)
	if ret == 0 {
		return nil, fmt.Errorf("ReadProcessMemory failed at 0x%x: %v", addr, err)
	}
	if bytesRead < uintptr(size) {
		return buf[:bytesRead], nil
	}
	return buf, nil
}

// ReadUInt8 reads a byte
func (m *MemoryReader) ReadUInt8(addr uintptr) (byte, error) {
	buf, err := m.ReadRaw(addr, 1)
	if err != nil || len(buf) < 1 {
		return 0, err
	}
	return buf[0], nil
}

// ReadUInt16 reads a uint16
func (m *MemoryReader) ReadUInt16(addr uintptr) (uint16, error) {
	buf, err := m.ReadRaw(addr, 2)
	if err != nil || len(buf) < 2 {
		return 0, err
	}
	return binary.LittleEndian.Uint16(buf), nil
}

// ReadInt32 reads an int32
func (m *MemoryReader) ReadInt32(addr uintptr) (int32, error) {
	buf, err := m.ReadRaw(addr, 4)
	if err != nil || len(buf) < 4 {
		return 0, err
	}
	return int32(binary.LittleEndian.Uint32(buf)), nil
}

// ReadUInt32 reads a uint32
func (m *MemoryReader) ReadUInt32(addr uintptr) (uint32, error) {
	buf, err := m.ReadRaw(addr, 4)
	if err != nil || len(buf) < 4 {
		return 0, err
	}
	return binary.LittleEndian.Uint32(buf), nil
}

// ReadInt64 reads an int64
func (m *MemoryReader) ReadInt64(addr uintptr) (int64, error) {
	buf, err := m.ReadRaw(addr, 8)
	if err != nil || len(buf) < 8 {
		return 0, err
	}
	return int64(binary.LittleEndian.Uint64(buf)), nil
}

// ReadUInt64 reads a uint64
func (m *MemoryReader) ReadUInt64(addr uintptr) (uint64, error) {
	buf, err := m.ReadRaw(addr, 8)
	if err != nil || len(buf) < 8 {
		return 0, err
	}
	return binary.LittleEndian.Uint64(buf), nil
}

// ReadPtr reads a pointer (either 32-bit or 64-bit depending on target process architecture)
func (m *MemoryReader) ReadPtr(addr uintptr) (uintptr, error) {
	if m.is64 {
		v, err := m.ReadUInt64(addr)
		return uintptr(v), err
	}
	v, err := m.ReadUInt32(addr)
	return uintptr(v), err
}

// ReadPtrOffsets follows a pointer chain with given offsets
func (m *MemoryReader) ReadPtrOffsets(base uintptr, offsets []int) (uintptr, error) {
	if base == 0 {
		return 0, fmt.Errorf("base pointer is nil")
	}
	current := base
	for i, offset := range offsets {
		target := current + uintptr(offset)
		// If it's the last element, we might want the value or pointer.
		// In pointer chains, we dereference at each step.
		ptr, err := m.ReadPtr(target)
		if err != nil {
			return 0, fmt.Errorf("failed at offset index %d (offset 0x%x from 0x%x): %w", i, offset, target, err)
		}
		if ptr == 0 && i < len(offsets)-1 {
			return 0, fmt.Errorf("null pointer dereferenced at step %d (offset 0x%x)", i, offset)
		}
		current = ptr
	}
	return current, nil
}

// ReadPtrOffsetsWithFinalAddress resolves a pointer chain up to the last offset without dereferencing the final value
func (m *MemoryReader) ReadPtrOffsetsWithFinalAddress(base uintptr, offsets []int) (uintptr, error) {
	if len(offsets) == 0 {
		return base, nil
	}
	if len(offsets) == 1 {
		return base + uintptr(offsets[0]), nil
	}
	// Follow all dereferences except the last
	current := base
	for i := 0; i < len(offsets)-1; i++ {
		target := current + uintptr(offsets[i])
		ptr, err := m.ReadPtr(target)
		if err != nil {
			return 0, fmt.Errorf("failed at step %d: %w", i, err)
		}
		if ptr == 0 {
			return 0, fmt.Errorf("null pointer at step %d", i)
		}
		current = ptr
	}
	return current + uintptr(offsets[len(offsets)-1]), nil
}

// ReadString reads a null-terminated UTF-8 or ASCII string
func (m *MemoryReader) ReadString(addr uintptr, maxLen int) (string, error) {
	if addr == 0 {
		return "", fmt.Errorf("null address")
	}
	buf, err := m.ReadRaw(addr, uint32(maxLen))
	if err != nil {
		return "", err
	}
	nullIdx := bytes.IndexByte(buf, 0)
	if nullIdx != -1 {
		buf = buf[:nullIdx]
	}
	return string(buf), nil
}

// ReadIl2cppString reads a Unity IL2CPP managed C# string (Length at +0x10 or +0x8, UTF-16 chars at +0x14 or +0xC)
func (m *MemoryReader) ReadIl2cppString(addr uintptr) (string, error) {
	if addr == 0 {
		return "", fmt.Errorf("null string pointer")
	}

	var lengthOffset uintptr = 0x10
	var charsOffset uintptr = 0x14
	if !m.is64 {
		lengthOffset = 0x8
		charsOffset = 0xC
	}

	length, err := m.ReadInt32(addr + lengthOffset)
	if err != nil || length <= 0 || length > 128 {
		return "", fmt.Errorf("invalid il2cpp string length %d: %v", length, err)
	}

	byteLen := uint32(length * 2)
	raw, err := m.ReadRaw(addr+charsOffset, byteLen)
	if err != nil || len(raw) < int(byteLen) {
		return "", fmt.Errorf("failed to read utf16 string bytes: %w", err)
	}

	u16 := make([]uint16, length)
	for i := 0; i < int(length); i++ {
		u16[i] = binary.LittleEndian.Uint16(raw[i*2 : i*2+2])
	}

	decoded := string(utf16.Decode(u16))
	decoded = strings.TrimRight(decoded, "\x00")
	return decoded, nil
}

// QueryMemory queries information about a range of pages
func (m *MemoryReader) QueryMemory(addr uintptr) (*MemoryBasicInformation, error) {
	var mbi MemoryBasicInformation
	ret, _, err := procVirtualQueryEx.Call(
		uintptr(m.handle),
		addr,
		uintptr(unsafe.Pointer(&mbi)),
		unsafe.Sizeof(mbi),
	)
	if ret == 0 {
		return nil, err
	}
	return &mbi, nil
}
