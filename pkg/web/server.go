package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"amongus-capture-v2/pkg/bot"
	"amongus-capture-v2/pkg/config"
	"amongus-capture-v2/pkg/memory"
	"amongus-capture-v2/pkg/metrics"
	"amongus-capture-v2/pkg/version"
	"github.com/gorilla/websocket"
	"golang.org/x/sys/windows"
)

//go:embed static/*
var staticFS embed.FS

// WebServer provides the embedded dashboard UI and HTTP/WebSocket APIs
type WebServer struct {
	mu           sync.RWMutex
	port         int
	gr           *memory.GameReader
	discordBot   *bot.DiscordBot
	cfgMgr       *config.ConfigManager
	offsetMgr    *memory.OffsetManager
	clients                  map[*websocket.Conn]bool
	broadcast                chan []byte
	broadcastStateUpdateChan chan struct{}
	upgrader                 websocket.Upgrader
	hooked                   bool
	processName              string
	gameHash                 string
	voiceMembers             []bot.VoiceMember
	botConnected             bool
	botUsername              string
	botError                 string
}

// StatePayload represents current capture state sent to Web UI
type StatePayload struct {
	Hooked          bool                `json:"hooked"`
	ProcessName     string              `json:"processName"`
	GameHash        string              `json:"gameHash"`
	GameState       memory.GameState    `json:"gameState"`
	LobbyCode       string              `json:"lobbyCode"`
	Players         []memory.PlayerInfo `json:"players"`

	// Self-Host Bot State
	BotConnected   bool                `json:"botConnected"`
	BotUsername    string              `json:"botUsername"`
	BotError       string              `json:"botError"`
	BotToken       string              `json:"botToken"`
	GuildID        string              `json:"guildId"`
	VoiceCID       string              `json:"voiceCId"`
	StatusCID      string              `json:"statusCId"`
	VoiceMembers   []bot.VoiceMember   `json:"voiceMembers"`
	PlayerMappings map[string]string   `json:"playerMappings"`
	LastMatchResult *memory.MatchResult `json:"lastMatchResult"`
	Metrics         metrics.ProcessMetrics `json:"metrics"`
	Version         string              `json:"version"`
	HasUpdate       bool                `json:"hasUpdate"`
	LatestVersion   string              `json:"latestVersion"`
	ReleaseURL      string              `json:"releaseUrl"`
}

// WSMessage is the message envelope for UI WebSocket
type WSMessage struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload"`
}

// NewWebServer initializes the web dashboard server
func NewWebServer(port int, gr *memory.GameReader, db *bot.DiscordBot, cm *config.ConfigManager, om *memory.OffsetManager) *WebServer {
	ws := &WebServer{
		port:                     port,
		gr:                       gr,
		discordBot:               db,
		cfgMgr:                   cm,
		offsetMgr:                om,
		clients:                  make(map[*websocket.Conn]bool),
		broadcast:                make(chan []byte, 128),
		broadcastStateUpdateChan: make(chan struct{}, 1),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}

	if gr != nil {
		gr.AddListener(ws)
	}
	if db != nil {
		db.AddListener(ws)
	}

	return ws
}

