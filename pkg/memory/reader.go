package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

// GameListener defines callbacks for game events
type GameListener interface {
	OnGameStateChanged(oldState, newState GameState)
	OnPlayerChanged(event PlayerChangeEvent)
	OnLobbyUpdated(event LobbyEvent)
	OnGameOver(event GameOverEvent)
	OnHookStatusChanged(hooked bool, processName string, gameHash string)
}

// DebugLogFunc receives diagnostic logs
type DebugLogFunc func(category, level, message string)

// GameReader polls and interprets the Among Us memory state based on standard AmongUsCapture architecture
type GameReader struct {
	mu             sync.RWMutex
	offsetManager  *OffsetManager
	listeners      []GameListener
	currentProcess *ProcessInfo
	memoryReader   *MemoryReader
	offsets        *GameOffsets
	scannedSigs    ScannedPointers

	// Game state tracking
	currentState     GameState
	currentLobbyCode string
	currentRegion    int32
	currentMap       int32
	currentPlayers   map[byte]PlayerInfo
	lastMatchResult  *MatchResult

	pollInterval time.Duration
	stopChan     chan struct{}
	running      bool
	debugLogger  DebugLogFunc

	lastStateLogTime time.Time
	lastRawVal       int32

	cachedAmongUsClientInst uintptr
	cachedGameDataInst       uintptr
	cachedAllPlayersListAddr uintptr
}

// NewGameReader creates a new GameReader instance
func NewGameReader(om *OffsetManager) *GameReader {
	return &GameReader{
		offsetManager:  om,
		currentState:   StateMenu, // Default to MENU, avoiding UNKNOWN
		currentPlayers: make(map[byte]PlayerInfo),
		pollInterval:   150 * time.Millisecond,
		stopChan:       make(chan struct{}),
	}
}

// SetDebugLogger registers an optional callback for memory tracking logs
func (gr *GameReader) SetDebugLogger(logger DebugLogFunc) {
	gr.mu.Lock()
	defer gr.mu.Unlock()
	gr.debugLogger = logger
}

func (gr *GameReader) debugLog(category, level, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	gr.mu.RLock()
	logger := gr.debugLogger
	gr.mu.RUnlock()

	if logger != nil {
		logger(category, level, msg)
	}
}

// AddListener registers a game event listener
func (gr *GameReader) AddListener(l GameListener) {
	gr.mu.Lock()
	defer gr.mu.Unlock()
	gr.listeners = append(gr.listeners, l)
}

// Start begins the memory reading polling loop
func (gr *GameReader) Start(ctx context.Context) {
	gr.mu.Lock()
	if gr.running {
		gr.mu.Unlock()
		return
	}
	gr.running = true
	gr.mu.Unlock()

	ticker := time.NewTicker(gr.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			gr.Stop()
			return
		case <-gr.stopChan:
			return
		case <-ticker.C:
			gr.tick()
		}
	}
}

// Stop terminates the polling loop and releases process handles
func (gr *GameReader) Stop() {
	gr.mu.Lock()
	defer gr.mu.Unlock()
	if !gr.running {
		return
	}
	gr.running = false
	close(gr.stopChan)
	gr.unhookProcess()
}

func (gr *GameReader) tick() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[GameReader] Recovered from panic in tick: %v", r)
		}
	}()

	if !gr.isHooked() {
		gr.tryHookProcess()
		return
	}

	if err := gr.verifyProcess(); err != nil {
		log.Printf("[GameReader] Process disconnected: %v", err)
		gr.unhookProcess()
		return
	}

	gr.mu.RLock()
	mr := gr.memoryReader
	proc := gr.currentProcess
	offsets := gr.offsets
	sigs := gr.scannedSigs
	gr.mu.RUnlock()

	if mr == nil || proc == nil || offsets == nil {
		return
	}

	base := proc.GameAssemblyBase

	// 1. Poll Game State (MENU, LOBBY, TASKS, DISCUSSION, GAME_OVER)
	gr.pollGameState(mr, base, offsets, sigs)

	// 2. Poll Lobby Info
	gr.pollLobby(mr, base, offsets, sigs)

	// 3. Poll Players (PlayerControl.AllPlayerControls list tracking)
	gr.pollPlayers(mr, base, offsets, sigs)

	// 4. Poll Game Over
	gr.pollGameOver(mr, base, offsets)
}

func (gr *GameReader) isHooked() bool {
	gr.mu.RLock()
	defer gr.mu.RUnlock()
	return gr.currentProcess != nil && gr.memoryReader != nil
}

// tryHookProcess scans for Among Us.exe and initializes memory reading
func (gr *GameReader) tryHookProcess() {
	proc, err := FindAmongUsProcess()
	if err != nil {
		return
	}

	mr := NewMemoryReader(proc.Handle, proc.PID, proc.Is64Bit)

	log.Printf("[GameReader] Hooked Among Us (PID %d, 64-bit: %v, GameAssembly: 0x%X, Hash: %s)",
		proc.PID, proc.Is64Bit, proc.GameAssemblyBase, proc.GameHash)

	// 1. Check offset table by GameAssembly hash
	var matchedOffsets *GameOffsets
	if proc.GameHash != "" && gr.offsetManager != nil {
		matchedOffsets = gr.offsetManager.GetOffsetByHash(proc.GameHash)
		if matchedOffsets != nil {
			log.Printf("[GameReader] Matched official offsets by hash: %s", matchedOffsets.Description)
		}
	}

	// 2. Fall back to default architecture offsets
	if matchedOffsets == nil && gr.offsetManager != nil {
		matchedOffsets = gr.offsetManager.GetDefaultOffsets(proc.Is64Bit)
		if matchedOffsets != nil {
			log.Printf("[GameReader] Using default %s offsets: %s", func() string {
				if proc.Is64Bit {
					return "x64"
				}
				return "x86"
			}(), matchedOffsets.Description)
		}
	}

	// 3. Dynamic AOB signature scan ONLY as a fallback if no hash and no default matched
	var sigs ScannedPointers
	if matchedOffsets == nil {
		scanner := NewAOBScanner(mr, proc.GameAssemblyBase, proc.GameAssemblySize, proc.Is64Bit)
		sigs = scanner.ScanAllSignatures()
		resolved, err := scanner.ResolveOffsetsDynamically(matchedOffsets)
		if err == nil && resolved != nil {
			matchedOffsets = resolved
		}
	}

	gr.mu.Lock()
	gr.currentProcess = proc
	gr.memoryReader = mr
	gr.offsets = matchedOffsets
	gr.scannedSigs = sigs
	gr.cachedAmongUsClientInst = 0
	gr.cachedGameDataInst = 0
	gr.cachedAllPlayersListAddr = 0
	gr.currentState = StateMenu
	gr.currentPlayers = make(map[byte]PlayerInfo)
	gr.mu.Unlock()

	gr.notifyHookStatus(true, "Among Us.exe", proc.GameHash)
	gr.debugLog("HOOK", "success", "プロセスのフックに成功しました (GameAssembly: 0x%X)", proc.GameAssemblyBase)
}

