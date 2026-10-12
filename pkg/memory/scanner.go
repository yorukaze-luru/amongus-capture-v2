package memory

import (
	"encoding/binary"
	"fmt"
	"log"
	"strings"
)

// Pattern represents a byte pattern for scanning
type Pattern struct {
	Name    string
	Bytes   []byte
	Mask    []bool // true = match exact byte, false = wildcard (??)
	Offset  int    // offset within pattern where relative RIP offset is located
	InstLen int    // total length of instruction from pattern start to calculate RIP
}

// ParsePattern parses an IDA-style hex pattern (e.g. "48 8B 05 ?? ?? ?? ?? 48 85 C0")
func ParsePattern(name, patternStr string, relOffsetIndex, instLen int) Pattern {
	parts := strings.Fields(patternStr)
	patternBytes := make([]byte, len(parts))
	mask := make([]bool, len(parts))

	for i, p := range parts {
		if p == "??" || p == "?" {
			patternBytes[i] = 0
			mask[i] = false
		} else {
			var b byte
			fmt.Sscanf(p, "%02x", &b)
			patternBytes[i] = b
			mask[i] = true
		}
	}

	return Pattern{
		Name:    name,
		Bytes:   patternBytes,
		Mask:    mask,
		Offset:  relOffsetIndex,
		InstLen: instLen,
	}
}

// AOBScanner scans memory regions for IL2CPP signatures
type AOBScanner struct {
	reader *MemoryReader
	base   uintptr
	size   uint32
	is64   bool
}

func NewAOBScanner(reader *MemoryReader, base uintptr, size uint32, is64 bool) *AOBScanner {
	return &AOBScanner{
		reader: reader,
		base:   base,
		size:   size,
		is64:   is64,
	}
}

// ScanPattern scans the module memory for a pattern and resolves the RIP-relative target
func (s *AOBScanner) ScanPattern(p Pattern) (uintptr, error) {
	chunkSize := uint32(1024 * 1024 * 4) // 4MB chunks
	patternLen := len(p.Bytes)
	if patternLen == 0 {
		return 0, fmt.Errorf("empty pattern")
	}

	for offset := uint32(0); offset < s.size; offset += chunkSize - uint32(patternLen) {
		readSize := chunkSize
		if offset+readSize > s.size {
			readSize = s.size - offset
		}

		raw, err := s.reader.ReadRaw(s.base+uintptr(offset), readSize)
		if err != nil || len(raw) < patternLen {
			continue
		}

		// Search pattern in buffer
		for i := 0; i <= len(raw)-patternLen; i++ {
			matched := true
			for j := 0; j < patternLen; j++ {
				if p.Mask[j] && raw[i+j] != p.Bytes[j] {
					matched = false
					break
				}
			}

			if matched {
				matchAddr := s.base + uintptr(offset) + uintptr(i)

				if s.is64 && p.Offset >= 0 && p.InstLen > 0 {
					// 64-bit RIP-relative addressing
					relOffsetBytes := raw[i+p.Offset : i+p.Offset+4]
					relDisplacement := int32(binary.LittleEndian.Uint32(relOffsetBytes))

					// Target = Instruction Address + Instruction Length + Displacement
					targetAddr := uintptr(int64(matchAddr) + int64(p.InstLen) + int64(relDisplacement))
					return targetAddr, nil
				} else if !s.is64 && p.Offset >= 0 {
					// 32-bit absolute direct pointer
					ptrBytes := raw[i+p.Offset : i+p.Offset+4]
					targetAddr := uintptr(binary.LittleEndian.Uint32(ptrBytes))
					return targetAddr, nil
				}

				return matchAddr, nil
			}
		}
	}

	return 0, fmt.Errorf("pattern '%s' not found", p.Name)
}

// ScannedPointers contains dynamically resolved base class pointers
type ScannedPointers struct {
	GameDataClassPtr      uintptr
	AmongUsClientClassPtr uintptr
	PlayerControlClassPtr uintptr
	MeetingHudClassPtr    uintptr
	GameStartManagerPtr   uintptr
}

