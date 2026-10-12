package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"amongus-capture-v2/pkg/config"
	"amongus-capture-v2/pkg/memory"
	"github.com/bwmarrin/discordgo"
)

// BotListener notifies listeners of bot events
type BotListener interface {
	OnBotStatusChanged(connected bool, username string, errStr string)
	OnVoiceMembersUpdated(members []VoiceMember)
	OnBotLog(message string, level string)
}

// VoiceMember represents a Discord user in the tracked voice channel
type VoiceMember struct {
	UserID       string `json:"userId"`
	Username     string `json:"username"`
	DisplayName  string `json:"displayName"`
	AvatarURL    string `json:"avatarUrl"`
	IsMuted      bool   `json:"isMuted"`
	IsDeafened   bool   `json:"isDeafened"`
	MatchedName  string `json:"matchedName"`
	MatchedColor string `json:"matchedColor"`
}

// ChannelInfo represents a Discord Voice or Text Channel
type ChannelInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// GuildInfo represents a Discord Server with voice and text channels
type GuildInfo struct {
	ID   string        `json:"id"`
	Name string        `json:"name"`
	VCs  []ChannelInfo `json:"vcs"`
	TCs  []ChannelInfo `json:"tcs"`
}

// DiscordBot represents the self-hosted Discord Bot engine
type DiscordBot struct {
	mu           sync.RWMutex
	session      *discordgo.Session
	token        string
	guildID      string
	voiceCID     string
	statusCID    string
	statusMsgID  string
	statusMu     sync.Mutex
	connected    bool
	botUser      *discordgo.User
	listeners    []BotListener
	cfgMgr       *config.ConfigManager
	gr           *memory.GameReader

	// Active VC state
	voiceMembers    map[string]*discordgo.VoiceState // UserID -> VoiceState
	embedUpdateChan chan struct{}
	stopEmbedWorker chan struct{}
}

// NewDiscordBot creates a new self-hosted bot instance
func NewDiscordBot(cfgMgr *config.ConfigManager, gr *memory.GameReader) *DiscordBot {
	bot := &DiscordBot{
		cfgMgr:          cfgMgr,
		gr:              gr,
		voiceMembers:    make(map[string]*discordgo.VoiceState),
		embedUpdateChan: make(chan struct{}, 1),
		stopEmbedWorker: make(chan struct{}),
	}

	go bot.embedUpdateWorker()

	if gr != nil {
		gr.AddListener(bot)
	}

	return bot
}

// AddListener registers a bot event listener
func (b *DiscordBot) AddListener(l BotListener) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.listeners = append(b.listeners, l)
}

// Start launches the Discord bot with given token and connects to Gateway
func (b *DiscordBot) Start(ctx context.Context, token, guildID, voiceCID, statusCID string) error {
	b.DeleteStatusEmbed()

	b.mu.Lock()
	if b.connected && b.session != nil {
		_ = b.session.Close()
		b.connected = false
	}

	b.token = strings.TrimSpace(token)
	b.guildID = strings.TrimSpace(guildID)
	b.voiceCID = strings.TrimSpace(voiceCID)
	b.statusCID = strings.TrimSpace(statusCID)
	b.statusMsgID = ""
	b.mu.Unlock()

	if b.token == "" {
		return fmt.Errorf("bot token is empty")
	}

	// Format token with "Bot " prefix if missing
	formattedToken := b.token
	if !strings.HasPrefix(formattedToken, "Bot ") {
		formattedToken = "Bot " + formattedToken
	}

	dg, err := discordgo.New(formattedToken)
	if err != nil {
		b.notifyStatus(false, "", err.Error())
		return fmt.Errorf("failed to create discord session: %w", err)
	}

	// Setup Intents: Guilds, GuildMembers, GuildVoiceStates, GuildMessages
	dg.Identify.Intents = discordgo.IntentsGuilds |
		discordgo.IntentsGuildMembers |
		discordgo.IntentsGuildVoiceStates |
		discordgo.IntentsGuildMessages

	// Event Handlers
	dg.AddHandler(b.handleVoiceStateUpdate)
	dg.AddHandler(b.handleReady)
	dg.AddHandler(b.handleInteractionCreate)

	err = dg.Open()
	if err != nil {
		errStr := err.Error()
		b.notifyStatus(false, "", errStr)

		// Check for 4014 Disallowed Intents error
		if strings.Contains(errStr, "4014") || strings.Contains(strings.ToLower(errStr), "disallowed intent") || strings.Contains(strings.ToLower(errStr), "privileged") {
			b.notifyLog("⚠️ 【エラー】Discord Developer Portal で Privileged Gateway Intents (Server Members Intent) が無効になっています！\nDiscord Developer Portal の Bot 設定ページから「Server Members Intent」を有効化（ON）してください。", "error")
		} else {
			b.notifyLog(fmt.Sprintf("Discord Bot 接続エラー: %s", errStr), "error")
		}

		return fmt.Errorf("failed to connect to discord gateway: %w", err)
	}

	b.mu.Lock()
	b.session = dg
	b.connected = true
	b.botUser = dg.State.User
	b.mu.Unlock()

	username := "Discord Bot"
	if dg.State.User != nil {
		username = dg.State.User.Username
	}

	b.notifyStatus(true, username, "")
	b.notifyLog(fmt.Sprintf("Discord Bot (%s) がオンラインになりました！", username), "success")

	// Initial Voice channel refresh, Slash command cleanup/sync, and Status Embed post
	go func() {
		time.Sleep(500 * time.Millisecond)
		b.cleanAndSyncSlashCommands(dg, guildID)
		b.RefreshVoiceMembers()
		b.UpdateStatusEmbed()
	}()

	return nil
}

