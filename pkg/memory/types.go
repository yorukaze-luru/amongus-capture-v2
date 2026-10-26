package memory

import (
	"fmt"
	"strings"
)

// GameState represents the state of the Among Us game
type GameState int

const (
	StateMenu       GameState = 0
	StateLobby      GameState = 1
	StateTasks      GameState = 2
	StateDiscussion GameState = 3
	StateGameOver   GameState = 4
	StateEnded      GameState = 4 // Alias for backwards compatibility
	StateUnknown    GameState = 5
)

func (s GameState) String() string {
	switch s {
	case StateMenu:
		return "MENU"
	case StateLobby:
		return "LOBBY"
	case StateTasks:
		return "TASKS"
	case StateDiscussion:
		return "DISCUSSION"
	case StateGameOver:
		return "GAME_OVER"
	default:
		return "MENU" // Fallback so gameStateStr does not become UNKNOWN
	}
}

// PlayerAction represents an action performed by or happening to a player
type PlayerAction int

const (
	ActionJoined       PlayerAction = 0
	ActionLeft         PlayerAction = 1
	ActionDied         PlayerAction = 2
	ActionChangedColor PlayerAction = 3
	ActionForceUpdated PlayerAction = 4
	ActionDisconnected PlayerAction = 5
	ActionExiled       PlayerAction = 6
)

func (a PlayerAction) String() string {
	switch a {
	case ActionJoined:
		return "JOINED"
	case ActionLeft:
		return "LEFT"
	case ActionDied:
		return "DIED"
	case ActionChangedColor:
		return "CHANGED_COLOR"
	case ActionForceUpdated:
		return "FORCE_UPDATED"
	case ActionDisconnected:
		return "DISCONNECTED"
	case ActionExiled:
		return "EXILED"
	default:
		return "UNKNOWN"
	}
}

// PlayerColor represents player color in Among Us
type PlayerColor int

const (
	ColorRed       PlayerColor = 0
	ColorBlue      PlayerColor = 1
	ColorGreen     PlayerColor = 2
	ColorPink      PlayerColor = 3
	ColorOrange    PlayerColor = 4
	ColorYellow    PlayerColor = 5
	ColorBlack     PlayerColor = 6
	ColorWhite     PlayerColor = 7
	ColorPurple    PlayerColor = 8
	ColorBrown     PlayerColor = 9
	ColorCyan      PlayerColor = 10
	ColorLime      PlayerColor = 11
	ColorMaroon    PlayerColor = 12
	ColorRose      PlayerColor = 13
	ColorBanana    PlayerColor = 14
	ColorGray      PlayerColor = 15
	ColorTan       PlayerColor = 16
	ColorCoral     PlayerColor = 17
	ColorSunset    PlayerColor = 17 // Alias for backwards compatibility
	ColorUnknown   PlayerColor = 255
)

var ColorNames = map[PlayerColor]string{
	ColorRed:    "Red",
	ColorBlue:   "Blue",
	ColorGreen:  "Green",
	ColorPink:   "Pink",
	ColorOrange: "Orange",
	ColorYellow: "Yellow",
	ColorBlack:  "Black",
	ColorWhite:  "White",
	ColorPurple: "Purple",
	ColorBrown:  "Brown",
	ColorCyan:   "Cyan",
	ColorLime:   "Lime",
	ColorMaroon: "Maroon",
	ColorRose:   "Rose",
	ColorBanana: "Banana",
	ColorGray:   "Gray",
	ColorTan:    "Tan",
	ColorCoral:  "Coral",
}

var ColorHex = map[PlayerColor]string{
	ColorRed:    "#c51111",
	ColorBlue:   "#132ed1",
	ColorGreen:  "#117f2d",
	ColorPink:   "#ed54ba",
	ColorOrange: "#ef7d0d",
	ColorYellow: "#f5f557",
	ColorBlack:  "#3f474e",
	ColorWhite:  "#d6e0f0",
	ColorPurple: "#6b2fbb",
	ColorBrown:  "#71491e",
	ColorCyan:   "#38fedc",
	ColorLime:   "#50ef39",
	ColorMaroon: "#5f1e2d",
	ColorRose:   "#ecc0d3",
	ColorBanana: "#fffebe",
	ColorGray:   "#708496",
	ColorTan:    "#928776",
	ColorCoral:  "#d76464",
}