// Start launches the HTTP and WebSocket dashboard server
func (ws *WebServer) Start(ctx context.Context) error {
	go ws.broadcastLoop(ctx)
	go ws.stateBroadcastWorker(ctx)

	mux := http.NewServeMux()

	// Static assets from embed.FS
	subFS, err := fs.Sub(staticFS, "static")
	if err != nil {
		return fmt.Errorf("failed to sub static fs: %w", err)
	}
	fileServer := http.FileServer(http.FS(subFS))
	mux.Handle("/", fileServer)

	// APIs
	mux.HandleFunc("/ws", ws.handleWS)
	mux.HandleFunc("/api/status", ws.handleStatus)
	mux.HandleFunc("/api/sync", ws.handleSync)
	mux.HandleFunc("/api/refresh-offsets", ws.handleRefreshOffsets)

	// Bot APIs
	mux.HandleFunc("/api/bot/start", ws.handleBotStart)
	mux.HandleFunc("/api/bot/stop", ws.handleBotStop)
	mux.HandleFunc("/api/bot/guilds", ws.handleBotGuilds)
	mux.HandleFunc("/api/bot/set-channel", ws.handleBotSetChannel)
	mux.HandleFunc("/api/bot/set-mapping", ws.handleBotSetMapping)
	mux.HandleFunc("/api/bot/unmute", ws.handleBotUnmute)
	mux.HandleFunc("/api/link/set", ws.handleLinkSet)
	mux.HandleFunc("/api/link/remove", ws.handleLinkRemove)
	mux.HandleFunc("/api/player/add", ws.handlePlayerAdd)
	mux.HandleFunc("/api/player/remove", ws.handlePlayerRemove)
	mux.HandleFunc("/api/config/save-token", ws.handleSaveToken)
	mux.HandleFunc("/api/app/quit", ws.handleAppQuit)

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", ws.port),
		Handler: mux,
	}

	log.Printf("[WebUI] Dedicated Client UI Server listening on http://localhost:%d", ws.port)

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (ws *WebServer) broadcastLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-ws.broadcast:
			ws.mu.Lock()
			for conn := range ws.clients {
				err := conn.WriteMessage(websocket.TextMessage, msg)
				if err != nil {
					conn.Close()
					delete(ws.clients, conn)
				}
			}
			ws.mu.Unlock()
		}
	}
}

func (ws *WebServer) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := ws.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	ws.mu.Lock()
	ws.clients[conn] = true
	ws.mu.Unlock()

	// Send initial state snapshot
	initPayload := ws.buildCurrentStatePayload()
	msg, _ := json.Marshal(WSMessage{
		Type:    "initial_state",
		Payload: initPayload,
	})
	_ = conn.WriteMessage(websocket.TextMessage, msg)

	// Keep alive read loop
	go func() {
		defer func() {
			ws.mu.Lock()
			delete(ws.clients, conn)
			ws.mu.Unlock()
			conn.Close()
		}()
		for {
			if _, _, err := conn.NextReader(); err != nil {
				break
			}
		}
	}()
}

func (ws *WebServer) buildCurrentStatePayload() StatePayload {
	ws.mu.RLock()
	hooked := ws.hooked
	procName := ws.processName
	gameHash := ws.gameHash
	vMembers := ws.voiceMembers
	botConn := ws.botConnected
	botUser := ws.botUsername
	botErr := ws.botError
	ws.mu.RUnlock()

	cfg := ws.cfgMgr.Get()

	var gameState memory.GameState = memory.StateUnknown
	var lobbyCode string
	var players []memory.PlayerInfo
	var lastMatch *memory.MatchResult
	if ws.gr != nil {
		gameState = ws.gr.GetCurrentState()
		lobbyCode = ws.gr.GetCurrentLobby()
		players = ws.gr.GetSnapshotPlayers()
		lastMatch = ws.gr.GetLastMatchResult()
	}

	upInfo := version.GetUpdateInfo()

	return StatePayload{
		Hooked:          hooked,
		ProcessName:     procName,
		GameHash:        gameHash,
		GameState:       gameState,
		LobbyCode:       lobbyCode,
		Players:         players,
		BotConnected:    botConn,
		BotUsername:     botUser,
		BotError:        botErr,
		BotToken:        cfg.DiscordBotToken,
		GuildID:         cfg.DiscordGuildID,
		VoiceCID:        cfg.DiscordVoiceCID,
		StatusCID:       cfg.DiscordStatusCID,
		VoiceMembers:    vMembers,
		PlayerMappings:  cfg.PlayerMappings,
		LastMatchResult: lastMatch,
		Metrics:         metrics.GetMonitor().Sample(),
		Version:         version.CurrentVersion,
		HasUpdate:       upInfo.HasUpdate,
		LatestVersion:   upInfo.LatestVersion,
		ReleaseURL:      upInfo.ReleaseURL,
	}
}