// DeleteStatusEmbed deletes the status embed from Discord channel if it exists
func (b *DiscordBot) DeleteStatusEmbed() {
	b.statusMu.Lock()
	defer b.statusMu.Unlock()

	b.mu.Lock()
	s := b.session
	statusCID := b.statusCID
	statusMsgID := b.statusMsgID
	b.statusMsgID = ""
	b.mu.Unlock()

	if s != nil && statusCID != "" && statusMsgID != "" {
		_ = s.ChannelMessageDelete(statusCID, statusMsgID)
		log.Printf("[DiscordBot] Deleted status embed message %s from channel %s", statusMsgID, statusCID)
	}
}

// Stop terminates the Discord session
func (b *DiscordBot) Stop() {
	b.DeleteStatusEmbed()

	b.mu.Lock()
	if b.session != nil {
		_ = b.session.Close()
		b.session = nil
	}
	b.connected = false
	b.botUser = nil
	b.statusMsgID = ""
	b.voiceMembers = make(map[string]*discordgo.VoiceState)
	b.mu.Unlock()

	b.notifyStatus(false, "", "")
	b.notifyVoiceMembers(nil)
	b.notifyLog("Discord Bot がオフラインになりました", "warn")
}

// IsConnected returns if the bot is currently connected
func (b *DiscordBot) IsConnected() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.connected
}

func (b *DiscordBot) handleReady(s *discordgo.Session, r *discordgo.Ready) {
	b.mu.Lock()
	b.botUser = r.User
	b.mu.Unlock()
	log.Printf("[DiscordBot] Ready as %s#%s (ID: %s)", r.User.Username, r.User.Discriminator, r.User.ID)
	b.notifyStatus(true, r.User.Username, "")
}

func (b *DiscordBot) handleVoiceStateUpdate(s *discordgo.Session, v *discordgo.VoiceStateUpdate) {
	b.mu.Lock()
	guildID := b.guildID
	voiceCID := b.voiceCID
	b.mu.Unlock()

	if v.GuildID != guildID {
		return
	}

	// Update cached voice state
	b.mu.Lock()
	if v.ChannelID == voiceCID && voiceCID != "" {
		b.voiceMembers[v.UserID] = v.VoiceState
	} else {
		delete(b.voiceMembers, v.UserID)
	}
	b.mu.Unlock()

	b.RefreshVoiceMembers()
	b.UpdateStatusEmbed()
}

// GetGuilds returns list of guilds the bot is in along with their voice and text channels
func (b *DiscordBot) GetGuilds() ([]GuildInfo, error) {
	b.mu.RLock()
	s := b.session
	b.mu.RUnlock()

	if s == nil {
		return nil, fmt.Errorf("bot not connected")
	}

	guilds, err := s.UserGuilds(100, "", "", false)
	if err != nil {
		return nil, err
	}

	var res []GuildInfo
	for _, g := range guilds {
		channels, err := s.GuildChannels(g.ID)
		var vcs []ChannelInfo
		var tcs []ChannelInfo
		if err == nil {
			for _, ch := range channels {
				if ch.Type == discordgo.ChannelTypeGuildVoice {
					vcs = append(vcs, ChannelInfo{
						ID:   ch.ID,
						Name: ch.Name,
					})
				} else if ch.Type == discordgo.ChannelTypeGuildText {
					tcs = append(tcs, ChannelInfo{
						ID:   ch.ID,
						Name: ch.Name,
					})
				}
			}
		}
		res = append(res, GuildInfo{
			ID:   g.ID,
			Name: g.Name,
			VCs:  vcs,
			TCs:  tcs,
		})
	}
	return res, nil
}

