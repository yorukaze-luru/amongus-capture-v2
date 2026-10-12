package memory

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"sort"
)

//go:embed offsets.json
var embeddedOffsetsJSON []byte

// GameOffsets represents the memory offsets needed to read Among Us game state
type GameOffsets struct {
	Description                string                         `json:"Description"`
	AmongUsClientOffset        int                            `json:"AmongUsClientOffset"`
	GameDataOffset             int                            `json:"GameDataOffset"`
	MeetingHudOffset           int                            `json:"MeetingHudOffset"`
	PlayerControlOffset        int                            `json:"PlayerControlOffset,omitempty"`
	GameStartManagerOffset     int                            `json:"GameStartManagerOffset"`
	HudManagerOffset           int                            `json:"HudManagerOffset"`
	ServerManagerOffset        int                            `json:"ServerManagerOffset"`
	TempDataOffset             int                            `json:"TempDataOffset"`
	GameOptionsOffset          int                            `json:"GameOptionsOffset"`
	MeetingHudPtr              []int                          `json:"MeetingHudPtr"`
	MeetingHudCachePtrOffsets  []int                          `json:"MeetingHudCachePtrOffsets"`
	MeetingHudStateOffsets     []int                          `json:"MeetingHudStateOffsets"`
	GameStateOffsets           []int                          `json:"GameStateOffsets"`
	AllPlayerPtrOffsets        []int                          `json:"AllPlayerPtrOffsets"`
	AllPlayersOffsets          []int                          `json:"AllPlayersOffsets"`
	PlayerCountOffsets         []int                          `json:"PlayerCountOffsets"`
	ExiledPlayerIdOffsets      []int                          `json:"ExiledPlayerIdOffsets"`
	RawGameOverReasonOffsets   []int                          `json:"RawGameOverReasonOffsets"`
	WinningPlayersPtrOffsets   []int                          `json:"WinningPlayersPtrOffsets"`
	WinningPlayersOffsets      []int                          `json:"WinningPlayersOffsets"`
	WinningPlayerCountOffsets  []int                          `json:"WinningPlayerCountOffsets"`
	GameCodeOffsets            []int                          `json:"GameCodeOffsets"`
	PlayRegionOffsets          []int                          `json:"PlayRegionOffsets"`
	PlayMapOffsets             []int                          `json:"PlayMapOffsets"`
	StringOffsets              []int                          `json:"StringOffsets"`
	IsEpic                     bool                           `json:"isEpic"`
	AddPlayerPtr               int                            `json:"AddPlayerPtr"`
	PlayerListPtr              int                            `json:"PlayerListPtr"`
	PlayerInfoStructOffsets    PlayerInfoStructOffsets        `json:"PlayerInfoStructOffsets"`
	WinningPlayerDataOffsets   WinningPlayerDataStructOffsets `json:"WinningPlayerDataStructOffsets"`
	PlayerOutfitStructOffsets  PlayerOutfitStructOffsets      `json:"PlayerOutfitStructOffsets"`
}

type PlayerInfoStructOffsets struct {
	PlayerIDOffset     int   `json:"PlayerIDOffset"`
	OutfitsOffset      []int `json:"OutfitsOffset"`
	PlayerLevelOffset  int   `json:"PlayerLevelOffset"`
	DisconnectedOffset int   `json:"DisconnectedOffset"`
	RoleTypeOffset     []int `json:"RoleTypeOffset"`
	RoleTeamTypeOffset []int `json:"RoleTeamTypeOffset"`
	TasksOffset        int   `json:"TasksOffset"`
	IsDeadOffset       int   `json:"IsDeadOffset"`
	ObjectOffset       int   `json:"ObjectOffset"`
}

type WinningPlayerDataStructOffsets struct {
	PlayerNameOffset int `json:"PlayerNameOffset"`
	OutfitOffset     int `json:"OutfitOffset"`
	IsYouOffset      int `json:"IsYouOffset"`
	IsImposterOffset int `json:"IsImposterOffset"`
	IsDeadOffset     int `json:"IsDeadOffset"`
}