func (gr *GameReader) unhookProcess() {
	gr.mu.Lock()
	if gr.memoryReader != nil {
		_ = gr.memoryReader.Close()
		gr.memoryReader = nil
	}
	gr.currentProcess = nil
	gr.offsets = nil
	gr.cachedAmongUsClientInst = 0
	gr.cachedGameDataInst = 0
	gr.cachedAllPlayersListAddr = 0
	gr.currentState = StateMenu
	gr.currentPlayers = make(map[byte]PlayerInfo)
	gr.mu.Unlock()

	gr.notifyHookStatus(false, "", "")
	gr.debugLog("HOOK", "warn", "プロセスの接続が切断されました")
}

func (gr *GameReader) verifyProcess() error {
	gr.mu.RLock()
	mr := gr.memoryReader
	proc := gr.currentProcess
	gr.mu.RUnlock()

	if mr == nil || proc == nil {
		return fmt.Errorf("no process attached")
	}

	// Read first 2 bytes (MZ header) of GameAssembly.dll to verify memory access
	buf, err := mr.ReadRaw(proc.GameAssemblyBase, 2)
	if err != nil || len(buf) < 2 || buf[0] != 'M' || buf[1] != 'Z' {
		return fmt.Errorf("process memory inaccessible or exited: %v", err)
	}
	return nil
}

// pollGameState reads and determines the current GameState
func (gr *GameReader) pollGameState(mr *MemoryReader, base uintptr, offsets *GameOffsets, sigs ScannedPointers) {
	newState, rawVal, readAddr := gr.readRawGameState(mr, base, offsets, sigs)

	gr.mu.Lock()
	oldState := gr.currentState
	stateChanged := newState != oldState
	if stateChanged {
		gr.currentState = newState
	}
	now := time.Now()
	shouldLogHeartbeat := now.Sub(gr.lastStateLogTime) >= 1200*time.Millisecond
	if shouldLogHeartbeat {
		gr.lastStateLogTime = now
		gr.lastRawVal = rawVal
	}
	lobby := gr.currentLobbyCode
	playerCount := len(gr.currentPlayers)
	gr.mu.Unlock()

	// 1. If state changed, immediately dispatch event and log prominently
	if stateChanged {
		gr.notifyStateChange(oldState, newState)
		log.Printf("[GameReader:GameState] ★★★ 状態変化検知: %s (%d) -> %s (%d) [rawValue=%d, Addr=0x%X, Lobby=%s, Players=%d] ★★★",
			oldState.String(), int(oldState), newState.String(), int(newState), rawVal, readAddr, lobby, playerCount)
		gr.debugLog("STATE", "warn", "GameState変化検知: %s (%d) -> %s (%d) [rawValue=%d, Addr=0x%X]",
			oldState.String(), int(oldState), newState.String(), int(newState), rawVal, readAddr)
	} else if shouldLogHeartbeat {
		// 2. Periodic debug log for requirements: rawValue confirmation
		log.Printf("[GameReader:GameState] 生メモリ監視: rawValue = %d (ステート: %s (%d), 読取Addr: 0x%X, ロビー: %s, 人数: %d)",
			rawVal, newState.String(), int(newState), readAddr, lobby, playerCount)
		gr.debugLog("STATE", "info", "生メモリ値: rawValue = %d (ステート: %s, ロビー: %s)",
			rawVal, newState.String(), lobby)
	}
}

// resolveAmongUsClientInstance resolves and caches the AmongUsClient.Instance singleton pointer
func (gr *GameReader) resolveAmongUsClientInstance(mr *MemoryReader, base uintptr, offsets *GameOffsets) (uintptr, error) {
	gr.mu.RLock()
	cached := gr.cachedAmongUsClientInst
	gr.mu.RUnlock()

	if cached != 0 {
		classPtr, err := mr.ReadPtr(cached)
		if err == nil && classPtr > base && classPtr < base+0x20000000 {
			return cached, nil
		}
		gr.mu.Lock()
		gr.cachedAmongUsClientInst = 0
		gr.mu.Unlock()
	}

	clientOffset := offsets.AmongUsClientOffset
	if clientOffset == 0 && len(offsets.GameStateOffsets) > 0 {
		clientOffset = offsets.GameStateOffsets[0]
	}
	if clientOffset == 0 {
		return 0, fmt.Errorf("AmongUsClient offset not defined")
	}

	classAddr := base + uintptr(clientOffset)
	classPtr, err := mr.ReadPtr(classAddr)
	if err != nil || classPtr == 0 {
		return 0, fmt.Errorf("failed to read AmongUsClient class at 0x%X: %w", classAddr, err)
	}

	var staticOffset uintptr = 0xB8
	if !mr.is64 {
		staticOffset = 0x5C
	}
	staticFields, err := mr.ReadPtr(classPtr + staticOffset)
	if err != nil || staticFields == 0 || staticFields < 0x10000 {
		return 0, fmt.Errorf("failed to read AmongUsClient static_fields at 0x%X: %w", classPtr+staticOffset, err)
	}

	inst, err := mr.ReadPtr(staticFields + 0x0)
	if err != nil || inst == 0 || inst < 0x10000 {
		return 0, fmt.Errorf("AmongUsClient.Instance is null at static_fields(0x%X)+0x0", staticFields)
	}

	gr.mu.Lock()
	gr.cachedAmongUsClientInst = inst
	gr.mu.Unlock()

	log.Printf("[GameReader] Successfully resolved and cached AmongUsClient.Instance at 0x%X", inst)
	gr.debugLog("OFFSET", "success", "AmongUsClient.Instance解決・キャッシュ: 0x%X", inst)
	return inst, nil
}