func (ws *WebServer) stateBroadcastWorker(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Periodically send state to refresh real-time resource metrics even when idle
			payload := ws.buildCurrentStatePayload()
			msg, err := json.Marshal(WSMessage{
				Type:    "state_update",
				Payload: payload,
			})
			if err == nil {
				select {
				case ws.broadcast <- msg:
				default:
				}
			}
		case <-ws.broadcastStateUpdateChan:
			// Coalesce rapid bursts within 30ms (prevents UI flicker)
			time.Sleep(30 * time.Millisecond)
			select {
			case <-ws.broadcastStateUpdateChan:
			default:
			}

			payload := ws.buildCurrentStatePayload()
			msg, err := json.Marshal(WSMessage{
				Type:    "state_update",
				Payload: payload,
			})
			if err == nil {
				select {
				case ws.broadcast <- msg:
				default:
				}
			}
		}
	}
}

func (ws *WebServer) BroadcastStateUpdate() {
	select {
	case ws.broadcastStateUpdateChan <- struct{}{}:
	default:
	}
}

func (ws *WebServer) BroadcastLog(message, level string) {
	msg, err := json.Marshal(WSMessage{
		Type: "log",
		Payload: map[string]string{
			"message": message,
			"level":   level,
		},
	})
	if err == nil {
		select {
		case ws.broadcast <- msg:
		default:
		}
	}
}

// API Handlers

func (ws *WebServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ws.buildCurrentStatePayload())
}

