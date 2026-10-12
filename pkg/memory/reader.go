package memory

import (
	"context"
	"fmt"
	"log"
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

// GameReader polls and interprets the Among Us memory state
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
	currentPlayers       map[byte]PlayerInfo
	cachedPlayers        map[string]PlayerInfo
	cachedPlayerListAddr uintptr
	lastMatchResult      *MatchResult
	exileCausesEnd       bool

	pollInterval time.Duration
	stopChan     chan struct{}
	running      bool
}

// NewGameReader creates a new GameReader instance
func NewGameReader(om *OffsetManager) *GameReader {
	return &GameReader{
		offsetManager:  om,
		currentState:   StateUnknown,
		currentPlayers: make(map[byte]PlayerInfo),
		cachedPlayers:  make(map[string]PlayerInfo),
		pollInterval:   150 * time.Millisecond,
		stopChan:       make(chan struct{}),
	}
}

// AddListener registers a game event listener
func (gr *GameReader) AddListener(l GameListener) {
	gr.mu.Lock()
	defer gr.mu.Unlock()
	gr.listeners = append(gr.listeners, l)
}

// Start begins the memory reading polling loop with adaptive sleep interval
func (gr *GameReader) Start(ctx context.Context) {
	gr.mu.Lock()
	if gr.running {
		gr.mu.Unlock()
		return
	}
	gr.running = true
	gr.mu.Unlock()

	timer := time.NewTimer(gr.getAdaptiveInterval())
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			gr.Stop()
			return
		case <-gr.stopChan:
			return
		case <-timer.C:
			gr.tick()
			timer.Reset(gr.getAdaptiveInterval())
		}
	}
}

func (gr *GameReader) getAdaptiveInterval() time.Duration {
	if !gr.isHooked() {
		return 800 * time.Millisecond // Game not running -> sleep 800ms (0% CPU)
	}
	gr.mu.RLock()
	st := gr.currentState
	gr.mu.RUnlock()

	switch st {
	case StateMenu:
		return 350 * time.Millisecond // Menu screen -> sleep 350ms (0% CPU)
	case StateLobby, StateTasks, StateDiscussion, StateEnded:
		return 120 * time.Millisecond // In active match -> fast 120ms polling
	default:
		return 250 * time.Millisecond
	}
}

// Stop terminates the polling loop and releases handles
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
	if !gr.isHooked() {
		gr.tryHookProcess()
		return
	}

	// Verify process is still alive
	if err := gr.verifyProcess(); err != nil {
		gr.unhookProcess()
		return
	}

	// Poll game state
	gr.pollGameState()
}

func (gr *GameReader) isHooked() bool {
	gr.mu.RLock()
	defer gr.mu.RUnlock()
	return gr.memoryReader != nil && gr.currentProcess != nil
}

func (gr *GameReader) tryHookProcess() {
	pInfo, err := FindAmongUsProcess()
	if err != nil {
		return
	}

	mr := NewMemoryReader(pInfo.Handle, pInfo.PID, pInfo.Is64Bit)

	var resolvedOffsets *GameOffsets
	if pInfo.GameHash != "" {
		resolvedOffsets = gr.offsetManager.GetOffsetByHash(pInfo.GameHash)
	}

	var sigs ScannedPointers
	// If exact official offsets found by SHA-256 hash, use immediately (<1ms)
	// Only run heavy AOB scanner if hash is unknown (modded / unlisted version)
	if resolvedOffsets == nil {
		scanner := NewAOBScanner(mr, pInfo.GameAssemblyBase, pInfo.GameAssemblySize, pInfo.Is64Bit)
		sigs = scanner.ScanAllSignatures()

		defaultTpl := gr.offsetManager.GetDefaultOffsets(pInfo.Is64Bit)
		if defaultTpl != nil {
			scanned, err := scanner.ResolveOffsetsDynamically(defaultTpl)
			if err == nil {
				resolvedOffsets = scanned
			} else {
				resolvedOffsets = defaultTpl
			}
		}
	}

	if resolvedOffsets == nil {
		resolvedOffsets = gr.offsetManager.GetDefaultOffsets(pInfo.Is64Bit)
	}

	gr.mu.Lock()
	gr.currentProcess = pInfo
	gr.memoryReader = mr
	gr.offsets = resolvedOffsets
	gr.scannedSigs = sigs
	gr.currentState = StateUnknown
	gr.currentPlayers = make(map[byte]PlayerInfo)
	gr.mu.Unlock()

	log.Printf("[GameReader] Hooked Among Us (PID: %d, 64-bit: %v, Base: 0x%X)",
		pInfo.PID, pInfo.Is64Bit, pInfo.GameAssemblyBase)

	gr.notifyHookStatus(true, "Among Us.exe", pInfo.GameHash)
}

func (gr *GameReader) unhookProcess() {
	gr.mu.Lock()
	if gr.memoryReader != nil {
		gr.memoryReader.Close()
		gr.memoryReader = nil
	}
	gr.currentProcess = nil
	gr.currentState = StateUnknown
	gr.currentPlayers = make(map[byte]PlayerInfo)
	gr.mu.Unlock()

	gr.notifyHookStatus(false, "", "")
}