var ColorEmojis = map[PlayerColor]string{
	ColorRed:    "🔴",
	ColorBlue:   "🔵",
	ColorGreen:  "🟢",
	ColorPink:   "🌸",
	ColorOrange: "🟠",
	ColorYellow: "🟡",
	ColorBlack:  "⚫",
	ColorWhite:  "⚪",
	ColorPurple: "🟣",
	ColorBrown:  "🟤",
	ColorCyan:   "💎",
	ColorLime:   "🍏",
	ColorMaroon: "🍷",
	ColorRose:   "🌹",
	ColorBanana: "🍌",
	ColorGray:   "🐘",
	ColorTan:    "🪵",
	ColorCoral:  "🌺",
}

func (c PlayerColor) String() string {
	if name, ok := ColorNames[c]; ok {
		return name
	}
	return fmt.Sprintf("Color(%d)", int(c))
}

func (c PlayerColor) Hex() string {
	if hex, ok := ColorHex[c]; ok {
		return hex
	}
	return "#888888"
}

func (c PlayerColor) Emoji() string {
	if emoji, ok := ColorEmojis[c]; ok {
		return emoji
	}
	return "🎨"
}

// ParseColorByName converts a color name (case-insensitive) to PlayerColor
func ParseColorByName(name string) PlayerColor {
	trimmed := strings.TrimSpace(name)
	if strings.EqualFold(trimmed, "Coral") || trimmed == "コーラル" || strings.EqualFold(trimmed, "Sunset") || trimmed == "サンセット" {
		return ColorCoral
	}
	for c, n := range ColorNames {
		if strings.EqualFold(n, trimmed) {
			return c
		}
	}
	return ColorUnknown
}

// GameOverReason represents how the game ended
type GameOverReason int

const (
	GameOverHumansByVote GameOverReason = iota
	GameOverHumansByTask
	GameOverImpostorByVote
	GameOverImpostorByKill
	GameOverImpostorBySabotage
	GameOverImpostorDisconnect
	GameOverHumansDisconnect
	GameOverUnknown = 255
)

func (r GameOverReason) String() string {
	switch r {
	case GameOverHumansByVote:
		return "HumansByVote"
	case GameOverHumansByTask:
		return "HumansByTask"
	case GameOverImpostorByVote:
		return "ImpostorByVote"
	case GameOverImpostorByKill:
		return "ImpostorByKill"
	case GameOverImpostorBySabotage:
		return "ImpostorBySabotage"
	case GameOverImpostorDisconnect:
		return "ImpostorDisconnect"
	case GameOverHumansDisconnect:
		return "HumansDisconnect"
	default:
		return "Unknown"
	}
}

// PlayerInfo holds information about a player in the game
type PlayerInfo struct {
	PlayerID       byte            `json:"id"`
	Name           string          `json:"name"`
	Color          PlayerColor     `json:"color"`
	ColorName      string          `json:"colorName"`
	ColorHex       string          `json:"colorHex"`
	IsDead         bool            `json:"isDead"`
	Disconnected   bool            `json:"disconnected"`
	RoleType       int32           `json:"roleType"`
	RoleTeamType   int32           `json:"roleTeamType"`
	RoleName       string          `json:"roleName"`
	RoleNameJa     string          `json:"roleNameJa,omitempty"`
	IsImpostor     bool            `json:"isImpostor"`
	HatID          uint32          `json:"hat"`
	PetID          uint32          `json:"pet"`
	SkinID         uint32          `json:"skin"`
	VisorID        uint32          `json:"visor"`
	NamePlateID    uint32          `json:"namePlate"`
	StructAddress  string          `json:"structAddress,omitempty"`
	RawOffsets     map[string]byte `json:"rawOffsets,omitempty"`
	IsDeadRawByte  byte            `json:"isDeadRawByte"`
	RoleRawInt     int32           `json:"roleRawInt"`
	RoleTeamRawInt int32           `json:"roleTeamRawInt"`
}

