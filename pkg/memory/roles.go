package memory

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// RoleFaction represents the alignment/team of a role
type RoleFaction string

const (
	FactionCrewmate RoleFaction = "Crewmate"
	FactionImpostor RoleFaction = "Impostor"
	FactionNeutral  RoleFaction = "Neutral"
	FactionUnknown  RoleFaction = "Unknown"
)

// Vanilla Role IDs
const (
	VanillaCrewmate      = 0
	VanillaImpostor      = 1
	VanillaScientist     = 2
	VanillaEngineer      = 3
	VanillaGuardianAngel = 4
	VanillaShapeshifter  = 5
	VanillaCrewGhost     = 6
	VanillaImpGhost      = 7
	VanillaNoiseMaker    = 8
	VanillaPhantom       = 9
	VanillaTracker       = 10
)

// RoleDefinition holds display info for a recognized role
type RoleDefinition struct {
	Name        string
	Faction     RoleFaction
	ColorHex    string
	Aliases     []string
}

// KnownRoles includes Vanilla and popular MOD roles (Town of Host, Town of Us, SuperNewRoles, etc.)
var KnownRoles = []RoleDefinition{
	// Vanilla Roles
	{Name: "Crewmate", Faction: FactionCrewmate, ColorHex: "#8cffff", Aliases: []string{"クルーメイト", "クルー", "crew", "crewmate"}},
	{Name: "Engineer", Faction: FactionCrewmate, ColorHex: "#ff9100", Aliases: []string{"エンジニア", "engineer"}},
	{Name: "Scientist", Faction: FactionCrewmate, ColorHex: "#3bb2ff", Aliases: []string{"科学者", "サイエンティスト", "scientist"}},
	{Name: "Tracker", Faction: FactionCrewmate, ColorHex: "#39ff14", Aliases: []string{"トラッカー", "tracker"}},
	{Name: "NoiseMaker", Faction: FactionCrewmate, ColorHex: "#00e5ff", Aliases: []string{"ノイズメーカー", "noisemaker"}},
	{Name: "Guardian Angel", Faction: FactionCrewmate, ColorHex: "#7fffd4", Aliases: []string{"守護天使", "ガーディアンエンジェル", "guardian", "guardian angel"}},

	{Name: "Impostor", Faction: FactionImpostor, ColorHex: "#ff1919", Aliases: []string{"インポスター", "人狼", "imp", "impostor"}},
	{Name: "Shapeshifter", Faction: FactionImpostor, ColorHex: "#ff4040", Aliases: []string{"シェイプシフター", "変身者", "shapeshifter", "ss"}},
	{Name: "Phantom", Faction: FactionImpostor, ColorHex: "#b30000", Aliases: []string{"ファントム", "phantom"}},

	// Town of Host / Town of Us / SNR - Crewmate Roles
	{Name: "Sheriff", Faction: FactionCrewmate, ColorHex: "#f5d142", Aliases: []string{"シェリフ", "保安官", "sheriff"}},
	{Name: "Mayor", Faction: FactionCrewmate, ColorHex: "#20427d", Aliases: []string{"市長", "メイヤー", "mayor"}},
	{Name: "Doctor", Faction: FactionCrewmate, ColorHex: "#80ffff", Aliases: []string{"ドクター", "医者", "doctor"}},
	{Name: "Bait", Faction: FactionCrewmate, ColorHex: "#00ffff", Aliases: []string{"ベイト", "餌", "bait"}},
	{Name: "Lighter", Faction: FactionCrewmate, ColorHex: "#ffdd55", Aliases: []string{"ライター", "lighter"}},
	{Name: "Snitch", Faction: FactionCrewmate, ColorHex: "#b8b500", Aliases: []string{"スニッチ", "密告者", "snitch"}},
	{Name: "Seer", Faction: FactionCrewmate, ColorHex: "#66ccff", Aliases: []string{"預言者", "シアー", "seer"}},
	{Name: "Medic", Faction: FactionCrewmate, ColorHex: "#00ffaa", Aliases: []string{"メディック", "衛生兵", "medic"}},
	{Name: "Detective", Faction: FactionCrewmate, ColorHex: "#4d79ff", Aliases: []string{"探偵", "ディテクティブ", "detective"}},
	{Name: "Dictator", Faction: FactionCrewmate, ColorHex: "#ffaa00", Aliases: []string{"独裁者", "ディクテーター", "dictator"}},
	{Name: "Judge", Faction: FactionCrewmate, ColorHex: "#bfa15f", Aliases: []string{"裁判官", "ジャッジ", "judge"}},
	{Name: "Nice Guesser", Faction: FactionCrewmate, ColorHex: "#ffd700", Aliases: []string{"ナイスゲッサー", "nice guesser", "guesser (crew)"}},
	{Name: "Child", Faction: FactionCrewmate, ColorHex: "#ffb6c1", Aliases: []string{"子供", "チャイルド", "child"}},
	{Name: "Swapper", Faction: FactionCrewmate, ColorHex: "#66ffcc", Aliases: []string{"スワッパー", "swapper"}},
	{Name: "Fortune Teller", Faction: FactionCrewmate, ColorHex: "#c8a2c8", Aliases: []string{"占い師", "fortune teller"}},
	{Name: "Veteran", Faction: FactionCrewmate, ColorHex: "#a0522d", Aliases: []string{"退役軍人", "ベテラン", "veteran"}},
	{Name: "Speed Booster", Faction: FactionCrewmate, ColorHex: "#00ff7f", Aliases: []string{"スピードブースター", "speed booster"}},
	{Name: "Bodyguard", Faction: FactionCrewmate, ColorHex: "#4682b4", Aliases: []string{"ボディーガード", "bodyguard"}},

	// Town of Host / Town of Us / SNR - Impostor & Madmate Roles
	{Name: "Madmate", Faction: FactionImpostor, ColorHex: "#ff6666", Aliases: []string{"マッドメイト", "狂人", "madmate"}},
	{Name: "Mad Guardian", Faction: FactionImpostor, ColorHex: "#ff8888", Aliases: []string{"マッドガーディアン", "mad guardian"}},
	{Name: "Mad Snitch", Faction: FactionImpostor, ColorHex: "#ffaaaa", Aliases: []string{"マッドスニッチ", "mad snitch"}},
	{Name: "Evil Guesser", Faction: FactionImpostor, ColorHex: "#cc0000", Aliases: []string{"イービルゲッサー", "evil guesser"}},
	{Name: "Serial Killer", Faction: FactionImpostor, ColorHex: "#8b0000", Aliases: []string{"シリアルキラー", "連続殺人鬼", "serial killer"}},
	{Name: "Vampire", Faction: FactionImpostor, ColorHex: "#990033", Aliases: []string{"ヴァンパイア", "吸血鬼", "vampire"}},
	{Name: "Warlock", Faction: FactionImpostor, ColorHex: "#800080", Aliases: []string{"ウォーロック", "妖術師", "warlock"}},
	{Name: "Witch", Faction: FactionImpostor, ColorHex: "#4b0082", Aliases: []string{"魔女", "ウィッチ", "witch"}},
	{Name: "Bounty Hunter", Faction: FactionImpostor, ColorHex: "#d2691e", Aliases: []string{"賞金稼ぎ", "バウンティハンター", "bounty hunter"}},
	{Name: "Mare", Faction: FactionImpostor, ColorHex: "#483d8b", Aliases: []string{"メア", "夢魔", "mare"}},
	{Name: "Puppeteer", Faction: FactionImpostor, ColorHex: "#8b4513", Aliases: []string{"人形遣い", "パペッティア", "puppeteer"}},
	{Name: "Sniper", Faction: FactionImpostor, ColorHex: "#696969", Aliases: []string{"スナイパー", "狙撃手", "sniper"}},
	{Name: "Ninja", Faction: FactionImpostor, ColorHex: "#2f4f4f", Aliases: []string{"忍者", "ニンジャ", "ninja"}},
	{Name: "YingYanger", Faction: FactionImpostor, ColorHex: "#ff0066", Aliases: []string{"陰陽師", "yingyanger"}},
	{Name: "Grenadier", Faction: FactionImpostor, ColorHex: "#708090", Aliases: []string{"グレネーダー", "grenadier"}},

	// Town of Host / Town of Us / SNR - Neutral / Third-Party Roles
	{Name: "Jester", Faction: FactionNeutral, ColorHex: "#ff69b4", Aliases: []string{"ジェスター", "てるてる", "道化師", "jester"}},
	{Name: "Jackal", Faction: FactionNeutral, ColorHex: "#00aaff", Aliases: []string{"ジャッカル", "jackal"}},
	{Name: "Sidekick", Faction: FactionNeutral, ColorHex: "#33ccff", Aliases: []string{"サイドキック", "相棒", "sidekick"}},
	{Name: "Arsonist", Faction: FactionNeutral, ColorHex: "#ff4500", Aliases: []string{"アーソニスト", "放火魔", "arsonist"}},
	{Name: "Vulture", Faction: FactionNeutral, ColorHex: "#a0522d", Aliases: []string{"ハゲワシ", "バルチャー", "vulture"}},
	{Name: "Executioner", Faction: FactionNeutral, ColorHex: "#800000", Aliases: []string{"処刑人", "エグゼクショナー", "executioner", "exe"}},
	{Name: "Lawyer", Faction: FactionNeutral, ColorHex: "#708090", Aliases: []string{"弁護士", "ローヤー", "lawyer"}},
	{Name: "Lover", Faction: FactionNeutral, ColorHex: "#ff1493", Aliases: []string{"恋人", "ラバーズ", "lover", "lovers"}},
	{Name: "Pestilence", Faction: FactionNeutral, ColorHex: "#4b0082", Aliases: []string{"ペスティレンス", "黒死病", "pestilence"}},
	{Name: "Plaguebearer", Faction: FactionNeutral, ColorHex: "#556b2f", Aliases: []string{"疫病神", "プレイグベアラー", "plaguebearer"}},
	{Name: "Egoist", Faction: FactionNeutral, ColorHex: "#9400d3", Aliases: []string{"エゴイスト", "利己主義者", "egoist"}},
	{Name: "Terrorist", Faction: FactionNeutral, ColorHex: "#228b22", Aliases: []string{"テロリスト", "terrorist"}},
	{Name: "Opportunist", Faction: FactionNeutral, ColorHex: "#00ff7f", Aliases: []string{"オポチュニスト", "日和見主義者", "opportunist"}},
	{Name: "Schrodinger's Cat", Faction: FactionNeutral, ColorHex: "#9370db", Aliases: []string{"シュレディンガーの猫", "schrodinger's cat", "cat"}},
	{Name: "Pyromaniac", Faction: FactionNeutral, ColorHex: "#ff8c00", Aliases: []string{"パイロマニアック", "pyromaniac"}},
	{Name: "Agitator", Faction: FactionNeutral, ColorHex: "#ff007f", Aliases: []string{"アジテーター", "煽動者", "agitator"}},
	{Name: "Blood Knight", Faction: FactionNeutral, ColorHex: "#8b0000", Aliases: []string{"ブラッドナイト", "blood knight"}},
}