func (gr *GameReader) verifyProcess() error {
	gr.mu.RLock()
	mr := gr.memoryReader
	pInfo := gr.currentProcess
	gr.mu.RUnlock()

	if mr == nil || pInfo == nil {
		return fmt.Errorf("not hooked")
	}

	_, err := mr.ReadUInt8(pInfo.GameAssemblyBase)
	return err
}

func (gr *GameReader) pollGameState() {
	gr.mu.RLock()
	mr := gr.memoryReader
	pInfo := gr.currentProcess
	offsets := gr.offsets
	sigs := gr.scannedSigs
	oldState := gr.currentState
	gr.mu.RUnlock()

	if mr == nil || pInfo == nil || offsets == nil {
		return
	}

	base := pInfo.GameAssemblyBase

	// 1. Read Raw GameState directly from memory FIRST
	newState := gr.readRawGameState(mr, base, offsets, sigs)

	if newState != StateUnknown && newState != oldState {
		gr.mu.Lock()
		gr.currentState = newState
		gr.mu.Unlock()

		log.Printf("[GameReader] Game State changed: %s -> %s", oldState.String(), newState.String())
		gr.notifyStateChange(oldState, newState)

		if newState == StateEnded {
			gr.pollGameOver(mr, base, offsets)
		} else if newState == StateMenu {
			// Clear active match memory when returning to menu
			gr.mu.Lock()
			gr.currentPlayers = make(map[byte]PlayerInfo)
			gr.currentLobbyCode = ""
			gr.cachedPlayerListAddr = 0
			gr.mu.Unlock()
		} else if newState == StateLobby {
			// Reset dead flags when returning to lobby for next round
			gr.mu.Lock()
			for id, p := range gr.currentPlayers {
				if p.IsDead {
					p.IsDead = false
					gr.currentPlayers[id] = p
				}
			}
			gr.mu.Unlock()
		}
		oldState = newState
	}

	// 2. In Main Menu or Unknown state, skip lobby and player scanning completely (0% wasted CPU)
	if newState == StateMenu || newState == StateUnknown {
		return
	}

	// 3. Poll Lobby Info
	gr.pollLobby(mr, base, offsets, sigs)

	// 4. Poll Players (with Multi-Level Fallback)
	gr.pollPlayers(mr, base, offsets, sigs)
}

func (gr *GameReader) readRawGameState(mr *MemoryReader, base uintptr, offsets *GameOffsets, sigs ScannedPointers) GameState {
	detectedState := StateUnknown

	// 1. Primary Method: Official GameStateOffsets pointer chain (Highest Accuracy)
	// Pointer chain: base + offsets[0] -> ... -> final offset -> int32 GameState
	if len(offsets.GameStateOffsets) > 0 {
		addr, err := mr.ReadPtrOffsetsWithFinalAddress(base, offsets.GameStateOffsets)
		if err == nil && addr != 0 {
			val, err := mr.ReadInt32(addr)
			if err == nil && val >= 0 && val <= 4 {
				switch val {
				case 0:
					detectedState = StateMenu
				case 1:
					detectedState = StateLobby
				case 2:
					detectedState = StateTasks
				case 3:
					detectedState = StateDiscussion
				case 4:
					detectedState = StateEnded
				}
			}
		}
	}

	// 2. Secondary Method: AOB Scanned AmongUsClient class instance
	if detectedState == StateUnknown && sigs.AmongUsClientClassPtr > 0 {
		clientInstance := gr.resolveInstanceFromClass(mr, sigs.AmongUsClientClassPtr)
		if clientInstance != 0 {
			targetOffset := uintptr(0x88)
			if mr.is64 {
				targetOffset = uintptr(0xEC)
			}
			if len(offsets.GameStateOffsets) > 0 {
				targetOffset = uintptr(offsets.GameStateOffsets[len(offsets.GameStateOffsets)-1])
			}
			val, err := mr.ReadInt32(clientInstance + targetOffset)
			if err == nil && val >= 0 && val <= 4 {
				switch val {
				case 0:
					detectedState = StateMenu
				case 1:
					detectedState = StateLobby
				case 2:
					detectedState = StateTasks
				case 3:
					detectedState = StateDiscussion
				case 4:
					detectedState = StateEnded
				}
			}
		}
	}

	// 3. Meeting Detection: Only if in Tasks state, verify if MeetingHud is genuinely active
	// Never promote Lobby, Menu, or Ended to Discussion!
	if detectedState == StateTasks {
		if gr.isMeetingActive(mr, base, offsets, sigs) {
			detectedState = StateDiscussion
		}
	}

	return detectedState
}

func (gr *GameReader) isMeetingActive(mr *MemoryReader, base uintptr, offsets *GameOffsets, sigs ScannedPointers) bool {
	meetingHudAddr := uintptr(0)
	if len(offsets.MeetingHudPtr) > 0 {
		meetingHudAddr, _ = mr.ReadPtrOffsets(base, offsets.MeetingHudPtr)
	}
	if meetingHudAddr == 0 && sigs.MeetingHudClassPtr > 0 {
		meetingHudAddr = gr.resolveInstanceFromClass(mr, sigs.MeetingHudClassPtr)
	}

	if meetingHudAddr == 0 {
		return false
	}

	// Check MeetingHud state offset (0x80 for 32-bit, 0xD0 for 64-bit)
	if len(offsets.MeetingHudStateOffsets) > 0 {
		stateOffset := uintptr(offsets.MeetingHudStateOffsets[0])
		val, err := mr.ReadInt32(meetingHudAddr + stateOffset)
		if err == nil && val >= 0 && val <= 3 {
			return true
		}
	}

	return false
}