// SetChannels updates the target guild, voice channel, and status text channel
func (b *DiscordBot) SetChannels(guildID, voiceCID, statusCID string) {
	newGuildID := strings.TrimSpace(guildID)
	newVoiceCID := strings.TrimSpace(voiceCID)
	newStatusCID := strings.TrimSpace(statusCID)

	b.statusMu.Lock()
	b.mu.Lock()
	s := b.session
	oldStatusCID := b.statusCID
	oldStatusMsgID := b.statusMsgID

	b.guildID = newGuildID
	b.voiceCID = newVoiceCID

	channelChanged := (oldStatusCID != newStatusCID)
	if channelChanged {
		b.statusCID = newStatusCID
		b.statusMsgID = "" // Reset message ID when channel changes
	}
	b.mu.Unlock()

	// If status channel changed and we had an active status message in the old channel, delete it
	if channelChanged && s != nil && oldStatusCID != "" && oldStatusMsgID != "" {
		err := s.ChannelMessageDelete(oldStatusCID, oldStatusMsgID)
		if err != nil {
			log.Printf("[DiscordBot] Failed to delete status embed %s from channel %s: %v", oldStatusMsgID, oldStatusCID, err)
		} else {
			log.Printf("[DiscordBot] Deleted status embed %s from old channel %s (changed to %q)", oldStatusMsgID, oldStatusCID, newStatusCID)
		}
	}
	b.statusMu.Unlock()

	_ = b.cfgMgr.Update(func(c *config.AppConfig) {
		c.DiscordGuildID = newGuildID
		c.DiscordVoiceCID = newVoiceCID
		c.DiscordStatusCID = newStatusCID
	})

	b.RefreshVoiceMembers()
	if newStatusCID != "" {
		b.UpdateStatusEmbed()
	}
	go b.cleanAndSyncSlashCommands(s, newGuildID)
}

// RefreshVoiceMembers queries voice channel members and dispatches to listeners
func (b *DiscordBot) RefreshVoiceMembers() {
	b.mu.RLock()
	s := b.session
	guildID := b.guildID
	voiceCID := b.voiceCID
	b.mu.RUnlock()

	if s == nil || guildID == "" || voiceCID == "" {
		return
	}

	guild, err := s.State.Guild(guildID)
	if err != nil {
		// Try fetching from API if not in state cache
		guild, err = s.Guild(guildID)
		if err != nil {
			return
		}
	}

	var membersInVC []VoiceMember
	cfg := b.cfgMgr.Get()
	var inGamePlayers []memory.PlayerInfo
	if b.gr != nil {
		inGamePlayers = b.gr.GetSnapshotPlayers()
	}

	for _, vs := range guild.VoiceStates {
		if vs.ChannelID == voiceCID {
			var member *discordgo.Member
			if vs.Member != nil {
				member = vs.Member
			} else if m, err := s.State.Member(guildID, vs.UserID); err == nil && m != nil {
				member = m
			} else {
				member, _ = s.GuildMember(guildID, vs.UserID)
			}

			displayName := vs.UserID
			username := vs.UserID
			avatarURL := ""
			if member != nil && member.User != nil {
				username = member.User.Username
				displayName = member.DisplayName()
				avatarURL = member.User.AvatarURL("64")
			}

			// Match with Among Us players
			matchedName, matchedColor := findMatch(vs.UserID, username, displayName, cfg.PlayerMappings, inGamePlayers)

			membersInVC = append(membersInVC, VoiceMember{
				UserID:       vs.UserID,
				Username:     username,
				DisplayName:  displayName,
				AvatarURL:    avatarURL,
				IsMuted:      vs.Mute,
				IsDeafened:   vs.Deaf,
				MatchedName:  matchedName,
				MatchedColor: matchedColor,
			})
		}
	}

	b.notifyVoiceMembers(membersInVC)
}

func findMatch(userID, username, displayName string, mappings map[string]string, inGamePlayers []memory.PlayerInfo) (string, string) {
	// 1. Check explicit manual mapping (by color or name)
	for key, mappedUID := range mappings {
		if mappedUID == userID {
			cleanKey := strings.ToLower(strings.TrimSpace(key))
			for _, p := range inGamePlayers {
				if strings.ToLower(p.Name) == cleanKey || strings.ToLower(p.Color.String()) == cleanKey {
					return p.Color.String(), p.Color.Hex()
				}
			}
			pColor := memory.ParseColorByName(key)
			if pColor != memory.ColorUnknown {
				return pColor.String(), pColor.Hex()
			}
			return key, "#888888"
		}
	}

	// 2. Auto-match by name / nickname similarity
	cleanDisplay := strings.ToLower(strings.TrimSpace(displayName))
	cleanUser := strings.ToLower(strings.TrimSpace(username))

	for _, p := range inGamePlayers {
		cleanGame := strings.ToLower(strings.TrimSpace(p.Name))
		if cleanGame == "" {
			continue
		}
		if cleanGame == cleanDisplay || cleanGame == cleanUser ||
			strings.Contains(cleanDisplay, cleanGame) || strings.Contains(cleanGame, cleanDisplay) {
			return p.Color.String(), p.Color.Hex()
		}
	}

	return "", ""
}

// UpdateUserMuteState modifies a user's Server Mute and Server Deaf state in Discord
func (b *DiscordBot) UpdateUserMuteState(userID string, mute, deaf bool) error {
	b.mu.RLock()
	s := b.session
	guildID := b.guildID
	b.mu.RUnlock()

	if s == nil || guildID == "" || userID == "" {
		return fmt.Errorf("bot not ready")
	}

	data := struct {
		Mute bool `json:"mute"`
		Deaf bool `json:"deaf"`
	}{
		Mute: mute,
		Deaf: deaf,
	}

	_, err := s.RequestWithBucketID("PATCH", discordgo.EndpointGuildMember(guildID, userID), data, discordgo.EndpointGuildMember(guildID, ""))
	return err
}