type PlayerOutfitStructOffsets struct {
	ColorIDOffset     int `json:"ColorIDOffset"`
	HatIDOffset       int `json:"HatIDOffset"`
	PetIDOffset       int `json:"PetIDOffset"`
	SkinIDOffset      int `json:"SkinIDOffset"`
	VisorIDOffset     int `json:"VisorIDOffset"`
	NamePlateIDOffset int `json:"NamePlateIDOffset"`
	PlayerNameOffset  int `json:"PlayerNameOffset"`
}

// OffsetManager manages fetching, caching, and matching offsets
type OffsetManager struct {
	mu           sync.RWMutex
	remoteURL    string
	cachePath    string
	localPath    string
	offsets      map[string]*GameOffsets
	localOffsets map[string]*GameOffsets
}

const DefaultOffsetsURL = "https://raw.githubusercontent.com/automuteus/amonguscapture/master/Offsets.json"
const BackupOffsetsURL = "https://raw.githubusercontent.com/denverquane/amonguscapture/master/Offsets.json"

// NewOffsetManager initializes the offset manager
func NewOffsetManager(customURL string) *OffsetManager {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		appData = "."
	}
	cacheDir := filepath.Join(appData, "AmongUsCaptureV2")
	_ = os.MkdirAll(cacheDir, 0755)

	url := customURL
	if url == "" {
		url = DefaultOffsetsURL
	}

	om := &OffsetManager{
		remoteURL:    url,
		cachePath:    filepath.Join(cacheDir, "offsets_cache.json"),
		localPath:    filepath.Join(cacheDir, "offsets_custom.json"),
		offsets:      make(map[string]*GameOffsets),
		localOffsets: make(map[string]*GameOffsets),
	}

	om.loadEmbeddedDefaults()
	om.loadLocalCache()
	om.loadCustomOffsets()

	// Asynchronously refresh from remote
	go om.RefreshRemote()

	return om
}

// parseHexJSON parses JSON that contains 0x... hex numbers
func parseHexJSON(data []byte, target interface{}) error {
	// Replace hex numbers (e.g. 0x22F4F10, 0x5C) with decimal strings
	re := regexp.MustCompile(`0x([0-9a-fA-F]+)`)
	clean := re.ReplaceAllFunc(data, func(match []byte) []byte {
		hexStr := string(match[2:])
		val, err := strconv.ParseInt(hexStr, 16, 64)
		if err != nil {
			return match
		}
		return []byte(strconv.FormatInt(val, 10))
	})
	return json.Unmarshal(clean, target)
}