// Regex patterns to detect role in player name tags from Town of Host and other mods
var (
	// Matches [RoleName] Player or (RoleName) Player
	prefixTagRegex = regexp.MustCompile(`^(?:\[|\()([^\]\)]+)(?:\]|\))\s*(.*)$`)
	// Matches Player [RoleName] or Player (RoleName)
	suffixTagRegex = regexp.MustCompile(`^(.*?)\s*(?:\[|\()([^\]\)]+)(?:\]|\))$`)
	// Matches Unity Rich Text <color=...>...</color> or <b>, <i>, etc.
	richTextRegex = regexp.MustCompile(`<[^>]*>`)
)

// MatchResult represents the detailed result of the last match
type MatchResult struct {
	GameOverReason GameOverReason `json:"gameOverReason"`
	ReasonText     string         `json:"reasonText"`
	WinningFaction RoleFaction    `json:"winningFaction"` // "Crewmate", "Impostor", "Neutral", etc.
	WinningTitle   string         `json:"winningTitle"`   // e.g. "🏆 クルーメイト陣営の勝利！"
	WinningColor   string         `json:"winningColor"`   // "#3b82f6", "#ef4444", etc.
	EndTime        string         `json:"endTime"`
	Players        []PlayerResult `json:"players"`
}

// PlayerResult represents individual player result and role details
type PlayerResult struct {
	ID           byte        `json:"id"`
	RawName      string      `json:"rawName"`
	CleanName    string      `json:"cleanName"`
	Color        PlayerColor `json:"color"`
	ColorHex     string      `json:"colorHex"`
	RoleName     string      `json:"roleName"`
	RoleFaction  RoleFaction `json:"roleFaction"`
	RoleColorHex string      `json:"roleColorHex"`
	IsDead       bool        `json:"isDead"`
	Disconnected bool        `json:"disconnected"`
	IsWinner     bool        `json:"isWinner"`
}