// resolveGameDataInstance resolves and caches the GameData.Instance singleton pointer
func (gr *GameReader) resolveGameDataInstance(mr *MemoryReader, base uintptr, offsets *GameOffsets) (uintptr, error) {
	gr.mu.RLock()
	cached := gr.cachedGameDataInst
	gr.mu.RUnlock()

	if cached != 0 {
		classPtr, err := mr.ReadPtr(cached)
		if err == nil && classPtr > base && classPtr < base+0x20000000 {
			return cached, nil
		}
		gr.mu.Lock()
		gr.cachedGameDataInst = 0
		gr.cachedAllPlayersListAddr = 0
		gr.mu.Unlock()
	}

	dataOffset := offsets.GameDataOffset
	if dataOffset == 0 && len(offsets.AllPlayerPtrOffsets) > 0 {
		dataOffset = offsets.AllPlayerPtrOffsets[0]
	}
	if dataOffset == 0 {
		return 0, fmt.Errorf("GameData offset not defined")
	}

	classAddr := base + uintptr(dataOffset)
	classPtr, err := mr.ReadPtr(classAddr)
	if err != nil || classPtr == 0 {
		return 0, fmt.Errorf("failed to read GameData class at 0x%X: %w", classAddr, err)
	}

	var staticOffset uintptr = 0xB8
	if !mr.is64 {
		staticOffset = 0x5C
	}
	staticFields, err := mr.ReadPtr(classPtr + staticOffset)
	if err != nil || staticFields == 0 || staticFields < 0x10000 {
		return 0, fmt.Errorf("failed to read GameData static_fields at 0x%X: %w", classPtr+staticOffset, err)
	}

	inst, err := mr.ReadPtr(staticFields + 0x0)
	if err != nil || inst == 0 || inst < 0x10000 {
		return 0, fmt.Errorf("GameData.Instance is null at static_fields(0x%X)+0x0", staticFields)
	}

	gr.mu.Lock()
	gr.cachedGameDataInst = inst
	gr.mu.Unlock()

	log.Printf("[GameReader] Successfully resolved and cached GameData.Instance at 0x%X", inst)
	gr.debugLog("OFFSET", "success", "GameData.Instance解決・キャッシュ: 0x%X", inst)
	return inst, nil
}

// readRawGameState reads GameState from memory using AmongUsClient.Instance and official offsets
func (gr *GameReader) readRawGameState(mr *MemoryReader, base uintptr, offsets *GameOffsets, sigs ScannedPointers) (GameState, int32, uintptr) {
	inst, err := gr.resolveAmongUsClientInstance(mr, base, offsets)
	var rawVal int32 = -1
	var readAddr uintptr = 0

	if err == nil && inst > 0 {
		var stateOff uintptr = 0xAC
		if len(offsets.GameStateOffsets) > 0 {
			stateOff = uintptr(offsets.GameStateOffsets[len(offsets.GameStateOffsets)-1])
		}
		readAddr = inst + stateOff
		val, err := mr.ReadInt32(readAddr)
		if err == nil && val >= 0 && val <= 4 {
			rawVal = val
		}
	}

	// Check MeetingHud for discussion phase
	meetingActive := gr.isMeetingActive(mr, base, offsets, sigs)

	gr.mu.RLock()
	prev := gr.currentState
	lobby := gr.currentLobbyCode
	playerCount := len(gr.currentPlayers)
	gr.mu.RUnlock()

	hasLobbyCode := (len(lobby) == 4 || len(lobby) == 6) && (isAllAlpha(lobby) || lobby == "******")
	hasPlayers := playerCount > 0

	var derivedState GameState

	switch rawVal {
	case 1:
		derivedState = StateLobby
	case 2:
		if meetingActive {
			derivedState = StateDiscussion
		} else {
			derivedState = StateTasks
		}
	case 3:
		derivedState = StateGameOver
	case 4:
		derivedState = StateGameOver
	case 0:
		// When in room or in match, rawVal=0 may be transient before AmongUsClient initializes fully
		if hasLobbyCode || hasPlayers {
			if prev == StateTasks || prev == StateDiscussion {
				if meetingActive {
					derivedState = StateDiscussion
				} else {
					derivedState = StateTasks
				}
			} else {
				derivedState = StateLobby
			}
		} else {
			derivedState = StateMenu
		}
	default:
		// When rawVal could not be read directly
		if meetingActive {
			derivedState = StateDiscussion
		} else if hasLobbyCode || hasPlayers {
			if prev == StateTasks || prev == StateDiscussion {
				derivedState = StateTasks
			} else {
				derivedState = StateLobby
			}
		} else if prev >= StateMenu && prev <= StateGameOver {
			derivedState = prev
		} else {
			derivedState = StateMenu
		}
	}

	return derivedState, rawVal, readAddr
}

func (gr *GameReader) isMeetingActive(mr *MemoryReader, base uintptr, offsets *GameOffsets, sigs ScannedPointers) bool {
	if len(offsets.MeetingHudPtr) == 0 {
		return false
	}
	meetingHud, err := mr.ReadPtrOffsets(base, offsets.MeetingHudPtr)
	if err != nil || meetingHud == 0 || meetingHud < 0x10000 {
		return false
	}

	// MeetingHudCachePtrOffsets: [0x10]
	var cacheOffset uintptr = 0x10
	if len(offsets.MeetingHudCachePtrOffsets) > 0 {
		cacheOffset = uintptr(offsets.MeetingHudCachePtrOffsets[0])
	}
	cachePtr, err := mr.ReadPtr(meetingHud + cacheOffset)
	if err != nil || cachePtr == 0 {
		return false
	}

	// MeetingHudStateOffsets: [0xE0]
	var stateOffset uintptr = 0xE0
	if len(offsets.MeetingHudStateOffsets) > 0 {
		stateOffset = uintptr(offsets.MeetingHudStateOffsets[0])
	}
	meetingState, err := mr.ReadInt32(meetingHud + stateOffset)
	if err != nil {
		return false
	}

	// 0 = Discussion, 1 = NotVoted, 2 = Voted, 3 = Results, 4 = Proceeding
	return meetingState < 4
}

// pollLobby reads game room code, region, and map
func (gr *GameReader) pollLobby(mr *MemoryReader, base uintptr, offsets *GameOffsets, sigs ScannedPointers) {
	// Method 1: GameCodeOffsets pointer chain to string
	if len(offsets.GameCodeOffsets) > 0 {
		strPtr, err := mr.ReadPtrOffsets(base, offsets.GameCodeOffsets)
		if err == nil && strPtr > 0x10000 {
			codeStr, err := mr.ReadIl2cppString(strPtr)
			if err == nil {
				codeStr = strings.TrimSpace(codeStr)
				if (len(codeStr) == 4 || len(codeStr) == 6) && (isAllAlpha(codeStr) || codeStr == "******") {
					gr.mu.Lock()
					if gr.currentLobbyCode != codeStr {
						gr.currentLobbyCode = codeStr
						gr.mu.Unlock()
						gr.notifyLobby(LobbyEvent{LobbyCode: codeStr})
						gr.debugLog("LOBBY", "info", "ロビーコード取得: %s", codeStr)
					} else {
						gr.mu.Unlock()
					}
					return
				}
			}
		}
	}

	// Method 2: GameId from cached or resolved AmongUsClient.Instance
	inst, err := gr.resolveAmongUsClientInstance(mr, base, offsets)
	if err == nil && inst > 0 {
		var gameIdOffset uintptr = 0x44
		if !mr.is64 {
			gameIdOffset = 0x24
		}
		gameId, err := mr.ReadInt32(inst + gameIdOffset)
		if err == nil && gameId != 0 {
			decoded := DecodeGameCode(gameId)
			if len(decoded) == 6 && isAllAlpha(decoded) {
				gr.mu.Lock()
				if gr.currentLobbyCode != decoded {
					gr.currentLobbyCode = decoded
					gr.mu.Unlock()
					gr.notifyLobby(LobbyEvent{LobbyCode: decoded})
					gr.debugLog("LOBBY", "info", "GameIdよりロビーコード解読: %s", decoded)
				} else {
					gr.mu.Unlock()
				}
				return
			}
		}
	}
}

