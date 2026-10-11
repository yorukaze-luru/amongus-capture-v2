package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

const CurrentConfigVersion = 2

// AppConfig represents persistent application settings
type AppConfig struct {
	ConfigVersion   int               `json:"config_version"`
	WebUIPort       int               `json:"webui_port"`
	OffsetsURL      string            `json:"offsets_url"`
	PollIntervalMs  int               `json:"poll_interval_ms"`
	Debug           bool              `json:"debug"`
	DarkMode        bool              `json:"dark_mode"`

	// Window Placement & Display Settings (Supports Multi-Monitors & Fullscreen)
	WindowX         int               `json:"window_x"`
	WindowY         int               `json:"window_y"`
	WindowWidth     int               `json:"window_width"`
	WindowHeight    int               `json:"window_height"`
	WindowMaximized bool              `json:"window_maximized"`

	// Self-Hosted Discord Bot Settings
	DiscordBotToken string            `json:"discord_bot_token"`
	DiscordGuildID  string            `json:"discord_guild_id"`
	DiscordVoiceCID string            `json:"discord_voice_channel_id"`
	DiscordStatusCID string           `json:"discord_status_channel_id"`
	PlayerMappings  map[string]string `json:"player_mappings"` // PlayerName or Color -> DiscordUserID
}

// ConfigManager handles loading, saving, and version migrations
type ConfigManager struct {
	mu       sync.RWMutex
	filePath string
	config   AppConfig
}

// DefaultConfig returns default application settings
func DefaultConfig() AppConfig {
	return AppConfig{
		ConfigVersion:    CurrentConfigVersion,
		WebUIPort:        8123,
		OffsetsURL:       "https://raw.githubusercontent.com/automuteus/amonguscapture/master/Offsets.json",
		PollIntervalMs:   150,
		Debug:            false,
		DarkMode:         true,
		WindowX:          -1,
		WindowY:          -1,
		WindowWidth:      820,
		WindowHeight:     640,
		WindowMaximized:  false,
		DiscordBotToken:  "",
		DiscordGuildID:   "",
		DiscordVoiceCID:  "",
		DiscordStatusCID: "",
		PlayerMappings:   make(map[string]string),
	}
}

// CleanLegacyStorage clears previously stored cache and legacy configs if requested
func CleanLegacyStorage() {
	appData := os.Getenv("APPDATA")
	if appData != "" {
		dir := filepath.Join(appData, "AmongUsCaptureV2")
		_ = os.Remove(filepath.Join(dir, "settings.json"))
		_ = os.Remove(filepath.Join(dir, "offsets_cache.json"))
		_ = os.Remove(filepath.Join(dir, "offsets_custom.json"))
	}
}

// NewConfigManager initializes and loads the config
func NewConfigManager() *ConfigManager {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		appData = "."
	}
	dir := filepath.Join(appData, "AmongUsCaptureV2")
	_ = os.MkdirAll(dir, 0755)

	cm := &ConfigManager{
		filePath: filepath.Join(dir, "settings.json"),
		config:   DefaultConfig(),
	}

	cm.Load()
	return cm
}

// Load reads config from disk and applies automatic migrations
func (cm *ConfigManager) Load() {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	data, err := os.ReadFile(cm.filePath)
	if err != nil {
		return
	}

	var loaded AppConfig
	if err := json.Unmarshal(data, &loaded); err == nil {
		// Migrate older versions if needed
		if loaded.ConfigVersion < CurrentConfigVersion {
			loaded.ConfigVersion = CurrentConfigVersion
			if loaded.WindowWidth < 400 {
				loaded.WindowWidth = 820
			}
			if loaded.WindowHeight < 300 {
				loaded.WindowHeight = 640
			}
		}
		if loaded.PlayerMappings == nil {
			loaded.PlayerMappings = make(map[string]string)
		}
		cm.config = loaded
	}
}

// Save persists config to disk
func (cm *ConfigManager) Save() error {
	cm.mu.RLock()
	data, err := json.MarshalIndent(cm.config, "", "  ")
	cm.mu.RUnlock()

	if err != nil {
		return err
	}
	return os.WriteFile(cm.filePath, data, 0644)
}

// Get returns a copy of current config
func (cm *ConfigManager) Get() AppConfig {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.config
}

// Update updates the configuration and saves it
func (cm *ConfigManager) Update(updater func(c *AppConfig)) error {
	cm.mu.Lock()
	updater(&cm.config)
	cm.mu.Unlock()
	return cm.Save()
}