// ParseRoleAndCleanName inspects player raw name and internal role flags to deduce clean name and role
func ParseRoleAndCleanName(rawName string, roleType int32, isImpostor bool) (cleanName string, roleName string, faction RoleFaction, roleColorHex string) {
	stripped := richTextRegex.ReplaceAllString(rawName, "")
	stripped = strings.TrimSpace(stripped)

	var extractedRole string
	cleanName = stripped

	// 1. Check for prefix [Role] Name or (Role) Name
	if match := prefixTagRegex.FindStringSubmatch(stripped); len(match) == 3 {
		extractedRole = strings.TrimSpace(match[1])
		cleanName = strings.TrimSpace(match[2])
	} else if match := suffixTagRegex.FindStringSubmatch(stripped); len(match) == 3 {
		// 2. Check for suffix Name [Role] or Name (Role)
		cleanName = strings.TrimSpace(match[1])
		extractedRole = strings.TrimSpace(match[2])
	}

	// 3. Match extractedRole with known MOD / Vanilla role dictionary
	if extractedRole != "" {
		for _, def := range KnownRoles {
			for _, alias := range def.Aliases {
				if strings.EqualFold(extractedRole, alias) || strings.EqualFold(extractedRole, def.Name) {
					return cleanName, def.Name, def.Faction, def.ColorHex
				}
			}
		}
		// If unknown custom MOD role name, return as Neutral/Custom
		return cleanName, extractedRole, FactionNeutral, "#a855f7"
	}

	// 4. Fallback to Vanilla RoleType
	switch roleType {
	case VanillaImpostor:
		return cleanName, "Impostor", FactionImpostor, "#ff1919"
	case VanillaScientist:
		return cleanName, "Scientist", FactionCrewmate, "#3bb2ff"
	case VanillaEngineer:
		return cleanName, "Engineer", FactionCrewmate, "#ff9100"
	case VanillaGuardianAngel:
		return cleanName, "Guardian Angel", FactionCrewmate, "#7fffd4"
	case VanillaShapeshifter:
		return cleanName, "Shapeshifter", FactionImpostor, "#ff4040"
	case VanillaNoiseMaker:
		return cleanName, "NoiseMaker", FactionCrewmate, "#00e5ff"
	case VanillaPhantom:
		return cleanName, "Phantom", FactionImpostor, "#b30000"
	case VanillaTracker:
		return cleanName, "Tracker", FactionCrewmate, "#39ff14"
	default:
		if isImpostor {
			return cleanName, "Impostor", FactionImpostor, "#ff1919"
		}
		return cleanName, "Crewmate", FactionCrewmate, "#8cffff"
	}
}