func isAllAlpha(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i := 0; i < len(s); i++ {
		b := s[i]
		if !(b >= 'A' && b <= 'Z') && !(b >= 'a' && b <= 'z') {
			return false
		}
	}
	return true
}

// pollPlayers extracts all players from PlayerControl.AllPlayerControls or GameData.Instance.AllPlayers
func (gr *GameReader) pollPlayers(mr *MemoryReader, base uintptr, offsets *GameOffsets, sigs ScannedPointers) {
	players, err := gr.readAllPlayers(mr, base, offsets, sigs)
	if err != nil {
		log.Printf("[GameReader] Failed to read player list: %v", err)
		gr.debugLog("PLAYER", "warn", "Failed to read player list: %v", err)
		return
	}

	newMap := make(map[byte]PlayerInfo, len(players))
	for _, p := range players {
		newMap[p.PlayerID] = p
	}

	gr.mu.Lock()
	oldMap := gr.currentPlayers
	gr.currentPlayers = newMap
	gr.mu.Unlock()

	// Compare changes and dispatch events
	for id, newP := range newMap {
		oldP, exists := oldMap[id]
		if !exists {
			gr.notifyPlayer(PlayerChangeEvent{
				Action:       ActionJoined,
				Name:         newP.Name,
				IsDead:       newP.IsDead,
				Disconnected: newP.Disconnected,
				Color:        newP.Color,
			})
			gr.debugLog("PLAYER", "success", "参加: %s (ID: %d, 色: %s)", newP.Name, id, newP.Color.String())
		} else {
			if !oldP.IsDead && newP.IsDead {
				gr.notifyPlayer(PlayerChangeEvent{
					Action:       ActionDied,
					Name:         newP.Name,
					IsDead:       true,
					Disconnected: newP.Disconnected,
					Color:        newP.Color,
				})
				gr.debugLog("PLAYER", "danger", "死亡: %s (ID: %d, 色: %s)", newP.Name, id, newP.Color.String())
			}
			if oldP.Color != newP.Color {
				gr.notifyPlayer(PlayerChangeEvent{
					Action:       ActionChangedColor,
					Name:         newP.Name,
					IsDead:       newP.IsDead,
					Disconnected: newP.Disconnected,
					Color:        newP.Color,
				})
			}
			if !oldP.Disconnected && newP.Disconnected {
				gr.notifyPlayer(PlayerChangeEvent{
					Action:       ActionDisconnected,
					Name:         newP.Name,
					IsDead:       newP.IsDead,
					Disconnected: true,
					Color:        newP.Color,
				})
			}
		}
	}

	for id, oldP := range oldMap {
		if _, exists := newMap[id]; !exists {
			gr.notifyPlayer(PlayerChangeEvent{
				Action:       ActionLeft,
				Name:         oldP.Name,
				IsDead:       oldP.IsDead,
				Disconnected: oldP.Disconnected,
				Color:        oldP.Color,
			})
			gr.debugLog("PLAYER", "info", "退出: %s (ID: %d)", oldP.Name, id)
		}
	}
}

// resolvePointerChainWithStepLog dereferences a pointer chain step by step with verbose debug logging and null checks
func (gr *GameReader) resolvePointerChainWithStepLog(name string, mr *MemoryReader, base uintptr, offsets []int) (uintptr, error) {
	if len(offsets) == 0 {
		return base, nil
	}
	current := base
	gr.debugLog("OFFSET", "info", "[%s] Starting pointer chain from Base=0x%X (Steps=%d)", name, base, len(offsets))

	for step, offset := range offsets {
		targetAddr := current + uintptr(offset)
		ptr, err := mr.ReadPtr(targetAddr)
		if err != nil {
			msg := fmt.Sprintf("[%s] Step %d FAILED: cannot read at 0x%X (Prev=0x%X + Offset=0x%X): %v",
				name, step, targetAddr, current, offset, err)
			log.Printf("[GameReader] %s", msg)
			gr.debugLog("OFFSET", "warn", "%s", msg)
			return 0, fmt.Errorf("%s", msg)
		}

		if ptr == 0 {
			msg := fmt.Sprintf("[%s] Step %d resolved to 0x0 (NULL) at 0x%X (Prev=0x%X + Offset=0x%X)",
				name, step, targetAddr, current, offset)
			log.Printf("[GameReader] %s", msg)
			gr.debugLog("OFFSET", "warn", "%s", msg)
			return 0, fmt.Errorf("%s", msg)
		}

		log.Printf("[GameReader] [%s] Step %d: Read 0x%X + 0x%X -> 0x%X", name, step, current, offset, ptr)
		gr.debugLog("OFFSET", "info", "[%s] Step %d: Read 0x%X + 0x%X -> 0x%X", name, step, current, offset, ptr)
		current = ptr
	}

	return current, nil
}