// UnmuteUser removes server mute and server deafen from a single user
func (b *DiscordBot) UnmuteUser(userID string) error {
	return b.UpdateUserMuteState(userID, false, false)
}

// UnmuteAll removes server mute and server deafen from all users in the active voice channel
func (b *DiscordBot) UnmuteAll() {
	b.mu.RLock()
	s := b.session
	guildID := b.guildID
	voiceCID := b.voiceCID
	b.mu.RUnlock()

	if s == nil || guildID == "" || voiceCID == "" {
		return
	}

	guild, err := s.State.Guild(guildID)
	if err != nil {
		guild, err = s.Guild(guildID)
		if err != nil {
			return
		}
	}

	for _, vs := range guild.VoiceStates {
		if vs.ChannelID == voiceCID {
			go func(uid string) {
				_ = b.UpdateUserMuteState(uid, false, false)
			}(vs.UserID)
		}
	}
}

// ApplyGameStateToVoice automates muting / unmuting based on Among Us game state
func (b *DiscordBot) ApplyGameStateToVoice(state memory.GameState, players []memory.PlayerInfo) {
	b.mu.RLock()
	s := b.session
	guildID := b.guildID
	voiceCID := b.voiceCID
	b.mu.RUnlock()

	if s == nil || guildID == "" || voiceCID == "" {
		return
	}

	cfg := b.cfgMgr.Get()
	guild, err := s.State.Guild(guildID)
	if err != nil {
		guild, err = s.Guild(guildID)
		if err != nil {
			return
		}
	}

	// Build map of Game player status
	playerStatusMap := make(map[string]memory.PlayerInfo)
	for _, p := range players {
		if p.Name != "" {
			playerStatusMap[strings.ToLower(p.Name)] = p
		}
		playerStatusMap[strings.ToLower(p.Color.String())] = p
	}

	var wg sync.WaitGroup

	for _, vs := range guild.VoiceStates {
		if vs.ChannelID != voiceCID {
			continue
		}

		userID := vs.UserID
		var member *discordgo.Member
		if vs.Member != nil {
			member = vs.Member
		} else if m, err := s.State.Member(guildID, userID); err == nil && m != nil {
			member = m
		}
		displayName := userID
		username := userID
		if member != nil && member.User != nil {
			displayName = member.DisplayName()
			username = member.User.Username
		}

		// Find which in-game player this user corresponds to
		matchedName, _ := findMatch(userID, username, displayName, cfg.PlayerMappings, players)

		var targetMute, targetDeaf bool

		// If user is NOT participating in the game (unmatched spectator/listener):
		// NEVER mute them! Keep them unmuted and undeafened at all times.
		if matchedName == "" {
			targetMute = false
			targetDeaf = false
		} else {
			// Determine if this in-game player is dead or disconnected
			isDead := false
			if p, ok := playerStatusMap[strings.ToLower(matchedName)]; ok {
				isDead = p.IsDead || p.Disconnected
			}

			switch state {
			case memory.StateMenu, memory.StateLobby, memory.StateEnded:
				// Lobby / Menu / GameOver: Everyone unmuted & undeafened
				targetMute = false
				targetDeaf = false

			case memory.StateTasks:
				// Tasks:
				// Alive players: Muted & Deafened (cannot speak, cannot hear ghosts)
				// Dead players (Ghosts): Unmuted & Undeafened (can talk freely with other ghosts!)
				if isDead {
					targetMute = false
					targetDeaf = false
				} else {
					targetMute = true
					targetDeaf = true
				}

			case memory.StateDiscussion:
				// Discussion / Meeting:
				// Alive players: Unmuted & Undeafened (can discuss)
				// Dead players: Muted (cannot interrupt discussion, but can listen)
				if isDead {
					targetMute = true
					targetDeaf = false
				} else {
					targetMute = false
					targetDeaf = false
				}

			default:
				// Unknown / Default: don't alter
				continue
			}
		}

		// Only send API request if state differs to save rate limits
		if vs.Mute != targetMute || vs.Deaf != targetDeaf {
			wg.Add(1)
			go func(uid string, m, d bool) {
				defer wg.Done()
				err := b.UpdateUserMuteState(uid, m, d)
				if err != nil {
					log.Printf("[DiscordBot] Failed to update user %s (Mute: %v, Deaf: %v): %v", uid, m, d, err)
				}
			}(userID, targetMute, targetDeaf)
		}
	}

	wg.Wait()
}

func (b *DiscordBot) embedUpdateWorker() {
	for {
		select {
		case <-b.stopEmbedWorker:
			return
		case <-b.embedUpdateChan:
			// Coalesce rapid bursts with a 250ms debounce
			time.Sleep(250 * time.Millisecond)
			// Drain any extra triggers queued during sleep
			select {
			case <-b.embedUpdateChan:
			default:
			}
			b.executeUpdateStatusEmbed()
		}
	}
}

// UpdateStatusEmbed queues a debounced update of the status embed (prevents 429 rate limit)
func (b *DiscordBot) UpdateStatusEmbed() {
	select {
	case b.embedUpdateChan <- struct{}{}:
	default:
	}
}