// ComputeMatchResult evaluates the game over state and assigns victory to the winning team and players
func ComputeMatchResult(reason GameOverReason, players []PlayerInfo) MatchResult {
	var reasonText string
	var winningFaction RoleFaction = FactionUnknown
	var winningTitle = "🏆 ゲーム終了"
	var winningColor = "#6b7280"

	switch reason {
	case GameOverHumansByVote:
		reasonText = "タスク・追放によるクルーメイトの勝利"
		winningFaction = FactionCrewmate
		winningTitle = "🏆 CREWMATE WIN (クルーメイト陣営の勝利)"
		winningColor = "#3b82f6"
	case GameOverHumansByTask:
		reasonText = "全タスク完了によるクルーメイトの勝利"
		winningFaction = FactionCrewmate
		winningTitle = "🏆 CREWMATE WIN (全タスク完了勝利)"
		winningColor = "#3b82f6"
	case GameOverHumansDisconnect:
		reasonText = "クルーメイト切断によるインポスター勝利"
		winningFaction = FactionImpostor
		winningTitle = "🏆 IMPOSTOR WIN (インポスター勝利)"
		winningColor = "#ef4444"
	case GameOverImpostorByVote:
		reasonText = "インポスター投票によるインポスター勝利"
		winningFaction = FactionImpostor
		winningTitle = "🏆 IMPOSTOR WIN (インポスター陣営の勝利)"
		winningColor = "#ef4444"
	case GameOverImpostorByKill:
		reasonText = "キル全滅によるインポスター勝利"
		winningFaction = FactionImpostor
		winningTitle = "🏆 IMPOSTOR WIN (インポスター殲滅勝利)"
		winningColor = "#ef4444"
	case GameOverImpostorBySabotage:
		reasonText = "サボタージュ成功によるインポスター勝利"
		winningFaction = FactionImpostor
		winningTitle = "🏆 IMPOSTOR WIN (サボタージュ勝利)"
		winningColor = "#ef4444"
	case GameOverImpostorDisconnect:
		reasonText = "インポスター切断によるクルーメイト勝利"
		winningFaction = FactionCrewmate
		winningTitle = "🏆 CREWMATE WIN (クルーメイト勝利)"
		winningColor = "#3b82f6"
	default:
		reasonText = fmt.Sprintf("ゲーム終了 (%s)", reason.String())
		// Heuristic: check if alive impostors >= alive crewmates
		aliveCrew := 0
		aliveImp := 0
		for _, p := range players {
			if !p.IsDead && !p.Disconnected {
				if p.IsImpostor {
					aliveImp++
				} else {
					aliveCrew++
				}
			}
		}
		if aliveImp > 0 && aliveImp >= aliveCrew {
			winningFaction = FactionImpostor
			winningTitle = "🏆 IMPOSTOR WIN (インポスター勝利)"
			winningColor = "#ef4444"
		} else {
			winningFaction = FactionCrewmate
			winningTitle = "🏆 CREWMATE WIN (クルーメイト勝利)"
			winningColor = "#3b82f6"
		}
	}

	var results []PlayerResult
	for _, p := range players {
		cleanName, roleName, faction, rColor := ParseRoleAndCleanName(p.Name, p.RoleType, p.IsImpostor)
		
		isWinner := false
		if faction == winningFaction {
			isWinner = true
		}

		results = append(results, PlayerResult{
			ID:           p.PlayerID,
			RawName:      p.Name,
			CleanName:    cleanName,
			Color:        p.Color,
			ColorHex:     p.ColorHex,
			RoleName:     roleName,
			RoleFaction:  faction,
			RoleColorHex: rColor,
			IsDead:       p.IsDead,
			Disconnected: p.Disconnected,
			IsWinner:     isWinner,
		})
	}

	return MatchResult{
		GameOverReason: reason,
		ReasonText:     reasonText,
		WinningFaction: winningFaction,
		WinningTitle:   winningTitle,
		WinningColor:   winningColor,
		EndTime:        time.Now().Format("15:04:05"),
		Players:        results,
	}
}