// probePlayerList attempts to unpack a List<T> pointer and extract valid players
func (gr *GameReader) probePlayerList(mr *MemoryReader, listPtr uintptr, offsets *GameOffsets, isControlList bool) []PlayerInfo {
	if listPtr == 0 || listPtr < 0x10000 {
		return nil
	}

	var ptrSize uintptr = 8
	var listItemsOffset uintptr = 0x10
	var listSizeOffset uintptr = 0x18
	var arrayStartOffset uintptr = 0x20
	if !mr.is64 {
		ptrSize = 4
		listItemsOffset = 0x8
		listSizeOffset = 0xC
		arrayStartOffset = 0x10
	}
	if len(offsets.AllPlayersOffsets) > 0 {
		listItemsOffset = uintptr(offsets.AllPlayersOffsets[0])
	}
	if len(offsets.PlayerCountOffsets) > 0 {
		listSizeOffset = uintptr(offsets.PlayerCountOffsets[0])
	}
	if offsets.PlayerListPtr > 0 {
		arrayStartOffset = uintptr(offsets.PlayerListPtr)
	}
	if offsets.AddPlayerPtr > 0 {
		ptrSize = uintptr(offsets.AddPlayerPtr)
	}

	count, err := mr.ReadInt32(listPtr + listSizeOffset)
	if err != nil || count < 0 || count > 30 {
		return nil
	}
	if count == 0 {
		return []PlayerInfo{}
	}

	itemsArray, err := mr.ReadPtr(listPtr + listItemsOffset)
	if err != nil || itemsArray == 0 || itemsArray < 0x10000 {
		return nil
	}

	var result []PlayerInfo
	seenIDs := make(map[byte]bool)

	for i := 0; i < int(count); i++ {
		elemAddr := itemsArray + arrayStartOffset + uintptr(i)*ptrSize
		elemPtr, err := mr.ReadPtr(elemAddr)
		if err != nil || elemPtr == 0 || elemPtr < 0x10000 {
			continue
		}

		var pInfo PlayerInfo
		if isControlList {
			// elemPtr is PlayerControl instance -> resolve PlayerControl.CachedPlayerData to NetworkedPlayerInfo
			var dataPtr uintptr
			dataOffsets := []uintptr{0x78, 0x60, 0x68, 0x58, 0x70}
			if !mr.is64 {
				dataOffsets = []uintptr{0x34, 0x30, 0x2C}
			}
			for _, dOff := range dataOffsets {
				dPtr, err := mr.ReadPtr(elemPtr + dOff)
				if err == nil && dPtr > 0x10000 {
					dataPtr = dPtr
					break
				}
			}
			if dataPtr > 0 {
				pInfo, err = gr.extractPlayerInfoFromData(mr, dataPtr, offsets)
			} else {
				pInfo, err = gr.extractPlayerInfoFromData(mr, elemPtr, offsets)
			}
		} else {
			// elemPtr is directly GameData.PlayerInfo
			pInfo, err = gr.extractPlayerInfoFromData(mr, elemPtr, offsets)
		}

		if err == nil && !seenIDs[pInfo.PlayerID] {
			seenIDs[pInfo.PlayerID] = true
			result = append(result, pInfo)
		}
	}

	return result
}

// readAllPlayers attempts standard AmongUsCapture tracking routes with step logging and fallbacks
func (gr *GameReader) readAllPlayers(mr *MemoryReader, base uintptr, offsets *GameOffsets, sigs ScannedPointers) ([]PlayerInfo, error) {
	gr.mu.RLock()
	curState := gr.currentState
	gr.mu.RUnlock()

	// In MENU state, absence of players is normal game behavior
	if curState == StateMenu {
		gr.debugLog("PLAYER", "info", "MENU待機中のためプレイヤーデータは未生成（0件）です")
		return []PlayerInfo{}, nil
	}

	// Route 1: GameData.Instance.AllPlayers (Standard AmongUsCapture route)
	players, errGD := gr.readPlayersFromGameData(mr, base, offsets, sigs)
	if errGD == nil {
		return players, nil
	}

	// Route 2: Fallback to PlayerControl.AllPlayerControls if GameData failed
	players, errPC := gr.readPlayersFromPlayerControl(mr, base, offsets, sigs)
	if errPC == nil && len(players) > 0 {
		return players, nil
	}

	return nil, fmt.Errorf("GameData route failed: %v", errGD)
}