func (b *DiscordBot) executeUpdateStatusEmbed() {
	b.statusMu.Lock()
	defer b.statusMu.Unlock()

	b.mu.RLock()
	s := b.session
	statusCID := b.statusCID
	statusMsgID := b.statusMsgID
	b.mu.RUnlock()

	if s == nil || statusCID == "" {
		return
	}

	embed := b.buildRealtimeStatusEmbed()
	components := b.buildColorSelectComponents()

	// If we already have a message ID, attempt to edit it with updated embed and select menu
	if statusMsgID != "" {
		edit := &discordgo.MessageEdit{
			Channel:    statusCID,
			ID:         statusMsgID,
			Embeds:     &[]*discordgo.MessageEmbed{embed},
			Components: &components,
		}
		_, err := s.ChannelMessageEditComplex(edit)
		if err == nil {
			return
		}
		// If editing failed (e.g. message was deleted), fallback to sending a new message
		log.Printf("[DiscordBot] Failed to edit status message %s: %v. Sending new message...", statusMsgID, err)
	}

	// Send new status message with interactive color select menu
	msg, err := s.ChannelMessageSendComplex(statusCID, &discordgo.MessageSend{
		Embeds:     []*discordgo.MessageEmbed{embed},
		Components: components,
	})
	if err != nil {
		log.Printf("[DiscordBot] Failed to send status embed to channel %s: %v", statusCID, err)
		return
	}

	b.mu.Lock()
	b.statusMsgID = msg.ID
	b.mu.Unlock()
}

