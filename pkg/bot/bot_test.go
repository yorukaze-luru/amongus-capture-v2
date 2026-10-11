package bot

import (
	"testing"

	"amongus-capture-v2/pkg/config"
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