// readPlayersFromPlayerControl scans PlayerControl.AllPlayerControls List with multi-slot probing
func (gr *GameReader) readPlayersFromPlayerControl(mr *MemoryReader, base uintptr, offsets *GameOffsets, sigs ScannedPointers) ([]PlayerInfo, error) {
	var pcClassCandidates []uintptr

	if offsets.PlayerControlOffset > 0 {
		pcClassCandidates = append(pcClassCandidates, base+uintptr(offsets.PlayerControlOffset))
	}
	if sigs.PlayerControlClassPtr > 0 {
		pcClassCandidates = append(pcClassCandidates, sigs.PlayerControlClassPtr)
	}

	if mr.is64 {
		pcClassCandidates = append(pcClassCandidates, base+0x2A9C128, base+0x2AA6948, base+0x2A65F38)
	}

	var staticOffset uintptr = 0xB8
	var ptrSize uintptr = 8
	if !mr.is64 {
		staticOffset = 0x5C
		ptrSize = 4
	}

	var lastErr error
	for idx, cand := range pcClassCandidates {
		classPtr := cand
		val, err := mr.ReadPtr(classPtr)
		if err == nil && val > base && val < base+0x10000000 {
			classPtr = val
		}

		staticFields, err := mr.ReadPtr(classPtr + staticOffset)
		if err != nil || staticFields == 0 || staticFields < 0x10000 {
			lastErr = fmt.Errorf("static_fields at 0x%X is invalid (0x%X)", classPtr+staticOffset, staticFields)
			continue
		}

		probeOffsets := []uintptr{0x0, ptrSize, ptrSize * 2, ptrSize * 3, ptrSize * 4, ptrSize * 5, ptrSize * 6}
		for _, pOff := range probeOffsets {
			listPtr, err := mr.ReadPtr(staticFields + pOff)
			if err != nil || listPtr == 0 || listPtr < 0x10000 {
				continue
			}

			players := gr.probePlayerList(mr, listPtr, offsets, true)
			if len(players) > 0 {
				log.Printf("[GameReader:PlayerControl] Successfully found AllPlayerControls at static_fields+0x%X (count: %d players)", pOff, len(players))
				return players, nil
			}
		}
		_ = idx
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("PlayerControl AllPlayerControls list not found in scanned slots")
}

// readPlayersFromGameData scans GameData.Instance.AllPlayers List with cached instance access and official offsets
func (gr *GameReader) readPlayersFromGameData(mr *MemoryReader, base uintptr, offsets *GameOffsets, sigs ScannedPointers) ([]PlayerInfo, error) {
	if len(offsets.AllPlayerPtrOffsets) < 2 {
		return nil, fmt.Errorf("AllPlayerPtrOffsets not defined")
	}

	inst, err := gr.resolveGameDataInstance(mr, base, offsets)
	if err != nil {
		return nil, err
	}

	// Step 1: Read AllPlayers List from GameData.Instance (typically at +0x20)
	listFieldOffset := uintptr(offsets.AllPlayerPtrOffsets[len(offsets.AllPlayerPtrOffsets)-1])
	listAddr := inst + listFieldOffset
	listPtr, err := mr.ReadPtr(listAddr)
	if err == nil && listPtr > 0x10000 {
		players := gr.probePlayerList(mr, listPtr, offsets, false)
		if players != nil {
			gr.mu.Lock()
			gr.cachedAllPlayersListAddr = listPtr
			gr.mu.Unlock()
			return players, nil
		}
	}

	// Step 2: Fallback field probing on GameData.Instance
	fieldProbeOffsets := []uintptr{0x20, 0x24, 0x28, 0x30, 0x38, 0x40, 0x48}
	for _, fOff := range fieldProbeOffsets {
		if fOff == listFieldOffset {
			continue
		}
		target := inst + fOff
		p, err := mr.ReadPtr(target)
		if err != nil || p == 0 || p < 0x10000 {
			continue
		}
		players := gr.probePlayerList(mr, p, offsets, false)
		if players != nil {
			log.Printf("[GameReader:GameData] Found AllPlayers List at fallback offset 0x%X (count: %d)", fOff, len(players))
			gr.mu.Lock()
			gr.cachedAllPlayersListAddr = p
			gr.mu.Unlock()
			return players, nil
		}
	}

	return nil, fmt.Errorf("GameData.Instance found at 0x%X, but AllPlayers list is null/empty at 0x%X", inst, listAddr)
}

// extractPlayerInfoFromData extracts player fields (Name, PlayerId, ColorId, IsDead, IsDisconnected, Role)
func (gr *GameReader) extractPlayerInfoFromData(mr *MemoryReader, playerPtr uintptr, offsets *GameOffsets) (PlayerInfo, error) {
	if playerPtr == 0 {
		return PlayerInfo{}, fmt.Errorf("null player pointer")
	}

	pOf := offsets.PlayerInfoStructOffsets
	oOf := offsets.PlayerOutfitStructOffsets

	// Resolve whether playerPtr is PlayerControl or NetworkedPlayerInfo
	// In PlayerControl (x64), offset 0x78 is CachedPlayerData (pointer to NetworkedPlayerInfo).
	// In NetworkedPlayerInfo (x64), offset 0x78 is IsDead (1-byte bool, 0 or 1).
	// If reading a 64-bit pointer at +0x78 gives a valid pointer > 0x10000,
	// playerPtr is PlayerControl, and that pointer is the NetworkedPlayerInfo!
	targetPlayerInfoPtr := playerPtr
	if mr.is64 {
		ptrAt78, errPtr78 := mr.ReadPtr(playerPtr + 0x78)
		if errPtr78 == nil && ptrAt78 > 0x10000 && ptrAt78 < 0x7FFFFFFFFFFF {
			targetPlayerInfoPtr = ptrAt78
		}
	}

	// 1. PlayerID
	var idOffset uintptr = 0x38
	if !mr.is64 {
		idOffset = 0x8
	}
	if pOf.PlayerIDOffset > 0 {
		idOffset = uintptr(pOf.PlayerIDOffset)
	}
	pID, err := mr.ReadUInt8(targetPlayerInfoPtr + idOffset)
	if err != nil || pID > 30 {
		pID = 0
	}

	// 2. IsDead (survival / death status)
	var deadOffset uintptr = 0x78
	if !mr.is64 {
		deadOffset = 0x30
	}
	if pOf.IsDeadOffset > 0 {
		deadOffset = uintptr(pOf.IsDeadOffset)
	}
	isDeadRaw, err := mr.ReadUInt8(targetPlayerInfoPtr + deadOffset)
	var isDead bool
	if err == nil {
		isDead = isDeadRaw > 0
	}

	// Also check WasEjected (offset 0x79 on x64)
	if !isDead && mr.is64 {
		wasEjectedRaw, errEject := mr.ReadUInt8(targetPlayerInfoPtr + deadOffset + 1)
		if errEject == nil && wasEjectedRaw > 0 {
			isDead = true
		}
	}

	// Logical state guard: When in LOBBY, players are ALWAYS alive (IsDead = false)
	gr.mu.RLock()
	curState := gr.currentState
	gr.mu.RUnlock()
	if curState == StateLobby {
		isDead = false
	}

	// 3. Disconnected
	var disconnOffset uintptr = 0x64
	if !mr.is64 {
		disconnOffset = 0x24
	}
	if pOf.DisconnectedOffset > 0 {
		disconnOffset = uintptr(pOf.DisconnectedOffset)
	}
	disconnRaw, err := mr.ReadUInt8(targetPlayerInfoPtr + disconnOffset)
	var isDisconnected bool
	if err == nil {
		isDisconnected = disconnRaw == 1
	}

	// 4. Role (RoleType / TeamType / RoleBehaviour)
	var roleType int32 = 0
	var roleTeamType int32 = 0
	var roleRawVal int32 = 0

	// Route 1: Via Role pointer at targetPlayerInfoPtr + 0x68 (points to RoleBehaviour)
	var rolePtrOffset uintptr = 0x68
	if !mr.is64 {
		rolePtrOffset = 0x28
	}
	if len(pOf.RoleTypeOffset) > 0 {
		rolePtrOffset = uintptr(pOf.RoleTypeOffset[0])
	}
	rolePtr, errRole := mr.ReadPtr(targetPlayerInfoPtr + rolePtrOffset)
	if errRole == nil && rolePtr > 0x10000 {
		// Read RoleType on RoleBehaviour (+0x20 on x64, +0xC on x86)
		var rTypeOffset uintptr = 0x20
		if !mr.is64 {
			rTypeOffset = 0xC
		}
		if len(pOf.RoleTypeOffset) > 1 {
			rTypeOffset = uintptr(pOf.RoleTypeOffset[1])
		}
		if rt, err := mr.ReadInt32(rolePtr + rTypeOffset); err == nil && rt >= 0 && rt <= 30 {
			roleType = rt
			roleRawVal = rt
		}

		// Read TeamType on RoleBehaviour (+0x74 on x64, +0x3C on x86)
		var teamOffset uintptr = 0x74
		if !mr.is64 {
			teamOffset = 0x3C
		}
		if len(pOf.RoleTeamTypeOffset) > 1 {
			teamOffset = uintptr(pOf.RoleTeamTypeOffset[1])
		}
		if tt, err := mr.ReadInt32(rolePtr + teamOffset); err == nil && tt >= 0 && tt <= 10 {
			roleTeamType = tt
		}
	}

	// Route 2: Direct RoleType on NetworkedPlayerInfo (+0x50 on x64, +0x28 on x86)
	if roleType == 0 {
		var directRoleOff uintptr = 0x50
		if !mr.is64 {
			directRoleOff = 0x28
		}
		if rtDirect, err := mr.ReadInt32(targetPlayerInfoPtr + directRoleOff); err == nil && rtDirect >= 0 && rtDirect <= 30 {
			roleType = rtDirect
			if roleRawVal == 0 {
				roleRawVal = rtDirect
			}
		}
	}

	isImpostor := IsImpostorRole(roleType, roleTeamType)
	roleName := RoleTypeName(roleType)
	roleNameJa := RoleTypeJapanese(roleType)

	// 5. Color & Name from Outfits
	color := ColorUnknown
	name := ""

	var outfitPtr uintptr
	if len(pOf.OutfitsOffset) > 0 {
		outfitPtr, _ = mr.ReadPtrOffsets(targetPlayerInfoPtr, pOf.OutfitsOffset)
	}
	if outfitPtr == 0 {
		// Fallback single offset
		var outfitOffset uintptr = 0x58
		if !mr.is64 {
			outfitOffset = 0x1C
		}
		if len(pOf.OutfitsOffset) > 0 {
			outfitOffset = uintptr(pOf.OutfitsOffset[0])
		}
		outfitPtr, _ = mr.ReadPtr(targetPlayerInfoPtr + outfitOffset)
	}

	if outfitPtr > 0 {
		// ColorID
		var colorOffset uintptr = 0x10
		if !mr.is64 {
			colorOffset = 0xC
		}
		if oOf.ColorIDOffset > 0 {
			colorOffset = uintptr(oOf.ColorIDOffset)
		}
		colorVal, err := mr.ReadUInt32(outfitPtr + colorOffset)
		if err == nil && colorVal <= 17 {
			color = PlayerColor(colorVal)
		}

		// PlayerName
		var nameOffset uintptr = 0x40
		if !mr.is64 {
			nameOffset = 0x28
		}
		if oOf.PlayerNameOffset > 0 {
			nameOffset = uintptr(oOf.PlayerNameOffset)
		}
		namePtr, err := mr.ReadPtr(outfitPtr + nameOffset)
		if err == nil && namePtr > 0 {
			str, err := mr.ReadIl2cppString(namePtr)
			if err == nil && isValidPlayerName(str) {
				name = str
			}
		}
	}

	// Name fallback directly from targetPlayerInfoPtr if empty
	if name == "" {
		stringOffsets := []uintptr{0x40, 0x48, 0x18, 0x20, 0x28, 0x30, 0x38}
		for _, sOff := range stringOffsets {
			sPtr, err := mr.ReadPtr(targetPlayerInfoPtr + sOff)
			if err == nil && sPtr > 0 {
				str, err := mr.ReadIl2cppString(sPtr)
				if err == nil && isValidPlayerName(str) {
					name = str
					break
				}
			}
		}
	}

	if name == "" {
		name = fmt.Sprintf("Player#%d", pID)
	}

	if color == ColorUnknown {
		color = PlayerColor(pID % 18)
	}

	// 6. Raw offsets dump (0x10〜0x80)
	rawOffsets := make(map[string]byte)
	offsetList := []uintptr{
		0x10, 0x14, 0x18, 0x20, 0x28, 0x30, 0x34, 0x38, 0x3C,
		0x40, 0x48, 0x50, 0x54, 0x58, 0x60, 0x64, 0x68, 0x70,
		0x74, 0x78, 0x79, 0x80,
	}
	for _, off := range offsetList {
		if b, err := mr.ReadUInt8(targetPlayerInfoPtr + off); err == nil {
			rawOffsets[fmt.Sprintf("0x%02X", off)] = b
		}
	}

	log.Printf("[GameReader:PlayerInfo] #%d (%s, %s): IsDead=%v (IsDead_RawByte=0x%02X at +0x%X), Role=%s/%s (Role_RawInt=%d, TeamType=%d, IsImpostor=%v), State=%s, Addr=0x%X",
		pID, name, color.String(), isDead, isDeadRaw, deadOffset, roleName, roleNameJa, roleRawVal, roleTeamType, isImpostor, curState.String(), targetPlayerInfoPtr)

	return PlayerInfo{
		PlayerID:       pID,
		Name:           name,
		Color:          color,
		ColorName:      color.String(),
		ColorHex:       color.Hex(),
		IsDead:         isDead,
		Disconnected:   isDisconnected,
		RoleType:       roleType,
		RoleTeamType:   roleTeamType,
		RoleName:       roleName,
		RoleNameJa:     roleNameJa,
		IsImpostor:     isImpostor,
		StructAddress:  fmt.Sprintf("0x%X", targetPlayerInfoPtr),
		RawOffsets:     rawOffsets,
		IsDeadRawByte:  isDeadRaw,
		RoleRawInt:     roleRawVal,
		RoleTeamRawInt: roleTeamType,
	}, nil
}

// extractPlayerInfoHeuristic maintains compatibility with unit tests
func (gr *GameReader) extractPlayerInfoHeuristic(mr *MemoryReader, playerStructPtr uintptr, offsets *GameOffsets) (PlayerInfo, error) {
	pInfo, err := gr.extractPlayerInfoFromData(mr, playerStructPtr, offsets)
	if err != nil {
		return pInfo, err
	}
	if !pInfo.IsDead {
		pOf := offsets.PlayerInfoStructOffsets
		tasksOff := pOf.TasksOffset
		objOff := pOf.ObjectOffset
		if tasksOff > 0 && objOff >= tasksOff {
			start := tasksOff
			end := objOff
			if end-start > 32 {
				end = start + 32
			}
			for off := start; off <= end; off++ {
				if off == pOf.IsDeadOffset {
					continue
				}
				if b, err := mr.ReadUInt8(playerStructPtr + uintptr(off)); err == nil && b == 1 {
					pInfo.IsDead = true
					return pInfo, nil
				}
			}
		}

		for _, off := range []uintptr{0x50, 0x4C, 0x58, 0x30} {
			if off != uintptr(pOf.IsDeadOffset) {
				if b, err := mr.ReadUInt8(playerStructPtr + off); err == nil && b == 1 {
					pInfo.IsDead = true
					return pInfo, nil
				}
			}
		}
	}
	return pInfo, nil
}

func isValidPlayerName(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) == 0 || len(s) > 32 {
		return false
	}
	for _, r := range s {
		if r < 32 && r != '\t' {
			return false
		}
	}
	return true
}