// RoleTypeName returns human-readable role name for RoleTypes enum value
func RoleTypeName(roleType int32) string {
	switch roleType {
	case 0:
		return "Crewmate"
	case 1:
		return "Impostor"
	case 2:
		return "Scientist"
	case 3:
		return "Engineer"
	case 4:
		return "GuardianAngel"
	case 5:
		return "Shapeshifter"
	case 6:
		return "CrewmateGhost"
	case 7:
		return "ImpostorGhost"
	case 8:
		return "Noisemaker"
	case 9:
		return "Phantom"
	case 10:
		return "Tracker"
	case 11:
		return "Detective"
	case 12:
		return "Viper"
	case 13:
		return "Judge"
	case 14:
		return "SpiritGuide"
	default:
		return fmt.Sprintf("Role(%d)", roleType)
	}
}

// RoleTypeJapanese returns localized Japanese role name for RoleTypes enum value
func RoleTypeJapanese(roleType int32) string {
	switch roleType {
	case 0:
		return "クルーメイト"
	case 1:
		return "インポスター"
	case 2:
		return "科学者"
	case 3:
		return "エンジニア"
	case 4:
		return "守護天使"
	case 5:
		return "シェイプシフター"
	case 6:
		return "亡霊クルー"
	case 7:
		return "亡霊インポスター"
	case 8:
		return "ノイズメーカー"
	case 9:
		return "ファントム"
	case 10:
		return "トラッカー"
	case 11:
		return "ディテクティブ"
	case 12:
		return "ヴァイパー"
	case 13:
		return "ジャッジ"
	case 14:
		return "スピリットガイド"
	default:
		return "クルーメイト"
	}
}

// IsImpostorRole returns true if role or team corresponds to an impostor
func IsImpostorRole(roleType int32, teamType int32) bool {
	if teamType == 1 {
		return true
	}
	switch roleType {
	case 1, 5, 7, 9, 12: // Impostor, Shapeshifter, ImpostorGhost, Phantom, Viper
		return true
	default:
		return false
	}
}

// GameStateChangeEvent is dispatched when the game state changes
type GameStateChangeEvent struct {
	OldState GameState `json:"oldState"`
	NewState GameState `json:"newState"`
}

// PlayerChangeEvent is dispatched when a player's state changes
type PlayerChangeEvent struct {
	Action       PlayerAction `json:"action"`
	Name         string       `json:"name"`
	IsDead       bool         `json:"isDead"`
	Disconnected bool         `json:"disconnected"`
	Color        PlayerColor  `json:"color"`
}

// LobbyEvent is dispatched when the lobby info is updated
type LobbyEvent struct {
	LobbyCode string `json:"lobbyCode"`
	Region    int32  `json:"region"`
	PlayMap   int32  `json:"playMap"`
}

// GameOverEvent is dispatched when the game ends
type GameOverEvent struct {
	GameOverReason GameOverReason `json:"gameOverReason"`
	WinningPlayers []PlayerInfo   `json:"winningPlayers"`
	MatchResult    MatchResult    `json:"matchResult"`
}

// PlayerSnapshotItem represents a single player entry in requirement format
type PlayerSnapshotItem struct {
	Name   string `json:"name"`
	IsDead bool   `json:"isDead"`
	Color  int    `json:"color"`
}

// GameSnapshot represents the complete game snapshot in requirement format
type GameSnapshot struct {
	GameState    int                  `json:"gameState"`
	GameStateStr string               `json:"gameStateStr"`
	LobbyCode    string               `json:"lobbyCode"`
	PlayerCount  int                  `json:"playerCount"`
	Players      []PlayerSnapshotItem `json:"players"`
}

// ToSnapshotItem converts PlayerInfo to PlayerSnapshotItem
func (p PlayerInfo) ToSnapshotItem() PlayerSnapshotItem {
	return PlayerSnapshotItem{
		Name:   p.Name,
		IsDead: p.IsDead,
		Color:  int(p.Color),
	}
}