// ScanAllSignatures resolves all core class pointers from GameAssembly.dll
func (s *AOBScanner) ScanAllSignatures() ScannedPointers {
	var res ScannedPointers

	if s.is64 {
		// x64 Signatures
		gameDataPatterns := []Pattern{
			ParsePattern("GameData_1", "48 8B 05 ?? ?? ?? ?? 48 85 C0 74 ?? 48 8B 48 20", 3, 7),
			ParsePattern("GameData_2", "48 8B 05 ?? ?? ?? ?? 48 8B 88 ?? 00 00 00 48 8B 49 20", 3, 7),
			ParsePattern("GameData_3", "48 8B 05 ?? ?? ?? ?? 48 85 C0 74 ?? 48 8B 80 ?? 00 00 00", 3, 7),
			ParsePattern("GameData_4", "48 8B 0D ?? ?? ?? ?? E8 ?? ?? ?? ?? 48 8B 05 ?? ?? ?? ?? 48 85 C0", 3, 7),
			ParsePattern("GameData_5", "48 8B 05 ?? ?? ?? ?? 48 8B 88 ?? 00 00 00 48 85 C9 74 ?? 48 8B 49 20", 3, 7),
			ParsePattern("GameData_6", "48 8B 05 ?? ?? ?? ?? 48 8B 48 20 48 85 C9", 3, 7),
		}
		for _, p := range gameDataPatterns {
			if addr, err := s.ScanPattern(p); err == nil && addr > s.base {
				res.GameDataClassPtr = addr
				log.Printf("[Scanner] Found GameData Class via %s: 0x%X (RVA: 0x%X)", p.Name, addr, addr-s.base)
				break
			}
		}

		clientPatterns := []Pattern{
			ParsePattern("Client_1", "48 8B 05 ?? ?? ?? ?? 48 8B 88 ?? 00 00 00 48 85 C9", 3, 7),
			ParsePattern("Client_2", "48 8B 05 ?? ?? ?? ?? 48 85 C0 74 ?? 48 8B 88 ?? 00 00 00", 3, 7),
			ParsePattern("Client_3", "48 8B 05 ?? ?? ?? ?? 48 85 C0 74 ?? 80 B8 ?? 00 00 00 00", 3, 7),
			ParsePattern("Client_4", "48 8B 05 ?? ?? ?? ?? 48 85 C0 74 ?? 80 B8 ?? 00 00 00 01", 3, 7),
			ParsePattern("Client_5", "48 8B 0D ?? ?? ?? ?? 48 85 C9 74 ?? 80 B9 ?? 00 00 00 00", 3, 7),
		}
		for _, p := range clientPatterns {
			if addr, err := s.ScanPattern(p); err == nil && addr > s.base {
				res.AmongUsClientClassPtr = addr
				log.Printf("[Scanner] Found AmongUsClient Class via %s: 0x%X (RVA: 0x%X)", p.Name, addr, addr-s.base)
				break
			}
		}

		playerControlPatterns := []Pattern{
			ParsePattern("PlayerControl_1", "48 8B 05 ?? ?? ?? ?? 48 8B 88 ?? 00 00 00 48 8B 01", 3, 7),
			ParsePattern("PlayerControl_2", "48 8B 05 ?? ?? ?? ?? 48 85 C0 74 ?? 48 8B 40 20", 3, 7),
			ParsePattern("PlayerControl_3", "48 8B 05 ?? ?? ?? ?? 48 8B 88 ?? 00 00 00 48 85 C9", 3, 7),
			ParsePattern("PlayerControl_4", "48 8B 05 ?? ?? ?? ?? 48 85 C0 74 ?? 48 8B 88 ?? 00 00 00", 3, 7),
			ParsePattern("PlayerControl_5", "48 8B 0D ?? ?? ?? ?? E8 ?? ?? ?? ?? 48 8B 05 ?? ?? ?? ?? 48 8B 88", 3, 7),
		}
		for _, p := range playerControlPatterns {
			if addr, err := s.ScanPattern(p); err == nil && addr > s.base {
				res.PlayerControlClassPtr = addr
				log.Printf("[Scanner] Found PlayerControl Class via %s: 0x%X (RVA: 0x%X)", p.Name, addr, addr-s.base)
				break
			}
		}

		meetingPatterns := []Pattern{
			ParsePattern("Meeting_1", "48 8B 05 ?? ?? ?? ?? 48 85 C0 74 ?? 48 8B 88 ?? 00 00 00", 3, 7),
			ParsePattern("Meeting_2", "48 8B 05 ?? ?? ?? ?? 80 B8 ?? 00 00 00 00 74", 3, 7),
			ParsePattern("Meeting_3", "48 8B 05 ?? ?? ?? ?? 48 85 C0 74 ?? 80 B8 ?? 00 00 00", 3, 7),
		}
		for _, p := range meetingPatterns {
			if addr, err := s.ScanPattern(p); err == nil && addr > s.base {
				res.MeetingHudClassPtr = addr
				break
			}
		}
	} else {
		// x86 Signatures
		p := ParsePattern("GameData_x86", "A1 ?? ?? ?? ?? 85 C0 74 ?? 8B 48 10", 1, 0)
		if addr, err := s.ScanPattern(p); err == nil {
			res.GameDataClassPtr = addr
		}
	}

	return res
}

// ResolveOffsetsDynamically attempts to scan IL2CPP structures in GameAssembly.dll
func (s *AOBScanner) ResolveOffsetsDynamically(template *GameOffsets) (*GameOffsets, error) {
	if template == nil {
		return nil, fmt.Errorf("template is nil")
	}

	resolved := *template
	resolved.Description = "Dynamic Pattern Scan Resolved"

	sigs := s.ScanAllSignatures()
	if sigs.GameDataClassPtr > s.base {
		rva := int(sigs.GameDataClassPtr - s.base)
		if len(resolved.AllPlayerPtrOffsets) > 0 {
			resolved.AllPlayerPtrOffsets[0] = rva
		}
		resolved.GameDataOffset = rva
	}

	if sigs.AmongUsClientClassPtr > s.base {
		rva := int(sigs.AmongUsClientClassPtr - s.base)
		if len(resolved.GameStateOffsets) > 0 {
			resolved.GameStateOffsets[0] = rva
		}
		resolved.AmongUsClientOffset = rva
	}

	if sigs.MeetingHudClassPtr > s.base {
		rva := int(sigs.MeetingHudClassPtr - s.base)
		if len(resolved.MeetingHudPtr) > 0 {
			resolved.MeetingHudPtr[0] = rva
		}
		resolved.MeetingHudOffset = rva
	}

	if sigs.PlayerControlClassPtr > s.base {
		rva := int(sigs.PlayerControlClassPtr - s.base)
		resolved.PlayerControlOffset = rva
	}

	return &resolved, nil
}