func (gr *GameReader) pollGameOver(mr *MemoryReader, base uintptr, offsets *GameOffsets) {
	gr.mu.RLock()
	st := gr.currentState
	gr.mu.RUnlock()

	if st != StateGameOver {
		return
	}

	var reason GameOverReason = GameOverUnknown
	if len(offsets.RawGameOverReasonOffsets) > 0 {
		addr, err := mr.ReadPtrOffsetsWithFinalAddress(base, offsets.RawGameOverReasonOffsets)
		if err == nil && addr != 0 {
			val, err := mr.ReadInt32(addr)
			if err == nil {
				reason = GameOverReason(val)
			}
		}
	}

	players := gr.GetSnapshotPlayers()
	matchResult := ComputeMatchResult(reason, players)

	gr.mu.Lock()
	gr.lastMatchResult = &matchResult
	gr.mu.Unlock()

	gr.notifyGameOver(GameOverEvent{
		GameOverReason: reason,
		WinningPlayers: players,
		MatchResult:    matchResult,
	})
}

// GetLastMatchResult returns the result of the last finished match
func (gr *GameReader) GetLastMatchResult() *MatchResult {
	gr.mu.RLock()
	defer gr.mu.RUnlock()
	return gr.lastMatchResult
}

// AddManualPlayer manually adds a player for testing or custom lobbies
func (gr *GameReader) AddManualPlayer(name, colorStr string) PlayerInfo {
	gr.mu.Lock()
	nextID := byte(len(gr.currentPlayers))
	c := ColorUnknown
	for col, colName := range ColorNames {
		if strings.EqualFold(colName, colorStr) {
			c = col
			break
		}
	}
	if c == ColorUnknown {
		c = ColorRed
	}

	p := PlayerInfo{
		PlayerID:  nextID,
		Name:      name,
		Color:     c,
		ColorName: c.String(),
		ColorHex:  c.Hex(),
	}
	gr.currentPlayers[nextID] = p
	gr.mu.Unlock()

	gr.notifyPlayer(PlayerChangeEvent{
		Action: ActionJoined,
		Name:   name,
		Color:  c,
	})
	return p
}

