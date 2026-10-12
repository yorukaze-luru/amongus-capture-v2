package memory

import (
	"encoding/json"
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

func TestGameSnapshotJSON(t *testing.T) {
	snap := GameSnapshot{
		GameState:    3,
		GameStateStr: "DISCUSSION",
		LobbyCode:    "XXXXXX",
		PlayerCount:  2,
		Players: []PlayerSnapshotItem{
			{
				Name:   "Player1",
				IsDead: false,
				Color:  0,
			},
			{
				Name:   "Player2",
				IsDead: true,
				Color:  1,
			},
		},
	}

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	expectedJSON := `{
  "gameState": 3,
  "gameStateStr": "DISCUSSION",
  "lobbyCode": "XXXXXX",
  "playerCount": 2,
  "players": [
    {
      "name": "Player1",
      "isDead": false,
      "color": 0
    },
    {
      "name": "Player2",
      "isDead": true,
      "color": 1
    }
  ]
}`

	if string(data) != expectedJSON {
		t.Errorf("Snapshot JSON mismatch:\nGot:\n%s\nWant:\n%s", string(data), expectedJSON)
	}
}

func TestReadRawGameState_Logic(t *testing.T) {
	gr := NewGameReader(nil)
	mr := NewMemoryReader(windows.CurrentProcess(), 0, true)

	// Simulate mock AmongUsClient in memory
	// Structure:
	// classPtr at classAddr
	// staticFields at classPtr + 0xB8
	// inst at staticFields + 0x0
	// gameState at inst + 0xAC
	gameStateBuf := make([]byte, 512)
	instAddr := uintptr(unsafe.Pointer(&gameStateBuf[0]))

	staticFieldsBuf := make([]uintptr, 8)
	staticFieldsBuf[0] = instAddr
	staticFieldsAddr := uintptr(unsafe.Pointer(&staticFieldsBuf[0]))

	classBuf := make([]uintptr, 32)
	classBuf[0xB8/8] = staticFieldsAddr
	classAddr := uintptr(unsafe.Pointer(&classBuf[0]))

	classPtrBuf := make([]uintptr, 2)
	classPtrBuf[0] = classAddr
	classPtrAddr := uintptr(unsafe.Pointer(&classPtrBuf[0]))

	offsets := &GameOffsets{
		AmongUsClientOffset: int(classPtrAddr), // base=0 in test
		GameStateOffsets:    []int{int(classPtrAddr), 0xB8, 0x0, 0xAC},
	}

	// 1. Test rawValue = 1 (LOBBY)
	*(*int32)(unsafe.Pointer(instAddr + 0xAC)) = 1
	state, raw, _ := gr.readRawGameState(mr, 0, offsets, ScannedPointers{})
	if state != StateLobby || raw != 1 {
		t.Errorf("Expected StateLobby (1), got state=%v, raw=%d", state, raw)
	}

	// 2. Test rawValue = 2 (TASKS when meeting is inactive)
	*(*int32)(unsafe.Pointer(instAddr + 0xAC)) = 2
	state, raw, _ = gr.readRawGameState(mr, 0, offsets, ScannedPointers{})
	if state != StateTasks || raw != 2 {
		t.Errorf("Expected StateTasks (2), got state=%v, raw=%d", state, raw)
	}

	// 3. Test rawValue = 3 (GAME_OVER)
	*(*int32)(unsafe.Pointer(instAddr + 0xAC)) = 3
	state, raw, _ = gr.readRawGameState(mr, 0, offsets, ScannedPointers{})
	if state != StateGameOver || raw != 3 {
		t.Errorf("Expected StateGameOver (4), got state=%v, raw=%d", state, raw)
	}

	// 4. Test rawValue = 0 (MENU when no lobby or players)
	*(*int32)(unsafe.Pointer(instAddr + 0xAC)) = 0
	state, raw, _ = gr.readRawGameState(mr, 0, offsets, ScannedPointers{})
	if state != StateMenu || raw != 0 {
		t.Errorf("Expected StateMenu (0), got state=%v, raw=%d", state, raw)
	}

	// 5. Test rawValue = 0 but in lobby (has room code) -> LOBBY
	gr.currentLobbyCode = "ABCDEF"
	state, raw, _ = gr.readRawGameState(mr, 0, offsets, ScannedPointers{})
	if state != StateLobby {
		t.Errorf("Expected StateLobby with active room code, got state=%v", state)
	}
}

func TestExtractPlayerInfoFromData_OutfitsChain(t *testing.T) {
	mr := NewMemoryReader(windows.CurrentProcess(), 0, true)
	gr := NewGameReader(nil)

	// Build Il2cppString for Name "TestPlayer"
	nameStr := "TestPlayer"
	nameRunes := []uint16{}
	for _, r := range nameStr {
		nameRunes = append(nameRunes, uint16(r))
	}
	nameBuf := make([]byte, 0x14+len(nameRunes)*2+4)
	*(*int32)(unsafe.Pointer(&nameBuf[0x10])) = int32(len(nameRunes))
	for i, r := range nameRunes {
		*(*uint16)(unsafe.Pointer(&nameBuf[0x14+i*2])) = r
	}
	namePtr := uintptr(unsafe.Pointer(&nameBuf[0]))

	// Build PlayerOutfit at outfitPtr:
	// Color at +0x10 = 5 (Yellow)
	// PlayerName ptr at +0x40 = namePtr
	outfitBuf := make([]byte, 128)
	*(*uint32)(unsafe.Pointer(&outfitBuf[0x10])) = 5
	*(*uintptr)(unsafe.Pointer(&outfitBuf[0x40])) = namePtr
	outfitPtr := uintptr(unsafe.Pointer(&outfitBuf[0]))

	// Intermediate outfit chain:
	// outStep2 at +0x30 = outfitPtr
	outStep2Buf := make([]byte, 128)
	*(*uintptr)(unsafe.Pointer(&outStep2Buf[0x30])) = outfitPtr
	outStep2Ptr := uintptr(unsafe.Pointer(&outStep2Buf[0]))

	// outStep1 at +0x18 = outStep2Ptr
	outStep1Buf := make([]byte, 128)
	*(*uintptr)(unsafe.Pointer(&outStep1Buf[0x18])) = outStep2Ptr
	outStep1Ptr := uintptr(unsafe.Pointer(&outStep1Buf[0]))

	// PlayerInfo struct at playerPtr:
	// PlayerID at +0x38 = 7
	// IsDead at +0x78 = 1
	// Disconnected at +0x64 = 0
	// OutfitsOffset at +0x58 = outStep1Ptr
	playerBuf := make([]byte, 256)
	playerBuf[0x38] = 7
	playerBuf[0x78] = 1
	playerBuf[0x64] = 0
	*(*uintptr)(unsafe.Pointer(&playerBuf[0x58])) = outStep1Ptr
	playerPtr := uintptr(unsafe.Pointer(&playerBuf[0]))

	offsets := &GameOffsets{
		PlayerInfoStructOffsets: PlayerInfoStructOffsets{
			PlayerIDOffset:     0x38,
			OutfitsOffset:      []int{0x58, 0x18, 0x30},
			DisconnectedOffset: 0x64,
			TasksOffset:        0x70,
			IsDeadOffset:       0x78,
			ObjectOffset:       0x80,
		},
		PlayerOutfitStructOffsets: PlayerOutfitStructOffsets{
			ColorIDOffset:    0x10,
			PlayerNameOffset: 0x40,
		},
	}

	pInfo, err := gr.extractPlayerInfoFromData(mr, playerPtr, offsets)
	if err != nil {
		t.Fatalf("extractPlayerInfoFromData returned error: %v", err)
	}

	if pInfo.PlayerID != 7 {
		t.Errorf("Expected PlayerID=7, got %d", pInfo.PlayerID)
	}
	if !pInfo.IsDead {
		t.Errorf("Expected IsDead=true, got false")
	}
	if pInfo.Color != ColorYellow {
		t.Errorf("Expected Color=ColorYellow (5), got %v", pInfo.Color)
	}
	if pInfo.Name != "TestPlayer" {
		t.Errorf("Expected Name='TestPlayer', got '%s'", pInfo.Name)
	}
}

func TestRoleTypeNameAndJapanese(t *testing.T) {
	if RoleTypeName(0) != "Crewmate" || RoleTypeJapanese(0) != "クルーメイト" {
		t.Errorf("Role 0 mismatch")
	}
	if RoleTypeName(1) != "Impostor" || RoleTypeJapanese(1) != "インポスター" {
		t.Errorf("Role 1 mismatch")
	}
	if RoleTypeName(2) != "Scientist" || RoleTypeJapanese(2) != "科学者" {
		t.Errorf("Role 2 mismatch")
	}
	if RoleTypeName(3) != "Engineer" || RoleTypeJapanese(3) != "エンジニア" {
		t.Errorf("Role 3 mismatch")
	}
	if RoleTypeName(4) != "GuardianAngel" || RoleTypeJapanese(4) != "守護天使" {
		t.Errorf("Role 4 mismatch")
	}
	if RoleTypeName(5) != "Shapeshifter" || RoleTypeJapanese(5) != "シェイプシフター" {
		t.Errorf("Role 5 mismatch")
	}
}

func TestIsImpostorRole(t *testing.T) {
	if !IsImpostorRole(0, 1) { // TeamType 1 = Impostor team
		t.Errorf("Expected IsImpostorRole=true for teamType=1")
	}
	if !IsImpostorRole(1, 0) { // RoleType 1 = Impostor
		t.Errorf("Expected IsImpostorRole=true for roleType=1")
	}
	if !IsImpostorRole(5, 0) { // RoleType 5 = Shapeshifter
		t.Errorf("Expected IsImpostorRole=true for roleType=5")
	}
	if IsImpostorRole(0, 0) { // Crewmate
		t.Errorf("Expected IsImpostorRole=false for Crewmate")
	}
	if IsImpostorRole(2, 0) { // Scientist
		t.Errorf("Expected IsImpostorRole=false for Scientist")
	}
	if IsImpostorRole(3, 0) { // Engineer
		t.Errorf("Expected IsImpostorRole=false for Engineer")
	}
}

func TestExtractPlayerInfoFromData_PlayerControlResolution(t *testing.T) {
	mr := NewMemoryReader(windows.CurrentProcess(), 0, true)
	gr := NewGameReader(nil)

	// Build NetworkedPlayerInfo buffer:
	// PlayerId at +0x38 = 3
	// IsDead at +0x78 = 0 (ALIVE!)
	// Disconnected at +0x64 = 0
	infoBuf := make([]byte, 256)
	infoBuf[0x38] = 3
	infoBuf[0x78] = 0 // alive
	infoBuf[0x64] = 0
	infoPtr := uintptr(unsafe.Pointer(&infoBuf[0]))

	// Build PlayerControl buffer:
	// CachedPlayerData at +0x78 = infoPtr
	// Note: infoPtr is a non-zero address, so the low byte of infoPtr is non-zero!
	// Previously, passing pcPtr would mistakenly read the low byte of infoPtr as IsDead=true!
	pcBuf := make([]byte, 256)
	*(*uintptr)(unsafe.Pointer(&pcBuf[0x78])) = infoPtr
	pcPtr := uintptr(unsafe.Pointer(&pcBuf[0]))

	offsets := &GameOffsets{
		PlayerInfoStructOffsets: PlayerInfoStructOffsets{
			PlayerIDOffset:     0x38,
			DisconnectedOffset: 0x64,
			IsDeadOffset:       0x78,
		},
	}

	// Passing pcPtr should resolve CachedPlayerData (+0x78) to infoPtr and correctly read IsDead = false!
	pInfo, err := gr.extractPlayerInfoFromData(mr, pcPtr, offsets)
	if err != nil {
		t.Fatalf("extractPlayerInfoFromData returned error: %v", err)
	}

	if pInfo.PlayerID != 3 {
		t.Errorf("Expected PlayerID=3, got %d", pInfo.PlayerID)
	}
	if pInfo.IsDead {
		t.Errorf("CRITICAL BUG REGRESSION: Expected IsDead=false (Alive), but got IsDead=true! Pointer at 0x78 was mistaken for dead flag!")
	}
}

func TestExtractPlayerInfoFromData_LobbyAliveGuard(t *testing.T) {
	mr := NewMemoryReader(windows.CurrentProcess(), 0, true)
	gr := NewGameReader(nil)
	gr.currentState = StateLobby // in Lobby!

	// NetworkedPlayerInfo buffer with garbage byte 1 at 0x78
	infoBuf := make([]byte, 256)
	infoBuf[0x38] = 5
	infoBuf[0x78] = 1 // memory has 1
	infoPtr := uintptr(unsafe.Pointer(&infoBuf[0]))

	offsets := &GameOffsets{
		PlayerInfoStructOffsets: PlayerInfoStructOffsets{
			PlayerIDOffset: 0x38,
			IsDeadOffset:   0x78,
		},
	}

	pInfo, err := gr.extractPlayerInfoFromData(mr, infoPtr, offsets)
	if err != nil {
		t.Fatalf("extractPlayerInfoFromData returned error: %v", err)
	}

	if pInfo.IsDead {
		t.Errorf("Expected IsDead=false in StateLobby, got true")
	}
}

func TestExtractPlayerInfoFromData_RoleExtraction(t *testing.T) {
	mr := NewMemoryReader(windows.CurrentProcess(), 0, true)
	gr := NewGameReader(nil)

	// RoleBehaviour at rolePtr:
	// RoleType at +0x20 = 5 (Shapeshifter)
	// TeamType at +0x74 = 1 (Impostor)
	roleBuf := make([]byte, 256)
	*(*int32)(unsafe.Pointer(&roleBuf[0x20])) = 5
	*(*int32)(unsafe.Pointer(&roleBuf[0x74])) = 1
	rolePtr := uintptr(unsafe.Pointer(&roleBuf[0]))

	// NetworkedPlayerInfo:
	// Role ptr at +0x68 = rolePtr
	infoBuf := make([]byte, 256)
	infoBuf[0x38] = 2
	*(*uintptr)(unsafe.Pointer(&infoBuf[0x68])) = rolePtr
	infoPtr := uintptr(unsafe.Pointer(&infoBuf[0]))

	offsets := &GameOffsets{
		PlayerInfoStructOffsets: PlayerInfoStructOffsets{
			PlayerIDOffset:     0x38,
			RoleTypeOffset:     []int{0x68, 0x20},
			RoleTeamTypeOffset: []int{0x68, 0x74},
			IsDeadOffset:       0x78,
		},
	}

	pInfo, err := gr.extractPlayerInfoFromData(mr, infoPtr, offsets)
	if err != nil {
		t.Fatalf("extractPlayerInfoFromData returned error: %v", err)
	}

	if pInfo.RoleType != 5 {
		t.Errorf("Expected RoleType=5 (Shapeshifter), got %d", pInfo.RoleType)
	}
	if pInfo.RoleName != "Shapeshifter" {
		t.Errorf("Expected RoleName='Shapeshifter', got '%s'", pInfo.RoleName)
	}
	if pInfo.RoleNameJa != "シェイプシフター" {
		t.Errorf("Expected RoleNameJa='シェイプシフター', got '%s'", pInfo.RoleNameJa)
	}
	if !pInfo.IsImpostor {
		t.Errorf("Expected IsImpostor=true for Shapeshifter, got false")
	}
}