// loadEmbeddedDefaults loads built-in standard offsets
func (om *OffsetManager) loadEmbeddedDefaults() {
	if len(embeddedOffsetsJSON) > 0 {
		var allOffsets map[string]*GameOffsets
		if err := parseHexJSON(embeddedOffsetsJSON, &allOffsets); err == nil {
			om.mu.Lock()
			for k, v := range allOffsets {
				om.offsets[strings.ToUpper(strings.TrimSpace(k))] = v
			}
			om.mu.Unlock()
			log.Printf("[OffsetManager] Successfully loaded %d built-in official game version offsets!", len(allOffsets))
		} else {
			log.Printf("[OffsetManager] Failed to parse embedded offsets JSON: %v", err)
		}
	}

	// 1. Steam / Epic 32-bit fallback
	defaultX86 := &GameOffsets{
		Description:               "v2024.6.4s (build num: 4234) / v2024.3.5s x86 IL2CPP",
		AmongUsClientOffset:       0x22F5048,
		GameDataOffset:            0x22D51A4,
		MeetingHudOffset:          0x22F68C8,
		GameStartManagerOffset:    0x22C3484,
		HudManagerOffset:          0x22C36B4,
		ServerManagerOffset:       0x2264684,
		TempDataOffset:            0x22E0418,
		GameOptionsOffset:         0x22D56EC,
		MeetingHudPtr:             []int{0x22F68C8, 0x5C, 0x0},
		MeetingHudCachePtrOffsets: []int{0x8},
		MeetingHudStateOffsets:    []int{0x80},
		GameStateOffsets:          []int{0x22F5048, 0x5C, 0x0, 0x88},
		AllPlayerPtrOffsets:       []int{0x22D51A4, 0x5C, 0x0, 0x24},
		AllPlayersOffsets:         []int{0x8},
		PlayerCountOffsets:        []int{0xC},
		ExiledPlayerIdOffsets:     []int{0x22F68C8, 0x5C, 0x0, 0x8C, 0x8},
		RawGameOverReasonOffsets:  []int{0x22E0418, 0x5C, 0xC},
		WinningPlayersPtrOffsets:  []int{0x22E0418, 0x5C, 0x14},
		WinningPlayersOffsets:     []int{0x8},
		WinningPlayerCountOffsets: []int{0xC},
		GameCodeOffsets:           []int{0x22C3484, 0x5C, 0x0, 0x28, 0x80},
		PlayRegionOffsets:         []int{0x2264684, 0x5C, 0x0, 0x10, 0x8, 0x8},
		PlayMapOffsets:            []int{0x22D56EC, 0x5C, 0x0, 0x18, 0x14},
		StringOffsets:             []int{0x8, 0xC},
		AddPlayerPtr:              0x4,
		PlayerListPtr:             0x10,
		PlayerInfoStructOffsets: PlayerInfoStructOffsets{
			PlayerIDOffset:     0x8,
			OutfitsOffset:      []int{0x1C, 0xC, 0x1C},
			PlayerLevelOffset:  0x20,
			DisconnectedOffset: 0x24,
			RoleTypeOffset:     []int{0x28, 0xC},
			RoleTeamTypeOffset: []int{0x28, 0x3C},
			TasksOffset:        0x2C,
			IsDeadOffset:       0x30,
			ObjectOffset:       0x34,
		},
		WinningPlayerDataOffsets: WinningPlayerDataStructOffsets{
			IsYouOffset:      0x30,
			IsImposterOffset: 0x31,
			IsDeadOffset:     0x32,
		},
		PlayerOutfitStructOffsets: PlayerOutfitStructOffsets{
			ColorIDOffset:     0xC,
			HatIDOffset:       0x10,
			PetIDOffset:       0x14,
			SkinIDOffset:      0x18,
			VisorIDOffset:     0x1C,
			NamePlateIDOffset: 0x20,
			PlayerNameOffset:  0x28,
		},
	}

	// 2. Latest standard 64-bit build (v19s build 7489 / v2024.x IL2CPP x64)
	defaultX64 := &GameOffsets{
		Description:               "v19s (build num: 7489) / v2024.x IL2CPP x64",
		AmongUsClientOffset:       0x34078C0,
		GameDataOffset:            0x344AF48,
		MeetingHudOffset:          0x3409CF8,
		PlayerControlOffset:       0x34078C0,
		GameStartManagerOffset:    0x3492128,
		HudManagerOffset:          0x3492618,
		ServerManagerOffset:       0x3494118,
		TempDataOffset:            0x343EB90,
		GameOptionsOffset:         0x344BEF8,
		MeetingHudPtr:             []int{0x3409CF8, 0xB8, 0x0},
		MeetingHudCachePtrOffsets: []int{0x10},
		MeetingHudStateOffsets:    []int{0xE0},
		GameStateOffsets:          []int{0x34078C0, 0xB8, 0x0, 0xAC},
		AllPlayerPtrOffsets:       []int{0x344AF48, 0xB8, 0x0, 0x20},
		AllPlayersOffsets:         []int{0x10},
		PlayerCountOffsets:        []int{0x18},
		ExiledPlayerIdOffsets:     []int{0x3409CF8, 0xB8, 0x0, 0xF8, 0x38},
		RawGameOverReasonOffsets:  []int{0x343EB90, 0xB8, 0x10},
		WinningPlayersPtrOffsets:  []int{0x343EB90, 0xB8, 0x0},
		WinningPlayersOffsets:     []int{0x10},
		WinningPlayerCountOffsets: []int{0x18},
		GameCodeOffsets:           []int{0x3492128, 0xB8, 0x0, 0x70, 0xE0},
		PlayRegionOffsets:         []int{0x3494118, 0xB8, 0x0, 0x28, 0x10, 0x10},
		PlayMapOffsets:            []int{0x344BEF8, 0xB8, 0x0, 0x28, 0x1C},
		StringOffsets:             []int{0x10, 0x14},
		AddPlayerPtr:              0x8,
		PlayerListPtr:             0x20,
		PlayerInfoStructOffsets: PlayerInfoStructOffsets{
			PlayerIDOffset:     0x38,
			OutfitsOffset:      []int{0x58, 0x18, 0x30},
			PlayerLevelOffset:  0x60,
			DisconnectedOffset: 0x64,
			RoleTypeOffset:     []int{0x68, 0x20},
			RoleTeamTypeOffset: []int{0x68, 0x74},
			TasksOffset:        0x70,
			IsDeadOffset:       0x78,
			ObjectOffset:       0x80,
		},
		WinningPlayerDataOffsets: WinningPlayerDataStructOffsets{
			PlayerNameOffset: 0x10,
			OutfitOffset:     0x18,
			IsYouOffset:      0x20,
			IsImposterOffset: 0x21,
			IsDeadOffset:     0x22,
		},
		PlayerOutfitStructOffsets: PlayerOutfitStructOffsets{
			ColorIDOffset:     0x10,
			HatIDOffset:       0x18,
			PetIDOffset:       0x20,
			SkinIDOffset:      0x28,
			VisorIDOffset:     0x30,
			NamePlateIDOffset: 0x38,
			PlayerNameOffset:  0x40,
		},
	}

	legacyX64 := &GameOffsets{
		Description:               "v2024.6.x (x64 IL2CPP)",
		AmongUsClientOffset:       0x2AA6948,
		GameDataOffset:            0x2A65F38,
		MeetingHudOffset:          0x2AA81F0,
		PlayerControlOffset:       0x2A9C128,
		GameStartManagerOffset:    0x2A31FE8,
		HudManagerOffset:          0x2A32508,
		ServerManagerOffset:       0x2A33CE8,
		TempDataOffset:            0x2A7AAB8,
		GameOptionsOffset:         0x2A669B8,
		MeetingHudPtr:             []int{0x2AA81F0, 0xB8, 0x0},
		MeetingHudCachePtrOffsets: []int{0x10},
		MeetingHudStateOffsets:    []int{0xD0},
		GameStateOffsets:          []int{0x2AA6948, 0xB8, 0x0, 0xEC},
		AllPlayerPtrOffsets:       []int{0x2A65F38, 0xB8, 0x0, 0x30},
		AllPlayersOffsets:         []int{0x10},
		PlayerCountOffsets:        []int{0x18},
		ExiledPlayerIdOffsets:     []int{0x2AA81F0, 0xB8, 0x0, 0xE8, 0x10},
		RawGameOverReasonOffsets:  []int{0x2A7AAB8, 0xB8, 0xC},
		WinningPlayersPtrOffsets:  []int{0x2A7AAB8, 0xB8, 0x18},
		WinningPlayersOffsets:     []int{0x10},
		WinningPlayerCountOffsets: []int{0x18},
		GameCodeOffsets:           []int{0x2A31FE8, 0xB8, 0x0, 0x50, 0xD8},
		PlayRegionOffsets:         []int{0x2A33CE8, 0xB8, 0x0, 0x20, 0x10, 0x10},
		PlayMapOffsets:            []int{0x2A669B8, 0xB8, 0x0, 0x30, 0x1C},
		StringOffsets:             []int{0x10, 0x14},
		AddPlayerPtr:              0x8,
		PlayerListPtr:             0x20,
		PlayerInfoStructOffsets: PlayerInfoStructOffsets{
			PlayerIDOffset:     0x10,
			OutfitsOffset:      []int{0x30, 0x18, 0x30},
			PlayerLevelOffset:  0x38,
			DisconnectedOffset: 0x3C,
			RoleTypeOffset:     []int{0x40, 0x18},
			RoleTeamTypeOffset: []int{0x40, 0x48},
			TasksOffset:        0x48,
			IsDeadOffset:       0x50,
			ObjectOffset:       0x58,
		},
		WinningPlayerDataOffsets: WinningPlayerDataStructOffsets{
			PlayerNameOffset: 0x10,
			OutfitOffset:     0x18,
			IsYouOffset:      0x20,
			IsImposterOffset: 0x21,
			IsDeadOffset:     0x22,
		},
		PlayerOutfitStructOffsets: PlayerOutfitStructOffsets{
			ColorIDOffset:     0x14,
			HatIDOffset:       0x18,
			PetIDOffset:       0x20,
			SkinIDOffset:      0x28,
			VisorIDOffset:     0x30,
			NamePlateIDOffset: 0x38,
			PlayerNameOffset:  0x48,
		},
	}

	om.mu.Lock()
	om.offsets["DEFAULT_X86"] = defaultX86
	om.offsets["DEFAULT_X64"] = defaultX64
	om.offsets["LEGACY_X64"] = legacyX64

	// Register official version hashes
	om.offsets["DBF89A6CE26E641141DE8125FB52CF099A573A9BC8FB04C384C71FDA54F3FE94"] = defaultX86
	om.offsets["1925F256460C80AC017E6B71AC9BC5DE6E0CF1A24F8B822CA9B3494DA2C69D19"] = &GameOffsets{
		Description:               "v2024.3.5s (build num: 3971)",
		AmongUsClientOffset:       0x22F4F10,
		GameDataOffset:            0x22D506C,
		MeetingHudOffset:          0x22F6790,
		GameStartManagerOffset:    0x22C3484,
		HudManagerOffset:          0x22C36B4,
		ServerManagerOffset:       0x2264684,
		TempDataOffset:            0x22E02E0,
		GameOptionsOffset:         0x22D55B4,
		MeetingHudPtr:             []int{0x22F6790, 0x5C, 0x0},
		MeetingHudCachePtrOffsets: []int{0x8},
		MeetingHudStateOffsets:    []int{0x80},
		GameStateOffsets:          []int{0x22F4F10, 0x5C, 0x0, 0x88},
		AllPlayerPtrOffsets:       []int{0x22D506C, 0x5C, 0x0, 0x24},
		AllPlayersOffsets:         []int{0x8},
		PlayerCountOffsets:        []int{0xC},
		ExiledPlayerIdOffsets:     []int{0x22F6790, 0x5C, 0x0, 0x8C, 0x8},
		RawGameOverReasonOffsets:  []int{0x22E02E0, 0x5C, 0xC},
		WinningPlayersPtrOffsets:  []int{0x22E02E0, 0x5C, 0x14},
		WinningPlayersOffsets:     []int{0x8},
		WinningPlayerCountOffsets: []int{0xC},
		GameCodeOffsets:           []int{0x22C3484, 0x5C, 0x0, 0x28, 0x80},
		PlayRegionOffsets:         []int{0x2264684, 0x5C, 0x0, 0x10, 0x8, 0x8},
		PlayMapOffsets:            []int{0x22D55B4, 0x5C, 0x0, 0x18, 0x14},
		StringOffsets:             []int{0x8, 0xC},
		AddPlayerPtr:              0x4,
		PlayerListPtr:             0x10,
		PlayerInfoStructOffsets:   defaultX86.PlayerInfoStructOffsets,
		WinningPlayerDataOffsets:  defaultX86.WinningPlayerDataOffsets,
		PlayerOutfitStructOffsets: defaultX86.PlayerOutfitStructOffsets,
	}
	om.offsets["E0B831C89B9A1367C1501A74F7FCF56B371747E6583653A9017DB5ECF3090363"] = &GameOffsets{
		Description:               "v2024.6.4e (build num: 4234)",
		AmongUsClientOffset:       0x226C55C,
		GameDataOffset:            0x224C1CC,
		MeetingHudOffset:          0x226CED8,
		GameStartManagerOffset:    0x2226BC4,
		HudManagerOffset:          0x2226E54,
		ServerManagerOffset:       0x2227A3C,
		TempDataOffset:            0x22564B0,
		GameOptionsOffset:         0x224C714,
		MeetingHudPtr:             []int{0x226CED8, 0x5C, 0x0},
		MeetingHudCachePtrOffsets: []int{0x8},
		MeetingHudStateOffsets:    []int{0x80},
		GameStateOffsets:          []int{0x226C55C, 0x5C, 0x0, 0x88},
		AllPlayerPtrOffsets:       []int{0x224C1CC, 0x5C, 0x0, 0x24},
		AllPlayersOffsets:         []int{0x8},
		PlayerCountOffsets:        []int{0xC},
		ExiledPlayerIdOffsets:     []int{0x226CED8, 0x5C, 0x0, 0x8C, 0x8},
		RawGameOverReasonOffsets:  []int{0x22564B0, 0x5C, 0xC},
		WinningPlayersPtrOffsets:  []int{0x22564B0, 0x5C, 0x14},
		WinningPlayersOffsets:     []int{0x8},
		WinningPlayerCountOffsets: []int{0xC},
		GameCodeOffsets:           []int{0x2226BC4, 0x5C, 0x0, 0x28, 0x80},
		PlayRegionOffsets:         []int{0x2227A3C, 0x5C, 0x0, 0x10, 0x8, 0x8},
		PlayMapOffsets:            []int{0x224C714, 0x5C, 0x0, 0x18, 0x14},
		StringOffsets:             []int{0x8, 0xC},
		AddPlayerPtr:              0x4,
		PlayerListPtr:             0x10,
		PlayerInfoStructOffsets:   defaultX86.PlayerInfoStructOffsets,
		WinningPlayerDataOffsets:  defaultX86.WinningPlayerDataOffsets,
		PlayerOutfitStructOffsets: defaultX86.PlayerOutfitStructOffsets,
	}
	om.offsets["BB0E5AEA695C51D5B4CD1E284521F9950B4B9ABA91BEE1D6BF0EF1CED40CE7DA"] = &GameOffsets{
		Description:               "v2024.3.5e (build num: 3971)",
		AmongUsClientOffset:       0x227024C,
		GameDataOffset:            0x224FEBC,
		MeetingHudOffset:          0x2270BC8,
		GameStartManagerOffset:    0x222AEE4,
		HudManagerOffset:          0x222B174,
		ServerManagerOffset:       0x222BD5C,
		TempDataOffset:            0x225A1A0,
		GameOptionsOffset:         0x2250404,
		MeetingHudPtr:             []int{0x2270BC8, 0x5C, 0x0},
		MeetingHudCachePtrOffsets: []int{0x8},
		MeetingHudStateOffsets:    []int{0x80},
		GameStateOffsets:          []int{0x227024C, 0x5C, 0x0, 0x88},
		AllPlayerPtrOffsets:       []int{0x224FEBC, 0x5C, 0x0, 0x24},
		AllPlayersOffsets:         []int{0x8},
		PlayerCountOffsets:        []int{0xC},
		ExiledPlayerIdOffsets:     []int{0x2270BC8, 0x5C, 0x0, 0x8C, 0x8},
		RawGameOverReasonOffsets:  []int{0x225A1A0, 0x5C, 0xC},
		WinningPlayersPtrOffsets:  []int{0x225A1A0, 0x5C, 0x14},
		WinningPlayersOffsets:     []int{0x8},
		WinningPlayerCountOffsets: []int{0xC},
		GameCodeOffsets:           []int{0x222AEE4, 0x5C, 0x0, 0x28, 0x80},
		PlayRegionOffsets:         []int{0x222BD5C, 0x5C, 0x0, 0x10, 0x8, 0x8},
		PlayMapOffsets:            []int{0x2250404, 0x5C, 0x0, 0x18, 0x14},
		StringOffsets:             []int{0x8, 0xC},
		AddPlayerPtr:              0x4,
		PlayerListPtr:             0x10,
		PlayerInfoStructOffsets:   defaultX86.PlayerInfoStructOffsets,
		WinningPlayerDataOffsets:  defaultX86.WinningPlayerDataOffsets,
		PlayerOutfitStructOffsets: defaultX86.PlayerOutfitStructOffsets,
	}
	om.offsets["BBF92B7241B72EF60E0ACC7AEEBF3669C27CB32BC04968EAAF60F8B67B9CC660"] = defaultX64
	om.offsets["615A95C9E09B6FFD45570883CB8459311C2B457533A38B37ED1704D6205AEDF9"] = defaultX64
	om.mu.Unlock()
}