func (gr *GameReader) pollLobby(mr *MemoryReader, base uintptr, offsets *GameOffsets, sigs ScannedPointers) {
	var gameCodeStr string

	// Method 1: GameCodeOffsets (via GameStartManager)
	if len(offsets.GameCodeOffsets) > 0 {
		addr, err := mr.ReadPtrOffsetsWithFinalAddress(base, offsets.GameCodeOffsets)
		if err == nil && addr != 0 {
			val, err := mr.ReadInt32(addr)
			if err == nil && val != 0 {
				decoded := DecodeGameCode(val)
				if (len(decoded) == 6 || len(decoded) == 4) && isAllAlpha(decoded) {
					gameCodeStr = decoded
				}
			}
		}
	}

	// Method 2: AmongUsClient.GameId (Direct network client game ID)
	if gameCodeStr == "" {
		clientInstance := uintptr(0)
		if sigs.AmongUsClientClassPtr > 0 {
			clientInstance = gr.resolveInstanceFromClass(mr, sigs.AmongUsClientClassPtr)
		}
		if clientInstance == 0 && len(offsets.GameStateOffsets) > 1 {
			clientInstance, _ = mr.ReadPtrOffsets(base, offsets.GameStateOffsets[:len(offsets.GameStateOffsets)-1])
		}
		if clientInstance == 0 && offsets.AmongUsClientOffset > 0 {
			// Try direct instance dereference from base + offset
			staticFields, _ := mr.ReadPtr(base + uintptr(offsets.AmongUsClientOffset))
			if staticFields != 0 {
				clientInstance = gr.resolveInstanceFromClass(mr, staticFields)
				if clientInstance == 0 {
					clientInstance, _ = mr.ReadPtr(staticFields)
				}
			}
		}

		if clientInstance != 0 {
			searchOffsets := []uintptr{0x70, 0x68, 0x60, 0x58, 0x50, 0x48, 0x44, 0x40, 0x38, 0x34, 0x28, 0x24}
			for _, off := range searchOffsets {
				gameId, err := mr.ReadInt32(clientInstance + off)
				if err == nil && gameId != 0 {
					decoded := DecodeGameCode(gameId)
					if (len(decoded) == 6 || len(decoded) == 4) && isAllAlpha(decoded) {
						gameCodeStr = decoded
						break
					}
				}
			}
		}
	}

	// Read Region & Map
	var region, playMap int32
	if len(offsets.PlayRegionOffsets) > 0 {
		addr, err := mr.ReadPtrOffsetsWithFinalAddress(base, offsets.PlayRegionOffsets)
		if err == nil && addr != 0 {
			region, _ = mr.ReadInt32(addr)
		}
	}
	if len(offsets.PlayMapOffsets) > 0 {
		addr, err := mr.ReadPtrOffsetsWithFinalAddress(base, offsets.PlayMapOffsets)
		if err == nil && addr != 0 {
			playMap, _ = mr.ReadInt32(addr)
		}
	}

	gr.mu.Lock()
	if gameCodeStr != "" && (gameCodeStr != gr.currentLobbyCode || region != gr.currentRegion || playMap != gr.currentMap) {
		gr.currentLobbyCode = gameCodeStr
		gr.currentRegion = region
		gr.currentMap = playMap
		gr.mu.Unlock()

		gr.notifyLobby(LobbyEvent{
			LobbyCode: gameCodeStr,
			Region:    region,
			PlayMap:   playMap,
		})
	} else {
		gr.mu.Unlock()
	}
}