// RemovePlayer manually removes a player
func (gr *GameReader) RemovePlayer(nameOrColor string) {
	gr.mu.Lock()
	var removed *PlayerInfo
	for id, p := range gr.currentPlayers {
		if strings.EqualFold(p.Name, nameOrColor) || strings.EqualFold(p.Color.String(), nameOrColor) {
			removed = &p
			delete(gr.currentPlayers, id)
			break
		}
	}
	gr.mu.Unlock()

	if removed != nil {
		gr.notifyPlayer(PlayerChangeEvent{
			Action: ActionLeft,
			Name:   removed.Name,
			Color:  removed.Color,
		})
	}
}

// GetSnapshot returns the current game state snapshot matching requirement format
func (gr *GameReader) GetSnapshot() GameSnapshot {
	gr.mu.RLock()
	defer gr.mu.RUnlock()

	players := make([]PlayerSnapshotItem, 0, len(gr.currentPlayers))
	for _, p := range gr.currentPlayers {
		players = append(players, p.ToSnapshotItem())
	}

	// Deterministic sorting by color ID
	sort.Slice(players, func(i, j int) bool {
		return players[i].Color < players[j].Color
	})

	return GameSnapshot{
		GameState:    int(gr.currentState),
		GameStateStr: gr.currentState.String(),
		LobbyCode:    gr.currentLobbyCode,
		PlayerCount:  len(players),
		Players:      players,
	}
}

// GetSnapshotJSON returns the JSON formatted string of the current snapshot
func (gr *GameReader) GetSnapshotJSON() ([]byte, error) {
	snap := gr.GetSnapshot()
	return json.MarshalIndent(snap, "", "  ")
}

// GetSnapshotPlayers returns current players as a slice
func (gr *GameReader) GetSnapshotPlayers() []PlayerInfo {
	gr.mu.RLock()
	defer gr.mu.RUnlock()
	res := make([]PlayerInfo, 0, len(gr.currentPlayers))
	for _, p := range gr.currentPlayers {
		res = append(res, p)
	}
	sort.Slice(res, func(i, j int) bool {
		return res[i].Color < res[j].Color
	})
	return res
}

// GetCurrentState returns the current GameState
func (gr *GameReader) GetCurrentState() GameState {
	gr.mu.RLock()
	defer gr.mu.RUnlock()
	return gr.currentState
}

// GetCurrentLobby returns the current lobby code
func (gr *GameReader) GetCurrentLobby() string {
	gr.mu.RLock()
	defer gr.mu.RUnlock()
	return gr.currentLobbyCode
}

// ForceTransmit triggers an immediate refresh
func (gr *GameReader) ForceTransmit() {
	go gr.tick()
}

func (gr *GameReader) notifyStateChange(oldS, newS GameState) {
	gr.mu.RLock()
	listeners := append([]GameListener{}, gr.listeners...)
	gr.mu.RUnlock()
	for _, l := range listeners {
		l.OnGameStateChanged(oldS, newS)
	}
}

func (gr *GameReader) notifyPlayer(e PlayerChangeEvent) {
	gr.mu.RLock()
	listeners := append([]GameListener{}, gr.listeners...)
	gr.mu.RUnlock()
	for _, l := range listeners {
		l.OnPlayerChanged(e)
	}
}

func (gr *GameReader) notifyLobby(e LobbyEvent) {
	gr.mu.RLock()
	listeners := append([]GameListener{}, gr.listeners...)
	gr.mu.RUnlock()
	for _, l := range listeners {
		l.OnLobbyUpdated(e)
	}
}

func (gr *GameReader) notifyGameOver(e GameOverEvent) {
	gr.mu.RLock()
	listeners := append([]GameListener{}, gr.listeners...)
	gr.mu.RUnlock()
	for _, l := range listeners {
		l.OnGameOver(e)
	}
}

func (gr *GameReader) notifyHookStatus(hooked bool, procName, hash string) {
	gr.mu.RLock()
	listeners := append([]GameListener{}, gr.listeners...)
	gr.mu.RUnlock()
	for _, l := range listeners {
		l.OnHookStatusChanged(hooked, procName, hash)
	}
}

var gameCodeCharSet = []byte("QWXRTYLPESDFGHUJKZOCVBINMA")

func isAsciiLetter(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

// DecodeGameCode converts an int32 game code to a room code string using official Among Us character mapping
func DecodeGameCode(val int32) string {
	if val == 0 {
		return ""
	}

	if val > 0 {
		b0 := byte(val & 0xFF)
		b1 := byte((val >> 8) & 0xFF)
		b2 := byte((val >> 16) & 0xFF)
		b3 := byte((val >> 24) & 0xFF)
		if isAsciiLetter(b0) && isAsciiLetter(b1) && isAsciiLetter(b2) && isAsciiLetter(b3) {
			return string([]byte{b0, b1, b2, b3})
		}
	}

	uVal := uint32(val)
	firstTwo := uVal & 0x3FF
	lastFour := (uVal >> 10) & 0xFFFFF

	if firstTwo/26 >= 26 {
		return ""
	}

	c1 := gameCodeCharSet[firstTwo%26]
	c2 := gameCodeCharSet[firstTwo/26]

	c3 := gameCodeCharSet[lastFour%26]
	lastFour /= 26
	c4 := gameCodeCharSet[lastFour%26]
	lastFour /= 26
	c5 := gameCodeCharSet[lastFour%26]
	lastFour /= 26
	if lastFour >= 26 {
		return ""
	}
	c6 := gameCodeCharSet[lastFour%26]

	return string([]byte{c1, c2, c3, c4, c5, c6})
}