func (ws *WebServer) handleSync(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if ws.gr != nil {
		ws.gr.ForceTransmit()
	}
	if ws.discordBot != nil && ws.discordBot.IsConnected() {
		ws.discordBot.RefreshVoiceMembers()
		ws.discordBot.UpdateStatusEmbed()
	}
	ws.BroadcastStateUpdate()
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

func (ws *WebServer) handleRefreshOffsets(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var err error
	if ws.offsetMgr != nil {
		err = ws.offsetMgr.RefreshRemote()
	}
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

// Bot API Handlers

func (ws *WebServer) handleBotStart(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var req struct {
		Token     string `json:"token"`
		GuildID   string `json:"guildId"`
		VoiceCID  string `json:"voiceCId"`
		StatusCID string `json:"statusCId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	cfg := ws.cfgMgr.Get()
	token := strings.TrimSpace(req.Token)
	if token == "" {
		token = cfg.DiscordBotToken
	}
	guildID := strings.TrimSpace(req.GuildID)
	if guildID == "" {
		guildID = cfg.DiscordGuildID
	}
	voiceCID := strings.TrimSpace(req.VoiceCID)
	if voiceCID == "" {
		voiceCID = cfg.DiscordVoiceCID
	}
	statusCID := strings.TrimSpace(req.StatusCID)
	if statusCID == "" && req.GuildID == "" {
		statusCID = cfg.DiscordStatusCID
	}

	if token == "" {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Bot token is empty"})
		return
	}

	_ = ws.cfgMgr.Update(func(c *config.AppConfig) {
		c.DiscordBotToken = token
		c.DiscordGuildID = guildID
		c.DiscordVoiceCID = voiceCID
		c.DiscordStatusCID = statusCID
	})

	if ws.discordBot != nil {
		go func() {
			err := ws.discordBot.Start(context.Background(), token, guildID, voiceCID, statusCID)
			if err != nil {
				log.Printf("[WebServer] Bot start error: %v", err)
			}
		}()
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

func (ws *WebServer) handleBotStop(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if ws.discordBot != nil {
		ws.discordBot.Stop()
	}
	ws.BroadcastStateUpdate()
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

func (ws *WebServer) handleBotGuilds(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if ws.discordBot == nil || !ws.discordBot.IsConnected() {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Bot is not connected"})
		return
	}

	guilds, err := ws.discordBot.GetGuilds()
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "guilds": guilds})
}

func (ws *WebServer) handleBotSetChannel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var req struct {
		GuildID   string `json:"guildId"`
		VoiceCID  string `json:"voiceCId"`
		StatusCID string `json:"statusCId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	if ws.discordBot != nil {
		ws.discordBot.SetChannels(req.GuildID, req.VoiceCID, req.StatusCID)
	}
	ws.BroadcastStateUpdate()

	_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

func (ws *WebServer) handleBotSetMapping(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var req struct {
		Key    string `json:"key"`    // Player Name or Color
		UserID string `json:"userId"` // Discord User ID
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	_ = ws.cfgMgr.Update(func(c *config.AppConfig) {
		if req.UserID == "" {
			delete(c.PlayerMappings, req.Key)
		} else {
			c.PlayerMappings[req.Key] = req.UserID
		}
	})

	if ws.discordBot != nil {
		ws.discordBot.RefreshVoiceMembers()
	}

	ws.BroadcastStateUpdate()
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

func (ws *WebServer) handleBotUnmute(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var req struct {
		UserID string `json:"userId"`
		All    bool   `json:"all"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	if ws.discordBot != nil {
		if req.All || req.UserID == "" {
			ws.discordBot.UnmuteAll()
			ws.BroadcastLog("ボイスチャンネル参加者全員のミュートを解除しました", "success")
		} else {
			_ = ws.discordBot.UnmuteUser(req.UserID)
			ws.BroadcastLog(fmt.Sprintf("ユーザー <@%s> のミュートを解除しました", req.UserID), "success")
		}
		ws.discordBot.RefreshVoiceMembers()
	}

	ws.BroadcastStateUpdate()
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

func (ws *WebServer) handleLinkSet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var req struct {
		Color  string `json:"color"`
		UserID string `json:"userId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	req.Color = strings.TrimSpace(req.Color)
	req.UserID = strings.TrimSpace(req.UserID)
	if req.Color == "" || req.UserID == "" {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "color or userId is empty"})
		return
	}

	_ = ws.cfgMgr.Update(func(c *config.AppConfig) {
		// 1. Remove any previous mappings for this Discord User ID (prevent duplicate colors for same user)
		for k, uid := range c.PlayerMappings {
			if uid == req.UserID {
				delete(c.PlayerMappings, k)
			}
		}

		// 2. Remove any previous mappings for this color (prevent multiple users mapped to same color)
		delete(c.PlayerMappings, req.Color)

		// 3. Set new color mapping
		c.PlayerMappings[req.Color] = req.UserID

		// 4. If an in-game player with this color exists, link their player name too
		if ws.gr != nil {
			for _, p := range ws.gr.GetSnapshotPlayers() {
				if strings.EqualFold(p.Color.String(), req.Color) || strings.EqualFold(p.ColorHex, req.Color) {
					c.PlayerMappings[p.Name] = req.UserID
					break
				}
			}
		}
	})

	ws.BroadcastLog(fmt.Sprintf("Discord リンク更新: %s ↔ <@%s>", req.Color, req.UserID), "success")

	if ws.discordBot != nil {
		ws.discordBot.RefreshVoiceMembers()
		ws.discordBot.UpdateStatusEmbed()
	}

	ws.BroadcastStateUpdate()
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

func (ws *WebServer) handleLinkRemove(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var req struct {
		Key    string `json:"key"`
		UserID string `json:"userId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	_ = ws.cfgMgr.Update(func(c *config.AppConfig) {
		if req.Key != "" {
			delete(c.PlayerMappings, req.Key)
		}
		if req.UserID != "" {
			for k, uid := range c.PlayerMappings {
				if uid == req.UserID {
					delete(c.PlayerMappings, k)
				}
			}
		}
	})

	ws.BroadcastLog("リンクを解除しました", "info")

	if ws.discordBot != nil {
		ws.discordBot.RefreshVoiceMembers()
		ws.discordBot.UpdateStatusEmbed()
	}

	ws.BroadcastStateUpdate()
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

func (ws *WebServer) handlePlayerAdd(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var req struct {
		Name   string `json:"name"`
		Color  string `json:"color"`
		UserID string `json:"userId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	req.Color = strings.TrimSpace(req.Color)
	req.Name = strings.TrimSpace(req.Name)
	req.UserID = strings.TrimSpace(req.UserID)

	if req.Color == "" && req.Name == "" {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "color and name are empty"})
		return
	}

	if req.Name != "" && ws.gr != nil {
		p := ws.gr.AddManualPlayer(req.Name, req.Color)
		ws.BroadcastLog(fmt.Sprintf("プレイヤー手動追加: %s (%s)", p.Name, p.Color.String()), "success")
	}

	if req.UserID != "" {
		_ = ws.cfgMgr.Update(func(c *config.AppConfig) {
			// Deduplicate: clear old mappings for this user
			for k, uid := range c.PlayerMappings {
				if uid == req.UserID {
					delete(c.PlayerMappings, k)
				}
			}
			if req.Color != "" {
				delete(c.PlayerMappings, req.Color)
				c.PlayerMappings[req.Color] = req.UserID
			}
			if req.Name != "" {
				c.PlayerMappings[req.Name] = req.UserID
			}
		})
		ws.BroadcastLog(fmt.Sprintf("Discord リンク更新: %s ↔ <@%s>", req.Color, req.UserID), "success")
	}

	if ws.discordBot != nil {
		ws.discordBot.RefreshVoiceMembers()
		ws.discordBot.UpdateStatusEmbed()
	}

	ws.BroadcastStateUpdate()
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

func (ws *WebServer) handlePlayerRemove(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	if ws.gr != nil && req.Name != "" {
		ws.gr.RemovePlayer(req.Name)
		ws.BroadcastLog(fmt.Sprintf("プレイヤー削除: %s", req.Name), "warn")
	}

	if ws.discordBot != nil {
		ws.discordBot.RefreshVoiceMembers()
		ws.discordBot.UpdateStatusEmbed()
	}

	ws.BroadcastStateUpdate()
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

// Listener Callbacks

func (ws *WebServer) OnGameStateChanged(oldState, newState memory.GameState) {
	ws.BroadcastStateUpdate()
	ws.BroadcastLog(fmt.Sprintf("ゲーム状態変化: %s → %s", oldState.String(), newState.String()), "info")
}

func (ws *WebServer) OnPlayerChanged(event memory.PlayerChangeEvent) {
	ws.BroadcastStateUpdate()
	ws.BroadcastLog(fmt.Sprintf("プレイヤー変化: [%s] %s (Dead: %v, Disc: %v)",
		event.Action.String(), event.Name, event.IsDead, event.Disconnected), "info")
}

func (ws *WebServer) OnLobbyUpdated(event memory.LobbyEvent) {
	ws.BroadcastStateUpdate()
	ws.BroadcastLog(fmt.Sprintf("ロビー更新: コード=%s リージョン=%d マップ=%d",
		event.LobbyCode, event.Region, event.PlayMap), "success")
}

func (ws *WebServer) OnGameOver(event memory.GameOverEvent) {
	ws.BroadcastStateUpdate()
	ws.BroadcastLog(fmt.Sprintf("ゲーム終了: %s 【%s】",
		event.MatchResult.WinningTitle, event.MatchResult.ReasonText), "warn")
}

func (ws *WebServer) OnHookStatusChanged(hooked bool, processName string, gameHash string) {
	ws.mu.Lock()
	ws.hooked = hooked
	ws.processName = processName
	ws.gameHash = gameHash
	ws.mu.Unlock()

	ws.BroadcastStateUpdate()
	if hooked {
		ws.BroadcastLog(fmt.Sprintf("Among Us プロセスに接続しました (%s, Hash: %s)", processName, gameHash), "success")
	} else {
		ws.BroadcastLog("Among Us プロセスが切断されました", "warn")
	}
}

func (ws *WebServer) OnBotStatusChanged(connected bool, username string, errStr string) {
	ws.mu.Lock()
	ws.botConnected = connected
	ws.botUsername = username
	ws.botError = errStr
	ws.mu.Unlock()

	ws.BroadcastStateUpdate()
}

func (ws *WebServer) OnVoiceMembersUpdated(members []bot.VoiceMember) {
	ws.mu.Lock()
	ws.voiceMembers = members
	ws.mu.Unlock()

	ws.BroadcastStateUpdate()
}

func (ws *WebServer) OnBotLog(message, level string) {
	ws.BroadcastLog(message, level)
}

func (ws *WebServer) handleSaveToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	token := strings.TrimSpace(req.Token)
	_ = ws.cfgMgr.Update(func(c *config.AppConfig) {
		c.DiscordBotToken = token
	})
	_ = ws.cfgMgr.Save()
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

func (ws *WebServer) handleAppQuit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
	go func() {
		time.Sleep(50 * time.Millisecond)
		if ws.gr != nil {
			ws.gr.Stop()
		}
		if ws.discordBot != nil {
			ws.discordBot.Stop()
		}
		if runtime.GOOS == "windows" {
			windows.ExitProcess(0)
		}
		os.Exit(0)
	}()
}