func (b *DiscordBot) buildRealtimeStatusEmbed() *discordgo.MessageEmbed {
	var stateTitle = "⚪ 待機中 / 不明"
	var colorInt = 0x6B7280 // Gray
	var players []memory.PlayerInfo
	var gameState memory.GameState = memory.StateUnknown

	if b.gr != nil {
		gameState = b.gr.GetCurrentState()
		players = b.gr.GetSnapshotPlayers()
	}

	switch gameState {
	case memory.StateLobby:
		stateTitle = "🟢 ロビー待機中 (LOBBY)"
		colorInt = 0x3B82F6 // Blue
	case memory.StateTasks:
		stateTitle = "⚡ タスク進行中 (TASKS)"
		colorInt = 0x10B981 // Green
	case memory.StateDiscussion:
		stateTitle = "📢 緊急会議・議論中 (DISCUSSION)"
		colorInt = 0xEF4444 // Red
	case memory.StateEnded:
		stateTitle = "🏆 ゲーム終了 (ENDED)"
		colorInt = 0x8B5CF6 // Purple
	case memory.StateMenu:
		stateTitle = "🏠 メニュー画面 (MENU)"
		colorInt = 0x6B7280 // Gray
	default:
		stateTitle = "⚪ 待機中 / 不明"
		colorInt = 0x6B7280
	}

	// Calculate Alive & Dead players
	playerStatusMap := make(map[string]memory.PlayerInfo)
	aliveCount := 0
	deadCount := 0
	for _, p := range players {
		if p.Name != "" {
			playerStatusMap[strings.ToLower(p.Name)] = p
		}
		playerStatusMap[strings.ToLower(p.Color.String())] = p

		if p.IsDead || p.Disconnected {
			deadCount++
		} else {
			aliveCount++
		}
	}

	cfg := b.cfgMgr.Get()

	// Build VC Members lookup
	b.mu.RLock()
	s := b.session
	guildID := b.guildID
	voiceCID := b.voiceCID
	b.mu.RUnlock()

	var vcMembers []VoiceMember
	if s != nil && guildID != "" && voiceCID != "" {
		if guild, err := s.State.Guild(guildID); err == nil {
			for _, vs := range guild.VoiceStates {
				if vs.ChannelID == voiceCID {
					displayName := vs.UserID
					if vs.Member != nil {
						displayName = vs.Member.DisplayName()
					} else if m, err := s.State.Member(guildID, vs.UserID); err == nil && m != nil {
						displayName = m.DisplayName()
					}
					vcMembers = append(vcMembers, VoiceMember{
						UserID:      vs.UserID,
						DisplayName: displayName,
					})
				}
			}
		}
	}

	// Description with Large Status & Counts
	descLines := []string{
		fmt.Sprintf("## %s", stateTitle),
	}
	if len(players) > 0 {
		descLines = append(descLines, fmt.Sprintf("🟢 生存: **%d** 名 ｜ 💀 幽霊: **%d** 名 *(計: %d 名)*", aliveCount, deadCount, len(players)))
	} else if len(cfg.PlayerMappings) > 0 {
		descLines = append(descLines, fmt.Sprintf("🔗 参加登録: **%d** 名 *(ゲーム待機中)*", len(cfg.PlayerMappings)))
	} else {
		descLines = append(descLines, "*(ゲーム起動・ロビー待機中)*")
	}

	// 2. VC Users & Colors Mapping lines
	var linkLines []string
	linkedUserIDs := make(map[string]bool)

	for key, uid := range cfg.PlayerMappings {
		linkedUserIDs[uid] = true
		pColor := memory.ParseColorByName(key)
		emoji := pColor.Emoji()
		colorName := key
		if pColor != memory.ColorUnknown {
			colorName = pColor.String()
		}

		// Find Discord display name
		userName := ""
		for _, vm := range vcMembers {
			if vm.UserID == uid {
				userName = vm.DisplayName
				break
			}
		}

		// Player game status
		statusBadge := "🟢 生存"
		if gameState == memory.StateLobby || gameState == memory.StateMenu {
			statusBadge = "🎮 待機"
		} else if p, ok := playerStatusMap[strings.ToLower(key)]; ok {
			if p.IsDead {
				statusBadge = "💀 幽霊"
			} else if p.Disconnected {
				statusBadge = "🔌 切断"
			} else {
				statusBadge = "🟢 生存"
			}
		}

		userDisplay := fmt.Sprintf("<@%s>", uid)
		if userName != "" {
			userDisplay = fmt.Sprintf("<@%s> (%s)", uid, userName)
		}

		linkLines = append(linkLines, fmt.Sprintf("%s **%s** ➔ %s ｜ %s", emoji, colorName, userDisplay, statusBadge))
	}

	mappingText := "*(登録されているリンクはありません。WEBパネルから色を設定してください)*"
	if len(linkLines) > 0 {
		mappingText = strings.Join(linkLines, "\n")
	}

	// 3. Spectators / Unlinked VC Members
	var unlinkedLines []string
	for _, vm := range vcMembers {
		if !linkedUserIDs[vm.UserID] {
			unlinkedLines = append(unlinkedLines, fmt.Sprintf("<@%s>", vm.UserID))
		}
	}

	embed := &discordgo.MessageEmbed{
		Title:       "🚀 Among Us • リアルタイム ステータス",
		Description: strings.Join(descLines, "\n"),
		Color:       colorInt,
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:   "🎨 プレイヤー & カラー紐付け",
				Value:  mappingText,
				Inline: false,
			},
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: fmt.Sprintf("AmongUsCapture v2 • 更新: %s", time.Now().Format("15:04:05")),
		},
	}

	if len(unlinkedLines) > 0 {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "👁️ 観戦 / 不参加 (常時ミュート解除)",
			Value:  strings.Join(unlinkedLines, ", "),
			Inline: false,
		})
	}

	// If Game has Ended, show detailed match results with roles
	var lastMatch *memory.MatchResult
	if b.gr != nil {
		lastMatch = b.gr.GetLastMatchResult()
	}

	if gameState == memory.StateEnded && lastMatch != nil {
		embed.Title = "🏆 Among Us • 試合結果 (GAME OVER)"
		embed.Description = fmt.Sprintf("## 🏆 ゲーム終了\n**勝敗:** %s ｜ **理由:** %s", lastMatch.WinningTitle, lastMatch.ReasonText)

		var resultLines []string
		for _, pr := range lastMatch.Players {
			winBadge := "敗北"
			if pr.IsWinner {
				winBadge = "👑 **勝利**"
			}
			deadBadge := "🟢 生存"
			if pr.IsDead {
				deadBadge = "💀 死亡"
			}
			resultLines = append(resultLines, fmt.Sprintf("• %s **%s** (%s) ➔ %s ｜ %s",
				pr.Color.Emoji(), pr.CleanName, pr.RoleName, deadBadge, winBadge))
		}

		embed.Fields = append([]*discordgo.MessageEmbedField{
			{
				Name:   "📊 全プレイヤー役職 & 勝敗一覧",
				Value:  strings.Join(resultLines, "\n"),
				Inline: false,
			},
		}, embed.Fields...)
	}

	embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
		Name:   "💡 ボイスチャンネル制御",
		Value:  "• タスク中: 生存者は Mute & Deaf ｜ 議論中: 全員会話可能 ｜ 幽霊: 幽霊同士で会話\n• ※ 未登録メンバーは常時ミュート解除されます。",
		Inline: false,
	})

	return embed
}

// SetPlayerMapping sets a manual mapping between a game key and a Discord user
func (b *DiscordBot) SetPlayerMapping(key, userID string) {
	cleanKey := strings.TrimSpace(key)
	_ = b.cfgMgr.Update(func(c *config.AppConfig) {
		if userID == "" {
			delete(c.PlayerMappings, cleanKey)
		} else {
			c.PlayerMappings[cleanKey] = userID
		}
	})

	b.RefreshVoiceMembers()
	b.UpdateStatusEmbed()
}

// GameListener implementations

