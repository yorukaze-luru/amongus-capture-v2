package memory

import "testing"

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