func isAllAlpha(s string) bool {
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// resolveInstanceFromClass tests common StaticFields offsets (+0xB8, +0xC0, +0xC8, +0xD0, +0xB0, +0x80, +0x5C)
func (gr *GameReader) resolveInstanceFromClass(mr *MemoryReader, classPtr uintptr) uintptr {
	if classPtr == 0 {
		return 0
	}
	staticOffsets := []uintptr{0xB8, 0xC0, 0xC8, 0xD0, 0xB0, 0x80, 0x5C}
	if !mr.is64 {
		staticOffsets = []uintptr{0x5C, 0x60, 0x64, 0x58, 0x80}
	}

	for _, sOff := range staticOffsets {
		staticFields, err := mr.ReadPtr(classPtr + sOff)
		if err == nil && staticFields != 0 {
			// Instance is at +0x0, +0x8, or +0x10 of StaticFields
			for _, iOff := range []uintptr{0x0, 0x8, 0x10} {
				inst, err := mr.ReadPtr(staticFields + iOff)
				if err == nil && inst != 0 {
					return inst
				}
			}
		}
	}
	return 0
}

func (gr *GameReader) pollPlayers(mr *MemoryReader, base uintptr, offsets *GameOffsets, sigs ScannedPointers) {
	currentList := make(map[byte]PlayerInfo)

	// Strategy 0: Fast cached address from previous successful discovery
	gr.mu.RLock()
	cachedAddr := gr.cachedPlayerListAddr
	gr.mu.RUnlock()

	if cachedAddr != 0 {
		players := gr.readPlayersFromGameData(mr, cachedAddr, offsets)
		if len(players) > 0 {
			for _, p := range players {
				currentList[p.PlayerID] = p
			}
		}
	}

	// Strategy 1: Official Pointer Chain (automuteus exact pointer chain)
	if len(currentList) == 0 && len(offsets.AllPlayerPtrOffsets) > 0 {
		players := gr.readPlayersOfficial(mr, base, offsets)
		if len(players) > 0 {
			for _, p := range players {
				currentList[p.PlayerID] = p
			}
		}
	}

	// Strategy 2: GameData.Instance -> allPlayers
	if len(currentList) == 0 {
		gameDataInstance := uintptr(0)
		if sigs.GameDataClassPtr > 0 {
			gameDataInstance = gr.resolveInstanceFromClass(mr, sigs.GameDataClassPtr)
		}
		if gameDataInstance == 0 && len(offsets.AllPlayerPtrOffsets) > 0 {
			gameDataInstance, _ = mr.ReadPtrOffsets(base, offsets.AllPlayerPtrOffsets[:len(offsets.AllPlayerPtrOffsets)-1])
		}

		if gameDataInstance != 0 {
			players := gr.readPlayersFromGameData(mr, gameDataInstance, offsets)
			if len(players) > 0 {
				gr.mu.Lock()
				gr.cachedPlayerListAddr = gameDataInstance
				gr.mu.Unlock()
				for _, p := range players {
					currentList[p.PlayerID] = p
				}
			}
		}
	}

	// Strategy 3: PlayerControl.AllPlayerControls -> Data (Backup Strategy)
	if len(currentList) == 0 {
		pcInstance := uintptr(0)
		if sigs.PlayerControlClassPtr > 0 {
			pcInstance = gr.resolveInstanceFromClass(mr, sigs.PlayerControlClassPtr)
		}
		if pcInstance != 0 {
			players := gr.readPlayersFromPlayerControl(mr, pcInstance, offsets)
			for _, p := range players {
				currentList[p.PlayerID] = p
			}
		}
	}

	if len(currentList) == 0 {
		return
	}

	gr.mu.RLock()
	curState := gr.currentState
	oldPlayers := gr.currentPlayers
	gr.mu.RUnlock()

	// 1. Sticky Death State: In active matches (Tasks/Discussion), dead players cannot revive
	if curState == StateTasks || curState == StateDiscussion {
		for id, p := range currentList {
			if oldP, ok := oldPlayers[id]; ok && oldP.IsDead {
				if !p.IsDead {
					p.IsDead = true
					currentList[id] = p
				}
			}
		}
	}

	// 2. Check Exiled Player from MeetingHud (if applicable)
	if (curState == StateTasks || curState == StateDiscussion) && len(offsets.ExiledPlayerIdOffsets) > 0 {
		exiledAddr, err := mr.ReadPtrOffsetsWithFinalAddress(base, offsets.ExiledPlayerIdOffsets)
		if err == nil && exiledAddr != 0 {
			exiledId, err := mr.ReadUInt8(exiledAddr)
			if err == nil && exiledId <= 15 {
				if p, ok := currentList[exiledId]; ok && !p.IsDead {
					p.IsDead = true
					currentList[exiledId] = p
					log.Printf("[GameReader] Exiled player %d (%s) marked as DEAD", exiledId, p.Name)
				}
			}
		}
	}

	gr.mu.Lock()
	gr.currentPlayers = currentList
	gr.mu.Unlock()

	// Notify player changes
	for id, p := range currentList {
		oldP, exists := oldPlayers[id]
		if !exists {
			gr.notifyPlayer(PlayerChangeEvent{
				Action:       ActionJoined,
				Name:         p.Name,
				IsDead:       p.IsDead,
				Disconnected: p.Disconnected,
				Color:        p.Color,
			})
		} else {
			if !oldP.IsDead && p.IsDead {
				gr.notifyPlayer(PlayerChangeEvent{
					Action:       ActionDied,
					Name:         p.Name,
					IsDead:       p.IsDead,
					Disconnected: p.Disconnected,
					Color:        p.Color,
				})
			}
			if !oldP.Disconnected && p.Disconnected {
				gr.notifyPlayer(PlayerChangeEvent{
					Action:       ActionDisconnected,
					Name:         p.Name,
					IsDead:       p.IsDead,
					Disconnected: p.Disconnected,
					Color:        p.Color,
				})
			}
			if oldP.Color != p.Color {
				gr.notifyPlayer(PlayerChangeEvent{
					Action:       ActionChangedColor,
					Name:         p.Name,
					IsDead:       p.IsDead,
					Disconnected: p.Disconnected,
					Color:        p.Color,
				})
			}
		}
	}

	for id, oldP := range oldPlayers {
		if _, exists := currentList[id]; !exists {
			gr.notifyPlayer(PlayerChangeEvent{
				Action:       ActionLeft,
				Name:         oldP.Name,
				IsDead:       oldP.IsDead,
				Disconnected: oldP.Disconnected,
				Color:        oldP.Color,
			})
		}
	}
}

// readPlayersOfficial implements standard automuteus pointer chain reading
func (gr *GameReader) readPlayersOfficial(mr *MemoryReader, base uintptr, offsets *GameOffsets) []PlayerInfo {
	var result []PlayerInfo

	// 1. Resolve pointer to AllPlayers list
	allPlayersPtr, err := mr.ReadPtrOffsets(base, offsets.AllPlayerPtrOffsets)
	if err != nil || allPlayersPtr == 0 {
		return nil
	}

	// 2. Resolve items array
	var itemsArray uintptr
	if len(offsets.AllPlayersOffsets) > 0 {
		itemsArray, _ = mr.ReadPtrOffsets(allPlayersPtr, offsets.AllPlayersOffsets)
	}
	if itemsArray == 0 {
		if mr.is64 {
			itemsArray, _ = mr.ReadPtr(allPlayersPtr + 0x10)
		} else {
			itemsArray, _ = mr.ReadPtr(allPlayersPtr + 0x8)
		}
	}

	// 3. Resolve count
	playerCount := int32(0)
	if len(offsets.PlayerCountOffsets) > 0 {
		playerCount, _ = mr.ReadInt32(allPlayersPtr + uintptr(offsets.PlayerCountOffsets[0]))
	}
	if playerCount <= 0 || playerCount > 15 {
		if mr.is64 {
			playerCount, _ = mr.ReadInt32(allPlayersPtr + 0x18)
		} else {
			playerCount, _ = mr.ReadInt32(allPlayersPtr + 0xC)
		}
	}

	if playerCount <= 0 || playerCount > 15 || itemsArray == 0 {
		return nil
	}

	// 4. Iterate over items array
	addPlayerStep := uintptr(offsets.AddPlayerPtr)
	if addPlayerStep == 0 {
		if mr.is64 {
			addPlayerStep = 8
		} else {
			addPlayerStep = 4
		}
	}

	playerListHeader := uintptr(offsets.PlayerListPtr)
	if playerListHeader == 0 {
		if mr.is64 {
			playerListHeader = 0x20
		} else {
			playerListHeader = 0x10
		}
	}

	for i := 0; i < int(playerCount); i++ {
		elemAddr := itemsArray + playerListHeader + uintptr(i)*addPlayerStep
		playerStructPtr, err := mr.ReadPtr(elemAddr)
		if err != nil || playerStructPtr == 0 {
			continue
		}

		pInfo, err := gr.extractPlayerInfoHeuristic(mr, playerStructPtr, offsets)
		if err == nil {
			result = append(result, pInfo)
		}
	}

	return result
}

// readPlayersFromGameData handles both List<PlayerInfo> and Dictionary<byte, PlayerInfo>
func (gr *GameReader) readPlayersFromGameData(mr *MemoryReader, gameDataPtr uintptr, offsets *GameOffsets) []PlayerInfo {
	var result []PlayerInfo
	step := uintptr(8)
	if !mr.is64 {
		step = 4
	}

	// Scan fields 0x10 to 0x80 of GameData to find List or Dictionary of players
	for off := uintptr(0x10); off <= 0x80; off += step {
		fieldPtr, err := mr.ReadPtr(gameDataPtr + off)
		if err != nil || fieldPtr == 0 {
			continue
		}

		// Try parsing as C# List<PlayerInfo>
		// List: +0x10 (_items array), +0x18 (_size)
		sizeOffset := uintptr(0x18)
		itemsOffset := uintptr(0x10)
		if !mr.is64 {
			sizeOffset = 0xC
			itemsOffset = 0x8
		}

		sizeVal, err := mr.ReadInt32(fieldPtr + sizeOffset)
		if err == nil && sizeVal > 0 && sizeVal <= 15 {
			itemsArray, err := mr.ReadPtr(fieldPtr + itemsOffset)
			if err == nil && itemsArray != 0 {
				players := gr.readPlayerArray(mr, itemsArray, int(sizeVal), offsets)
				if len(players) > 0 {
					return players
				}
			}
		}

		// Try parsing as C# Dictionary<byte, PlayerInfo>
		// Dict: +0x18 (_entries array), +0x20 (_count)
		dictEntries, err := mr.ReadPtr(fieldPtr + 0x18)
		dictCount, err2 := mr.ReadInt32(fieldPtr + 0x20)
		if err == nil && err2 == nil && dictCount > 0 && dictCount <= 15 && dictEntries != 0 {
			players := gr.readPlayerDictionaryEntries(mr, dictEntries, int(dictCount), offsets)
			if len(players) > 0 {
				return players
			}
		}
	}

	return result
}

// readPlayersFromPlayerControl scans PlayerControl.AllPlayerControls
func (gr *GameReader) readPlayersFromPlayerControl(mr *MemoryReader, pcClassInstance uintptr, offsets *GameOffsets) []PlayerInfo {
	var result []PlayerInfo
	step := uintptr(8)
	if !mr.is64 {
		step = 4
	}

	// Scan fields for List<PlayerControl>
	for off := uintptr(0x10); off <= 0x60; off += step {
		listPtr, err := mr.ReadPtr(pcClassInstance + off)
		if err != nil || listPtr == 0 {
			continue
		}

		sizeVal, _ := mr.ReadInt32(listPtr + 0x18)
		if sizeVal > 0 && sizeVal <= 15 {
			itemsArray, _ := mr.ReadPtr(listPtr + 0x10)
			if itemsArray != 0 {
				for i := 0; i < int(sizeVal); i++ {
					pcObj, _ := mr.ReadPtr(itemsArray + 0x20 + uintptr(i)*step)
					if pcObj == 0 {
						continue
					}
					// PlayerControl.Data is commonly at +0x30, +0x38, +0x40, +0x48, +0x50
					for _, dataOff := range []uintptr{0x30, 0x38, 0x40, 0x48, 0x50, 0x28} {
						dataPtr, _ := mr.ReadPtr(pcObj + dataOff)
						if dataPtr != 0 {
							pInfo, err := gr.extractPlayerInfoHeuristic(mr, dataPtr, offsets)
							if err == nil && pInfo.Name != "" {
								result = append(result, pInfo)
								break
							}
						}
					}
				}
				if len(result) > 0 {
					return result
				}
			}
		}
	}

	return result
}

func (gr *GameReader) readPlayerArray(mr *MemoryReader, arrayPtr uintptr, count int, offsets *GameOffsets) []PlayerInfo {
	var res []PlayerInfo
	ptrSize := uintptr(8)
	arrayHeader := uintptr(0x20)
	if !mr.is64 {
		ptrSize = 4
		arrayHeader = 0x10
	}

	for i := 0; i < count; i++ {
		elemAddr := arrayPtr + arrayHeader + uintptr(i)*ptrSize
		playerStructPtr, err := mr.ReadPtr(elemAddr)
		if err != nil || playerStructPtr == 0 {
			continue
		}

		pInfo, err := gr.extractPlayerInfoHeuristic(mr, playerStructPtr, offsets)
		if err == nil && pInfo.Name != "" {
			res = append(res, pInfo)
		}
	}
	return res
}

func (gr *GameReader) readPlayerDictionaryEntries(mr *MemoryReader, entriesArrayPtr uintptr, count int, offsets *GameOffsets) []PlayerInfo {
	var res []PlayerInfo
	entrySize := uintptr(32) // Default struct Entry size in x64 (hashCode:4, next:4, key:8, value:16)
	if !mr.is64 {
		entrySize = 16
	}

	for i := 0; i < count; i++ {
		entryAddr := entriesArrayPtr + 0x20 + uintptr(i)*entrySize
		// Value pointer is at +0x10 (x64) or +0x8 (x86)
		valPtr, err := mr.ReadPtr(entryAddr + 0x10)
		if err != nil || valPtr == 0 {
			valPtr, _ = mr.ReadPtr(entryAddr + 0x8)
		}

		if valPtr != 0 {
			pInfo, err := gr.extractPlayerInfoHeuristic(mr, valPtr, offsets)
			if err == nil && pInfo.Name != "" {
				res = append(res, pInfo)
			}
		}
	}
	return res
}

// extractPlayerInfoHeuristic extracts PlayerInfo safely with heuristic field detection
func (gr *GameReader) extractPlayerInfoHeuristic(mr *MemoryReader, playerStructPtr uintptr, offsets *GameOffsets) (PlayerInfo, error) {
	if playerStructPtr == 0 {
		return PlayerInfo{}, fmt.Errorf("nil pointer")
	}

	var name string
	var color PlayerColor = ColorUnknown
	var playerID byte
	var isDead, disconnected, isImpostor bool
	var roleType int32
	var hatID, petID, skinID, visorID, namePlateID uint32

	// 1. Try defined struct offsets first
	pOffsets := offsets.PlayerInfoStructOffsets
	outfitOffsets := offsets.PlayerOutfitStructOffsets

	if pOffsets.PlayerIDOffset > 0 {
		idByte, err := mr.ReadUInt8(playerStructPtr + uintptr(pOffsets.PlayerIDOffset))
		if err == nil {
			playerID = idByte
		}
	}

	// Multi-tier IsDead detection
	// Tier 1: Configured IsDeadOffset
	if pOffsets.IsDeadOffset > 0 {
		deadByte, err := mr.ReadUInt8(playerStructPtr + uintptr(pOffsets.IsDeadOffset))
		if err == nil && deadByte != 0 {
			isDead = true
		}
	}

	// Tier 2: Structural Window Scan between Tasks and Object
	// In IL2CPP C#, `bool IsDead` is structurally placed between `List<TaskInfo> Tasks` (pointer)
	// and `PlayerControl _object` (pointer).
	if !isDead {
		tasksOff := pOffsets.TasksOffset
		objectOff := pOffsets.ObjectOffset
		ptrSize := 8
		if !mr.is64 {
			ptrSize = 4
		}

		if tasksOff > 0 && objectOff > tasksOff {
			windowStart := tasksOff + ptrSize
			for off := windowStart; off < objectOff; off++ {
				val, err := mr.ReadUInt8(playerStructPtr + uintptr(off))
				if err == nil && val == 1 {
					isDead = true
					break
				}
			}
		} else if objectOff > 0 {
			scanStart := objectOff - ptrSize
			if scanStart >= 0x20 {
				for off := scanStart; off < objectOff; off++ {
					val, err := mr.ReadUInt8(playerStructPtr + uintptr(off))
					if err == nil && val == 1 {
						isDead = true
						break
					}
				}
			}
		}
	}

	// Tier 3: Known historical version offsets for IsDead
	if !isDead {
		knownDeadOffsets := []int{0x78, 0x70, 0x50, 0x54, 0x30, 0x32, 0x29, 0x28, 0x79, 0x71, 0x51}
		for _, off := range knownDeadOffsets {
			if off == pOffsets.PlayerIDOffset ||
				off == pOffsets.DisconnectedOffset ||
				off == pOffsets.PlayerLevelOffset ||
				off == pOffsets.TasksOffset ||
				off == pOffsets.ObjectOffset {
				continue
			}
			val, err := mr.ReadUInt8(playerStructPtr + uintptr(off))
			if err == nil && val == 1 {
				isDead = true
				break
			}
		}
	}

	// Multi-tier Disconnected detection
	if pOffsets.DisconnectedOffset > 0 {
		discByte, err := mr.ReadUInt8(playerStructPtr + uintptr(pOffsets.DisconnectedOffset))
		if err == nil && discByte != 0 {
			disconnected = true
		}
	}
	if !disconnected {
		knownDiscOffsets := []int{0x64, 0x5C, 0x3C, 0x24, 0x44, 0x48}
		for _, off := range knownDiscOffsets {
			if off == pOffsets.PlayerIDOffset ||
				off == pOffsets.IsDeadOffset ||
				off == pOffsets.PlayerLevelOffset ||
				off == pOffsets.TasksOffset ||
				off == pOffsets.ObjectOffset {
				continue
			}
			discByte, err := mr.ReadUInt8(playerStructPtr + uintptr(off))
			if err == nil && discByte == 1 {
				disconnected = true
				break
			}
		}
	}

	// Outfits pointer
	if len(pOffsets.OutfitsOffset) > 0 {
		outfitPtr, err := mr.ReadPtrOffsets(playerStructPtr, pOffsets.OutfitsOffset)
		if err == nil && outfitPtr != 0 {
			colorVal, _ := mr.ReadInt32(outfitPtr + uintptr(outfitOffsets.ColorIDOffset))
			if colorVal >= 0 && colorVal <= 17 {
				color = PlayerColor(colorVal)
			}
			hatID, _ = mr.ReadUInt32(outfitPtr + uintptr(outfitOffsets.HatIDOffset))
			petID, _ = mr.ReadUInt32(outfitPtr + uintptr(outfitOffsets.PetIDOffset))
			skinID, _ = mr.ReadUInt32(outfitPtr + uintptr(outfitOffsets.SkinIDOffset))
			visorID, _ = mr.ReadUInt32(outfitPtr + uintptr(outfitOffsets.VisorIDOffset))
			namePlateID, _ = mr.ReadUInt32(outfitPtr + uintptr(outfitOffsets.NamePlateIDOffset))

			// Check multiple name offsets in PlayerOutfit (e.g. PlayerName, PreCensor, PostCensor)
			for _, nOff := range []int{
				outfitOffsets.PlayerNameOffset,
				0x28, 0x2C, 0x20, 0x24, 0x48, 0x50,
			} {
				if nOff > 0 {
					namePtr, _ := mr.ReadPtr(outfitPtr + uintptr(nOff))
					if namePtr != 0 {
						str, err := mr.ReadIl2cppString(namePtr)
						if err == nil && len(str) >= 1 && isValidPlayerName(str) {
							name = str
							break
						}
					}
				}
			}
		}
	}

	// 2. Comprehensive Scan for Name and Color
	step := uintptr(8)
	if !mr.is64 {
		step = 4
	}

	if name == "" {
		for off := uintptr(0x8); off <= 0xA0; off += step {
			candidatePtr, err := mr.ReadPtr(playerStructPtr + off)
			if err != nil || candidatePtr == 0 {
				continue
			}

			// Direct IL2CPP string
			str, err := mr.ReadIl2cppString(candidatePtr)
			if err == nil && len(str) >= 1 && len(str) <= 32 && isValidPlayerName(str) {
				name = str
				break
			}

			// Sub-struct IL2CPP string
			for subOff := uintptr(0x8); subOff <= 0x50; subOff += step {
				subPtr, err := mr.ReadPtr(candidatePtr + subOff)
				if err == nil && subPtr != 0 {
					subStr, err := mr.ReadIl2cppString(subPtr)
					if err == nil && len(subStr) >= 1 && len(subStr) <= 32 && isValidPlayerName(subStr) {
						name = subStr
						break
					}
				}
			}
			if name != "" {
				break
			}
		}
	}

	// Fallback for Color if unknown
	if color == ColorUnknown {
		for off := uintptr(0x8); off <= 0x60; off += 4 {
			val, err := mr.ReadInt32(playerStructPtr + off)
			if err == nil && val >= 0 && val <= 17 {
				color = PlayerColor(val)
				break
			}
		}
	}

	// Role Type exploration
	for _, rOff := range []uintptr{0x68, 0x70, 0x48, 0x50, 0x38, 0x28} {
		rolePtr, err := mr.ReadPtr(playerStructPtr + rOff)
		if err == nil && rolePtr != 0 {
			rType, err := mr.ReadInt32(rolePtr + 0x20)
			if err == nil && rType >= 0 && rType <= 10 {
				roleType = rType
				if rType == VanillaImpostor || rType == VanillaShapeshifter || rType == VanillaPhantom {
					isImpostor = true
				}
				break
			}
		}
	}

	// Fallback name if string couldn't be resolved but player struct exists
	if name == "" {
		if color != ColorUnknown {
			name = fmt.Sprintf("Player (%s)", color.String())
		} else {
			name = fmt.Sprintf("Player %d", playerID)
		}
	}

	return PlayerInfo{
		PlayerID:     playerID,
		Name:         name,
		Color:        color,
		HatID:        hatID,
		PetID:        petID,
		SkinID:       skinID,
		VisorID:      visorID,
		NamePlateID:  namePlateID,
		IsDead:       isDead,
		Disconnected: disconnected,
		RoleType:     roleType,
		IsImpostor:   isImpostor,
		ColorHex:     color.Hex(),
	}, nil
}

func isValidPlayerName(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) == 0 || len([]rune(s)) > 32 {
		return false
	}

	validChars := 0
	totalChars := 0

	for _, r := range s {
		totalChars++
		// Reject control characters or replacement char
		if r < 32 || r == 127 || r == 0xFFFD {
			return false
		}
		// Reject Private Use Area (often garbage pointers decoded as UTF-16)
		if (r >= 0xE000 && r <= 0xF8FF) || (r >= 0xF0000 && r <= 0xFFFFD) {
			return false
		}

		// Count normal recognizable letters/symbols
		// Basic Latin, Latin-1, Hiragana, Katakana, CJK Unified Ideographs, Hangul, Cyrillic
		if (r >= 32 && r <= 126) ||
			(r >= 0x3040 && r <= 0x309F) || // Hiragana
			(r >= 0x30A0 && r <= 0x30FF) || // Katakana
			(r >= 0x4E00 && r <= 0x9FFF) || // CJK Kanji
			(r >= 0xAC00 && r <= 0xD7AF) || // Hangul
			(r >= 0x0400 && r <= 0x04FF) || // Cyrillic
			(r >= 0x00C0 && r <= 0x024F) { // Latin Extended
			validChars++
		}
	}

	// At least 80% of characters must be standard valid readable characters
	return totalChars > 0 && (float64(validChars)/float64(totalChars) >= 0.8)
}