func (b *DiscordBot) OnGameStateChanged(oldState, newState memory.GameState) {
	if !b.IsConnected() {
		return
	}
	var players []memory.PlayerInfo
	if b.gr != nil {
		players = b.gr.GetSnapshotPlayers()
	}
	b.notifyLog(fmt.Sprintf("[DiscordBot] ゲーム状態変化: %s ➔ VC自動ミュート適用中...", newState.String()), "info")
	b.ApplyGameStateToVoice(newState, players)

	// Ensure all members are unmuted when entering Lobby, Menu, or GameOver
	if newState == memory.StateLobby || newState == memory.StateMenu || newState == memory.StateEnded {
		b.UnmuteAll()
	}

	b.RefreshVoiceMembers()
	b.UpdateStatusEmbed()

	// When entering Discussion (Emergency Meeting or Body Reported),
	// run follow-up checks at 300ms, 800ms, and 1500ms to catch any players
	// whose death was confirmed while the meeting screen was loading!
	if newState == memory.StateDiscussion {
		go func() {
			for _, delay := range []time.Duration{300 * time.Millisecond, 800 * time.Millisecond, 1500 * time.Millisecond} {
				time.Sleep(delay)
				b.mu.RLock()
				connected := b.connected
				b.mu.RUnlock()
				if !connected || b.gr == nil {
					return
				}
				if b.gr.GetCurrentState() != memory.StateDiscussion {
					return
				}
				latestPlayers := b.gr.GetSnapshotPlayers()
				b.ApplyGameStateToVoice(memory.StateDiscussion, latestPlayers)
				b.RefreshVoiceMembers()
				b.UpdateStatusEmbed()
			}
		}()
	}
}

func (b *DiscordBot) OnPlayerChanged(event memory.PlayerChangeEvent) {
	if !b.IsConnected() {
		return
	}
	// If a player died during tasks/discussion, reapply
	if event.Action == memory.ActionDied || event.Action == memory.ActionExiled {
		var players []memory.PlayerInfo
		if b.gr != nil {
			players = b.gr.GetSnapshotPlayers()
		}
		state := memory.StateUnknown
		if b.gr != nil {
			state = b.gr.GetCurrentState()
		}
		b.ApplyGameStateToVoice(state, players)
		b.RefreshVoiceMembers()
	}
	b.UpdateStatusEmbed()
}

func (b *DiscordBot) OnLobbyUpdated(event memory.LobbyEvent) {
	if !b.IsConnected() {
		return
	}
	b.UpdateStatusEmbed()
}

func (b *DiscordBot) OnGameOver(event memory.GameOverEvent) {
	if !b.IsConnected() {
		return
	}
	var players []memory.PlayerInfo
	if b.gr != nil {
		players = b.gr.GetSnapshotPlayers()
	}
	b.notifyLog("[DiscordBot] 試合終了検知 (GAME OVER) ➔ 全員ミュート解除中...", "info")
	b.ApplyGameStateToVoice(memory.StateEnded, players)
	b.UnmuteAll()
	b.RefreshVoiceMembers()
	b.UpdateStatusEmbed()
}

func (b *DiscordBot) OnHookStatusChanged(hooked bool, procName, hash string) {
	if !b.IsConnected() {
		return
	}
	b.UpdateStatusEmbed()
}

func (b *DiscordBot) notifyStatus(connected bool, username, errStr string) {
	b.mu.RLock()
	listeners := append([]BotListener(nil), b.listeners...)
	b.mu.RUnlock()
	for _, l := range listeners {
		l.OnBotStatusChanged(connected, username, errStr)
	}
}

func (b *DiscordBot) notifyVoiceMembers(members []VoiceMember) {
	b.mu.RLock()
	listeners := append([]BotListener(nil), b.listeners...)
	b.mu.RUnlock()
	for _, l := range listeners {
		l.OnVoiceMembersUpdated(members)
	}
}

func (b *DiscordBot) notifyLog(message, level string) {
	b.mu.RLock()
	listeners := append([]BotListener(nil), b.listeners...)
	b.mu.RUnlock()
	for _, l := range listeners {
		l.OnBotLog(message, level)
	}
}

// cleanAndSyncSlashCommands wipes all global and guild slash commands to completely abolish them
func (b *DiscordBot) cleanAndSyncSlashCommands(s *discordgo.Session, targetGuildID string) {
	if s == nil || s.State == nil || s.State.User == nil {
		return
	}
	appID := s.State.User.ID
	if appID == "" {
		return
	}

	// 1. Delete ALL global slash commands
	globalCmds, err := s.ApplicationCommands(appID, "")
	if err == nil {
		for _, cmd := range globalCmds {
			_ = s.ApplicationCommandDelete(appID, "", cmd.ID)
			log.Printf("[DiscordBot] Abolished global slash command: /%s", cmd.Name)
		}
	}

	// 2. Wipe ALL guild slash commands by bulk overwriting with an empty list
	guildIDs := make(map[string]bool)
	if targetGuildID != "" {
		guildIDs[targetGuildID] = true
	}
	if s.State != nil {
		for _, g := range s.State.Guilds {
			guildIDs[g.ID] = true
		}
	}

	emptyCommands := []*discordgo.ApplicationCommand{}
	for gID := range guildIDs {
		_, err := s.ApplicationCommandBulkOverwrite(appID, gID, emptyCommands)
		if err != nil {
			log.Printf("[DiscordBot] Failed to wipe slash commands for guild %s: %v", gID, err)
		} else {
			log.Printf("[DiscordBot] Successfully abolished all slash commands for guild %s", gID)
		}
	}
}