func (om *OffsetManager) loadLocalCache() {
	data, err := os.ReadFile(om.cachePath)
	if err != nil {
		return
	}
	var cached map[string]*GameOffsets
	if err := parseHexJSON(data, &cached); err == nil {
		om.mu.Lock()
		for k, v := range cached {
			om.offsets[k] = v
		}
		om.mu.Unlock()
	}
}

func (om *OffsetManager) loadCustomOffsets() {
	data, err := os.ReadFile(om.localPath)
	if err != nil {
		return
	}
	var custom map[string]*GameOffsets
	if err := parseHexJSON(data, &custom); err == nil {
		om.mu.Lock()
		om.localOffsets = custom
		om.mu.Unlock()
	}
}

// RefreshRemote fetches the latest offsets from the remote URL
func (om *OffsetManager) RefreshRemote() error {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(om.remoteURL)
	if err != nil {
		// Try fallback URL
		resp, err = client.Get(BackupOffsetsURL)
		if err != nil {
			return fmt.Errorf("failed to fetch offsets from remote: %w", err)
		}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var fetched map[string]*GameOffsets
	if err := parseHexJSON(body, &fetched); err != nil {
		return fmt.Errorf("failed to parse remote offsets: %w", err)
	}

	om.mu.Lock()
	count := 0
	for k, v := range fetched {
		om.offsets[k] = v
		count++
	}
	om.mu.Unlock()

	// Write cache
	_ = os.WriteFile(om.cachePath, body, 0644)
	return nil
}

// GetOffsetByHash looks up offsets for a specific GameAssembly SHA256 hash
func (om *OffsetManager) GetOffsetByHash(hash string) *GameOffsets {
	om.mu.RLock()
	defer om.mu.RUnlock()

	upper := strings.ToUpper(strings.TrimSpace(hash))

	// 1. Check custom overrides
	for k, v := range om.localOffsets {
		if strings.EqualFold(k, upper) {
			return v
		}
	}

	// 2. Check fetched / cached / embedded offsets
	if val, ok := om.offsets[upper]; ok {
		return val
	}
	for k, v := range om.offsets {
		if strings.EqualFold(k, upper) {
			return v
		}
	}

	return nil
}

// GetDefaultOffsets returns default fallback offsets (64-bit or 32-bit)
func (om *OffsetManager) GetDefaultOffsets(is64 bool) *GameOffsets {
	om.mu.RLock()
	defer om.mu.RUnlock()
	if is64 {
		if val, ok := om.offsets["DEFAULT_X64"]; ok {
			return val
		}
	} else {
		if val, ok := om.offsets["DEFAULT_X86"]; ok {
			return val
		}
	}
	return nil
}

// GetAllOffsets returns all registered offsets map
func (om *OffsetManager) GetAllOffsets() map[string]*GameOffsets {
	om.mu.RLock()
	defer om.mu.RUnlock()
	res := make(map[string]*GameOffsets, len(om.offsets))
	for k, v := range om.offsets {
		res[k] = v
	}
	return res
}

// GetAllKnownOffsets returns a prioritized slice of known offset templates matching the bitness
func (om *OffsetManager) GetAllKnownOffsets(is64 bool) []*GameOffsets {
	om.mu.RLock()
	defer om.mu.RUnlock()

	var result []*GameOffsets
	seen := make(map[int]bool) // deduplicate by GameDataOffset or AllPlayerPtrOffsets[0]

	// Add default first
	def := om.GetDefaultOffsets(is64)
	if def != nil {
		result = append(result, def)
		if len(def.AllPlayerPtrOffsets) > 0 {
			seen[def.AllPlayerPtrOffsets[0]] = true
		}
	}

	var nonDefault []*GameOffsets
	for _, v := range om.offsets {
		if v == nil || len(v.AllPlayerPtrOffsets) == 0 {
			continue
		}
		// Distinguish 64-bit vs 32-bit:
		isOffset64 := v.AddPlayerPtr == 8 || v.PlayerListPtr >= 0x20 || (len(v.AllPlayerPtrOffsets) > 1 && v.AllPlayerPtrOffsets[1] == 0xB8)
		if isOffset64 == is64 {
			rva := v.AllPlayerPtrOffsets[0]
			if !seen[rva] {
				seen[rva] = true
				nonDefault = append(nonDefault, v)
			}
		}
	}

	// Sort descending by RVA (higher RVAs are usually newer game builds)
	sort.Slice(nonDefault, func(i, j int) bool {
		return nonDefault[i].AllPlayerPtrOffsets[0] > nonDefault[j].AllPlayerPtrOffsets[0]
	})

	result = append(result, nonDefault...)
	return result
}