func (gr *GameReader) pollGameOver(mr *MemoryReader, base uintptr, offsets *GameOffsets) {
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

// GetSnapshotPlayers returns a copy of current active players
func (gr *GameReader) GetSnapshotPlayers() []PlayerInfo {
	gr.mu.RLock()
	defer gr.mu.RUnlock()
	res := make([]PlayerInfo, 0, len(gr.currentPlayers))
	for _, p := range gr.currentPlayers {
		res = append(res, p)
	}
	return res
}

// AddManualPlayer manually registers a player to the active player list
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
		PlayerID: nextID,
		Name:     name,
		Color:    c,
		ColorHex: c.Hex(),
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

// RemovePlayer removes a player from the active list
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

// GetCurrentState returns the current game state
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

// ForceTransmit triggers state, lobby, and player notifications
func (gr *GameReader) ForceTransmit() {
	gr.mu.RLock()
	state := gr.currentState
	lobby := gr.currentLobbyCode
	region := gr.currentRegion
	playMap := gr.currentMap
	players := gr.GetSnapshotPlayers()
	lastMatch := gr.lastMatchResult
	gr.mu.RUnlock()

	gr.notifyStateChange(StateUnknown, state)
	if lobby != "" {
		gr.notifyLobby(LobbyEvent{
			LobbyCode: lobby,
			Region:    region,
			PlayMap:   playMap,
		})
	}
	for _, p := range players {
		gr.notifyPlayer(PlayerChangeEvent{
			Action:       ActionForceUpdated,
			Name:         p.Name,
			IsDead:       p.IsDead,
			Disconnected: p.Disconnected,
			Color:        p.Color,
		})
	}
	if lastMatch != nil {
		gr.notifyGameOver(GameOverEvent{
			GameOverReason: lastMatch.GameOverReason,
			WinningPlayers: players,
			MatchResult:    *lastMatch,
		})
	}
}

func (gr *GameReader) notifyStateChange(oldS, newS GameState) {
	gr.mu.RLock()
	listeners := append([]GameListener(nil), gr.listeners...)
	gr.mu.RUnlock()
	for _, l := range listeners {
		l.OnGameStateChanged(oldS, newS)
	}
}

func (gr *GameReader) notifyPlayer(e PlayerChangeEvent) {
	gr.mu.RLock()
	listeners := append([]GameListener(nil), gr.listeners...)
	gr.mu.RUnlock()
	for _, l := range listeners {
		l.OnPlayerChanged(e)
	}
}

func (gr *GameReader) notifyLobby(e LobbyEvent) {
	gr.mu.RLock()
	listeners := append([]GameListener(nil), gr.listeners...)
	gr.mu.RUnlock()
	for _, l := range listeners {
		l.OnLobbyUpdated(e)
	}
}

func (gr *GameReader) notifyGameOver(e GameOverEvent) {
	gr.mu.RLock()
	listeners := append([]GameListener(nil), gr.listeners...)
	gr.mu.RUnlock()
	for _, l := range listeners {
		l.OnGameOver(e)
	}
}

func (gr *GameReader) notifyHookStatus(hooked bool, procName, hash string) {
	gr.mu.RLock()
	listeners := append([]GameListener(nil), gr.listeners...)
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

	// Version 1: 4-letter ASCII room code (val is a positive 32-bit int with 4 ASCII characters)
	if val > 0 {
		b0 := byte(val & 0xFF)
		b1 := byte((val >> 8) & 0xFF)
		b2 := byte((val >> 16) & 0xFF)
		b3 := byte((val >> 24) & 0xFF)
		if isAsciiLetter(b0) && isAsciiLetter(b1) && isAsciiLetter(b2) && isAsciiLetter(b3) {
			return string([]byte{b0, b1, b2, b3})
		}
	}

	// Version 2: 6-letter room code (val is packed int32, typically negative in signed int32)
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