// buildColorSelectComponents constructs the interactive select menu placed directly below the status embed
func (b *DiscordBot) buildColorSelectComponents() []discordgo.MessageComponent {
	var currentGameState memory.GameState = memory.StateUnknown
	if b.gr != nil {
		currentGameState = b.gr.GetCurrentState()
	}

	isLobby := (currentGameState == memory.StateLobby)

	placeholder := "🎨 あなたのゲーム内カラーを選択（または登録解除）"
	if !isLobby {
		switch currentGameState {
		case memory.StateTasks:
			placeholder = "🔒 タスク進行中: ロビー待機中のみカラー選択が可能です"
		case memory.StateDiscussion:
			placeholder = "🔒 緊急会議中: ロビー待機中のみカラー選択が可能です"
		case memory.StateMenu:
			placeholder = "🔒 メニュー画面: ロビー入室後にカラー選択が可能です"
		case memory.StateEnded:
			placeholder = "🔒 ゲーム終了: ロビー復帰後にカラー選択が可能です"
		default:
			placeholder = "🔒 ロビー待機中のみカラー選択が可能です"
		}
	}

	options := []discordgo.SelectMenuOption{
		{
			Label:       "🚫 登録解除・不参加（観戦）",
			Value:       "unlink",
			Description: "ゲーム不参加・観戦（自動ミュートの対象外になります）",
			Emoji:       &discordgo.ComponentEmoji{Name: "🚫"},
		},
	}

	colors := []struct {
		Name   string
		JaName string
	}{
		{"Red", "赤"},
		{"Blue", "青"},
		{"Green", "緑"},
		{"Pink", "ピンク"},
		{"Orange", "オレンジ"},
		{"Yellow", "黄"},
		{"Black", "黒"},
		{"White", "白"},
		{"Purple", "紫"},
		{"Brown", "茶"},
		{"Cyan", "シアン"},
		{"Lime", "ライム"},
		{"Maroon", "マルーン"},
		{"Rose", "ローズ"},
		{"Banana", "バナナ"},
		{"Gray", "グレー"},
		{"Tan", "タン"},
		{"Sunset", "サンセット"},
	}

	for _, c := range colors {
		pColor := memory.ParseColorByName(c.Name)
		options = append(options, discordgo.SelectMenuOption{
			Label:       fmt.Sprintf("%s (%s)", c.Name, c.JaName),
			Value:       c.Name,
			Description: fmt.Sprintf("ゲーム内の %s と自分のDiscordを紐付け", c.Name),
			Emoji:       &discordgo.ComponentEmoji{Name: pColor.Emoji()},
		})
	}

	return []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.SelectMenu{
					MenuType:    discordgo.StringSelectMenu,
					CustomID:    "select_player_color",
					Placeholder: placeholder,
					Options:     options,
					Disabled:    !isLobby,
				},
			},
		},
	}
}

// handleInteractionCreate processes select menu interactions and replies with ephemeral (self-only) messages
func (b *DiscordBot) handleInteractionCreate(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.Type != discordgo.InteractionMessageComponent {
		return
	}

	data := i.MessageComponentData()
	if data.CustomID != "select_player_color" {
		return
	}

	userID := ""
	userName := ""
	if i.Member != nil && i.Member.User != nil {
		userID = i.Member.User.ID
		userName = i.Member.DisplayName()
	} else if i.User != nil {
		userID = i.User.ID
		userName = i.User.Username
	}

	if userID == "" || len(data.Values) == 0 {
		return
	}

	var currentGameState memory.GameState = memory.StateUnknown
	if b.gr != nil {
		currentGameState = b.gr.GetCurrentState()
	}
	if currentGameState != memory.StateLobby {
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "⚠️ カラーの紐付け・変更は **ロビー待機中 (LOBBY)** にのみ可能です。（メニュー画面やゲーム進行中は選択できません）",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	selectedVal := data.Values[0]
	var respText string

	if selectedVal == "unlink" {
		b.cfgMgr.Update(func(c *config.AppConfig) {
			for k, uid := range c.PlayerMappings {
				if uid == userID {
					delete(c.PlayerMappings, k)
				}
			}
		})
		respText = "🚫 色の紐付けを解除しました。（不参加・観戦扱いとなり自動ミュートの対象外になります）"
		b.notifyLog(fmt.Sprintf("色リンク解除: %s (<@%s>)", userName, userID), "warn")
	} else {
		b.cfgMgr.Update(func(c *config.AppConfig) {
			// Remove any previous color for this user
			for k, uid := range c.PlayerMappings {
				if uid == userID {
					delete(c.PlayerMappings, k)
				}
			}
			// Deduplicate: remove if another user had this color
			delete(c.PlayerMappings, selectedVal)
			c.PlayerMappings[selectedVal] = userID
		})
		pColor := memory.ParseColorByName(selectedVal)
		respText = fmt.Sprintf("🔗 %s **%s** と紐付けました！ゲーム進行に合わせて自動ミュートが制御されます。", pColor.Emoji(), selectedVal)
		b.notifyLog(fmt.Sprintf("色リンク完了: %s ↔ %s (<@%s>)", selectedVal, userName, userID), "success")
	}

	// Ephemeral response: Visible ONLY to the user who clicked the select menu!
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: respText,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})

	// Refresh voice members and status embed in real time
	b.RefreshVoiceMembers()
	b.UpdateStatusEmbed()
}

