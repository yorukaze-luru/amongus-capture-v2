package memory

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestDecodeGameCode(t *testing.T) {
	tests := []struct {
		input    int32
		expected string
	}{
		{0, ""},
	}

	for _, tt := range tests {
		got := DecodeGameCode(tt.input)
		if got != tt.expected {
			t.Errorf("DecodeGameCode(%d) = %s; want %s", tt.input, got, tt.expected)
		}
	}

	// Test 6-letter valid codes
	sampleCode := int32(123456789)
	res := DecodeGameCode(sampleCode)
	if len(res) != 6 {
		t.Errorf("Expected 6 character code, got '%s' (len %d)", res, len(res))
	}
}

func TestParseHexJSON(t *testing.T) {
	sampleJSON := []byte(`{
		"TEST_HASH": {
			"Description": "Test Game Version",
			"AmongUsClientOffset": 0x22F4F10,
			"MeetingHudPtr": [0x22F6790, 0x5C, 0x0]
		}
	}`)

	var offsets map[string]*GameOffsets
	err := parseHexJSON(sampleJSON, &offsets)
	if err != nil {
		t.Fatalf("parseHexJSON failed: %v", err)
	}

	item, ok := offsets["TEST_HASH"]
	if !ok {
		t.Fatalf("TEST_HASH not found in parsed output")
	}

	if item.AmongUsClientOffset != 0x22F4F10 {
		t.Errorf("Expected 0x22F4F10 (%d), got %d", 0x22F4F10, item.AmongUsClientOffset)
	}
	if len(item.MeetingHudPtr) != 3 || item.MeetingHudPtr[1] != 0x5C {
		t.Errorf("Expected MeetingHudPtr[1] == 0x5C, got %v", item.MeetingHudPtr)
	}
}

func TestPlayerColors(t *testing.T) {
	if ColorRed.String() != "Red" {
		t.Errorf("Expected 'Red', got '%s'", ColorRed.String())
	}
	if ColorRed.Hex() != "#c51111" {
		t.Errorf("Expected '#c51111', got '%s'", ColorRed.Hex())
	}
	if ColorUnknown.Hex() != "#888888" {
		t.Errorf("Expected default color '#888888', got '%s'", ColorUnknown.Hex())
	}
}

func TestGameStateString(t *testing.T) {
	if StateLobby.String() != "LOBBY" {
		t.Errorf("Expected 'LOBBY', got '%s'", StateLobby.String())
	}
	if StateDiscussion.String() != "DISCUSSION" {
		t.Errorf("Expected 'DISCUSSION', got '%s'", StateDiscussion.String())
	}
}

func TestExtractPlayerInfoHeuristic_IsDead(t *testing.T) {
	mr := NewMemoryReader(windows.CurrentProcess(), 0, true)
	gr := &GameReader{}

	offsets := &GameOffsets{
		PlayerInfoStructOffsets: PlayerInfoStructOffsets{
			PlayerIDOffset:     0x38,
			PlayerLevelOffset:  0x60,
			DisconnectedOffset: 0x64,
			TasksOffset:        0x70,
			IsDeadOffset:       0x78,
			ObjectOffset:       0x80,
		},
	}

	// Case 1: Standard IsDead at configured offset 0x78
	buf1 := make([]byte, 256)
	buf1[0x38] = 1    // PlayerID 1
	buf1[0x78] = 1    // IsDead = 1
	ptr1 := uintptr(unsafe.Pointer(&buf1[0]))

	pInfo1, err := gr.extractPlayerInfoHeuristic(mr, ptr1, offsets)
	if err != nil || !pInfo1.IsDead {
		t.Errorf("Case 1: Expected IsDead=true, got %v (err: %v)", pInfo1.IsDead, err)
	}

	// Case 2: Configured offset is 0x78, but actual IsDead is at 0x74 (window scan)
	buf2 := make([]byte, 256)
	buf2[0x38] = 2
	buf2[0x74] = 1 // in window [TasksOffset+8 .. ObjectOffset] (0x78..0x80 or fallback window)
	ptr2 := uintptr(unsafe.Pointer(&buf2[0]))

	// Set TasksOffset=0x68, ObjectOffset=0x78 so window is 0x70..0x78
	offsets2 := &GameOffsets{
		PlayerInfoStructOffsets: PlayerInfoStructOffsets{
			PlayerIDOffset:     0x38,
			PlayerLevelOffset:  0x58,
			DisconnectedOffset: 0x5C,
			TasksOffset:        0x68,
			IsDeadOffset:       0x78, // Wrong configured offset!
			ObjectOffset:       0x78,
		},
	}
	buf2[0x70] = 1 // actual IsDead at 0x70
	pInfo2, err := gr.extractPlayerInfoHeuristic(mr, ptr2, offsets2)
	if err != nil || !pInfo2.IsDead {
		t.Errorf("Case 2: Expected window-scanned IsDead=true, got %v (err: %v)", pInfo2.IsDead, err)
	}

	// Case 3: Fallback historical offset 0x50 when configured offset is 0x78
	buf3 := make([]byte, 256)
	buf3[0x38] = 3
	buf3[0x50] = 1 // IsDead at known historical 0x50
	ptr3 := uintptr(unsafe.Pointer(&buf3[0]))

	pInfo3, err := gr.extractPlayerInfoHeuristic(mr, ptr3, offsets)
	if err != nil || !pInfo3.IsDead {
		t.Errorf("Case 3: Expected historical-fallback IsDead=true, got %v (err: %v)", pInfo3.IsDead, err)
	}

	// Case 4: Alive player (all dead flags 0)
	buf4 := make([]byte, 256)
	buf4[0x38] = 4
	buf4[0x60] = 1 // PlayerLevel=1 (should NOT be mistaken for IsDead)
	ptr4 := uintptr(unsafe.Pointer(&buf4[0]))

	pInfo4, err := gr.extractPlayerInfoHeuristic(mr, ptr4, offsets)
	if err != nil || pInfo4.IsDead {
		t.Errorf("Case 4: Expected alive player IsDead=false, got %v", pInfo4.IsDead)
	}
}
