package bot

import (
	"strings"
	"testing"

	"amongus-capture-v2/pkg/config"
	"amongus-capture-v2/pkg/memory"
)

func TestSetChannels_UnsetStatusCID(t *testing.T) {
	cfgMgr := config.NewConfigManager()

	bot := NewDiscordBot(cfgMgr, nil)
	defer bot.Stop()

	// Initial set with a status channel
	bot.SetChannels("guild1", "vc1", "tc1")
	bot.mu.Lock()
	bot.statusMsgID = "msg123" // Simulate active embed message
	bot.mu.Unlock()

	bot.mu.RLock()
	initStatusCID := bot.statusCID
	bot.mu.RUnlock()

	if initStatusCID != "tc1" {
		t.Fatalf("expected statusCID to be 'tc1', got '%s'", initStatusCID)
	}

	// Change to no status channel (empty string)
	bot.SetChannels("guild1", "vc1", "")

	bot.mu.RLock()
	currentStatusCID := bot.statusCID
	currentStatusMsgID := bot.statusMsgID
	bot.mu.RUnlock()

	if currentStatusCID != "" {
		t.Errorf("expected statusCID to be empty, got '%s'", currentStatusCID)
	}
	if currentStatusMsgID != "" {
		t.Errorf("expected statusMsgID to be cleared, got '%s'", currentStatusMsgID)
	}

	// Config should also reflect empty statusCID
	savedCfg := cfgMgr.Get()
	if savedCfg.DiscordStatusCID != "" {
		t.Errorf("expected config DiscordStatusCID to be empty, got '%s'", savedCfg.DiscordStatusCID)
	}
}

func TestSetChannels_ChangeStatusCID(t *testing.T) {
	cfgMgr := config.NewConfigManager()

	bot := NewDiscordBot(cfgMgr, nil)
	defer bot.Stop()

	// Set initial channel
	bot.SetChannels("guild1", "vc1", "tc1")
	bot.mu.Lock()
	bot.statusMsgID = "msg123"
	bot.mu.Unlock()

	// Change to different channel
	bot.SetChannels("guild1", "vc1", "tc2")

	bot.mu.RLock()
	currentStatusCID := bot.statusCID
	currentStatusMsgID := bot.statusMsgID
	bot.mu.RUnlock()

	if currentStatusCID != "tc2" {
		t.Errorf("expected statusCID to be 'tc2', got '%s'", currentStatusCID)
	}
	if currentStatusMsgID != "" {
		t.Errorf("expected statusMsgID to be reset to empty for new channel, got '%s'", currentStatusMsgID)
	}
}

func TestConfirmedDead_TasksAndDiscussion(t *testing.T) {
	cfgMgr := config.NewConfigManager()
	gr := memory.NewGameReader(nil)
	bot := NewDiscordBot(cfgMgr, gr)
	defer bot.Stop()

	// Add players
	p1 := gr.AddManualPlayer("Tanaka", "Red")
	p2 := gr.AddManualPlayer("Sato", "Blue")

	// Initially, confirmedDead is empty
	if bot.isConfirmedDead("Red") || bot.isConfirmedDead("Tanaka") {
		t.Fatalf("expected Red/Tanaka to not be confirmed dead initially")
	}

	// Tanaka dies during Tasks
	p1.IsDead = true
	// During StateTasks, Tanaka is in pseudo-death (仮死状態) and not yet confirmed dead
	if bot.isConfirmedDead("Red") || bot.isConfirmedDead("Tanaka") {
		t.Fatalf("expected Red/Tanaka to still NOT be confirmed dead during tasks")
	}

	// Discussion starts!
	bot.confirmDeadPlayers([]memory.PlayerInfo{p1, p2})
	if !bot.isConfirmedDead("Red") || !bot.isConfirmedDead("Tanaka") {
		t.Fatalf("expected Red/Tanaka to be confirmed dead in discussion")
	}
	if bot.isConfirmedDead("Blue") || bot.isConfirmedDead("Sato") {
		t.Fatalf("expected Blue/Sato to remain alive")
	}

	// Reset on Lobby
	bot.OnGameStateChanged(memory.StateDiscussion, memory.StateLobby)
	if bot.isConfirmedDead("Red") {
		t.Fatalf("expected confirmedDead to be reset in Lobby")
	}
}

func TestRealtimeStatusEmbed_PlayerColorsFormat(t *testing.T) {
	cfgMgr := config.NewConfigManager()
	_ = cfgMgr.Update(func(c *config.AppConfig) {
		c.PlayerMappings["Red"] = "user_red_123"
		c.PlayerMappings["Blue"] = "user_blue_456"
	})

	gr := memory.NewGameReader(nil)
	bot := NewDiscordBot(cfgMgr, gr)
	defer bot.Stop()

	gr.AddManualPlayer("Tanaka", "Red")
	gr.AddManualPlayer("Sato", "Blue")

	embed := bot.buildRealtimeStatusEmbed()
	if len(embed.Fields) == 0 {
		t.Fatalf("expected embed fields, got none")
	}

	// First field must be "プレイヤーカラー"
	field := embed.Fields[0]
	if field.Name != "プレイヤーカラー" {
		t.Errorf("expected field name 'プレイヤーカラー', got '%s'", field.Name)
	}

	// Must contain formatted string: カラーDiscord名（ゲーム内名）｜生存状態
	// e.g. 🔴 **Red** <@user_red_123>（Tanaka）｜ 🎮 待機
	if !strings.Contains(field.Value, "🔴 **Red** <@user_red_123>（Tanaka）｜ 🎮 待機") {
		t.Errorf("expected formatted string for Red, got: %s", field.Value)
	}
	if !strings.Contains(field.Value, "🔵 **Blue** <@user_blue_456>（Sato）｜ 🎮 待機") {
		t.Errorf("expected formatted string for Blue, got: %s", field.Value)
	}
}

func TestFindMatch_ExplicitOnly(t *testing.T) {
	mappings := map[string]string{
		"Red": "user_123",
	}

	players := []memory.PlayerInfo{
		{
			Name:  "Tanaka",
			Color: memory.ColorRed,
		},
		{
			Name:  "Sato",
			Color: memory.ColorBlue,
		},
	}

	// 1. Mapped user matches their mapped color
	matchedName, matchedColor := findMatch("user_123", "Tanaka", "Tanaka", mappings, players)
	if matchedName != "Red" || matchedColor != "#c51111" {
		t.Errorf("expected Red, got %s (%s)", matchedName, matchedColor)
	}

	// 2. Unmapped user whose nickname is "Sato" must NOT be auto-matched to Blue!
	unmappedName, unmappedColor := findMatch("user_unmapped_456", "Sato", "Sato", mappings, players)
	if unmappedName != "" || unmappedColor != "" {
		t.Errorf("expected unmapped user to return empty, got %s (%s)", unmappedName, unmappedColor)
	}
}
