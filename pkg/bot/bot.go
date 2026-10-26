package bot

import (
	"context"
	"fmt"
	"log"
	"sort"
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

	// Game death confirmation (tracks deaths confirmed during discussions / exiles)
	deadMu                sync.RWMutex
	confirmedDead         map[string]bool // keys: lowercase color name & player name
	lastDiscussionEndTime time.Time

	transitionMu  sync.Mutex
	transitionSeq uint64
}

// NewDiscordBot creates a new self-hosted bot instance
func NewDiscordBot(cfgMgr *config.ConfigManager, gr *memory.GameReader) *DiscordBot {
	bot := &DiscordBot{
		cfgMgr:          cfgMgr,
		gr:              gr,
		voiceMembers:    make(map[string]*discordgo.VoiceState),
		embedUpdateChan: make(chan struct{}, 1),
		stopEmbedWorker: make(chan struct{}),
		confirmedDead:   make(map[string]bool),
	}

	go bot.embedUpdateWorker()

	if gr != nil {
		gr.AddListener(bot)
	}

	return bot
}

func (b *DiscordBot) confirmDeadPlayers(players []memory.PlayerInfo) {
	b.deadMu.Lock()
	defer b.deadMu.Unlock()
	if b.confirmedDead == nil {
		b.confirmedDead = make(map[string]bool)
	}
	for _, p := range players {
		if p.IsDead || p.Disconnected {
			if p.Color != memory.ColorUnknown {
				b.confirmedDead[strings.ToLower(p.Color.String())] = true
			}
			if p.Name != "" {
				b.confirmedDead[strings.ToLower(p.Name)] = true
			}
		}
	}
}

func (b *DiscordBot) isConfirmedDead(key string) bool {
	if key == "" {
		return false
	}
	b.deadMu.RLock()
	defer b.deadMu.RUnlock()
	if b.confirmedDead == nil {
		return false
	}
	return b.confirmedDead[strings.ToLower(key)]
}

func (b *DiscordBot) resetConfirmedDead() {
	b.deadMu.Lock()
	defer b.deadMu.Unlock()
	b.confirmedDead = make(map[string]bool)
	b.lastDiscussionEndTime = time.Time{}
}

func (b *DiscordBot) markDiscussionEnded() {
	b.deadMu.Lock()
	defer b.deadMu.Unlock()
	b.lastDiscussionEndTime = time.Now()
}

func (b *DiscordBot) isInExileWindow() bool {
	b.deadMu.RLock()
	defer b.deadMu.RUnlock()
	if b.lastDiscussionEndTime.IsZero() {
		return false
	}
	return time.Since(b.lastDiscussionEndTime) <= 7*time.Second
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
	dg.AddHandler(b.handleGuildCreate)
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

	// Initial Voice channel refresh and Status Embed post immediately (without blocking on slash command cleanup)
	go func() {
		time.Sleep(200 * time.Millisecond)
		b.RefreshVoiceMembers()
		b.UpdateStatusEmbed()
	}()
	go func() {
		time.Sleep(1500 * time.Millisecond)
		b.cleanAndSyncSlashCommands(dg, guildID)
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
	b.RefreshVoiceMembers()
	b.UpdateStatusEmbed()
}

func (b *DiscordBot) handleGuildCreate(s *discordgo.Session, g *discordgo.GuildCreate) {
	if g == nil || g.Guild == nil {
		return
	}
	b.mu.Lock()
	targetGuildID := b.guildID
	voiceCID := b.voiceCID
	if g.Guild.ID == targetGuildID && voiceCID != "" {
		for _, vs := range g.Guild.VoiceStates {
			if vs != nil && vs.ChannelID == voiceCID {
				copyVS := *vs
				b.voiceMembers[vs.UserID] = &copyVS
			}
		}
	}
	b.mu.Unlock()

	if g.Guild.ID == targetGuildID {
		b.RefreshVoiceMembers()
		b.UpdateStatusEmbed()
	}
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

// getVoiceStatesInChannel returns all current voice states in the active voice channel,
// merging Discord Gateway state cache and local voice state tracking.
func (b *DiscordBot) getVoiceStatesInChannel() []*discordgo.VoiceState {
	b.mu.RLock()
	s := b.session
	guildID := b.guildID
	voiceCID := b.voiceCID
	merged := make(map[string]*discordgo.VoiceState, len(b.voiceMembers))
	for uid, vs := range b.voiceMembers {
		if vs != nil && vs.ChannelID == voiceCID {
			cp := *vs
			merged[uid] = &cp
		}
	}
	b.mu.RUnlock()

	if s == nil || guildID == "" || voiceCID == "" {
		return nil
	}

	if guild, err := s.State.Guild(guildID); err == nil && guild != nil {
		for _, vs := range guild.VoiceStates {
			if vs != nil && vs.ChannelID == voiceCID {
				cp := *vs
				merged[vs.UserID] = &cp
			}
		}
	}

	res := make([]*discordgo.VoiceState, 0, len(merged))
	for _, vs := range merged {
		res = append(res, vs)
	}
	return res
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

	voiceStates := b.getVoiceStatesInChannel()

	var membersInVC []VoiceMember
	cfg := b.cfgMgr.Get()
	var inGamePlayers []memory.PlayerInfo
	if b.gr != nil {
		inGamePlayers = b.gr.GetSnapshotPlayers()
	}

	for _, vs := range voiceStates {
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
	// Check explicit manual mapping (by color or name)
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
	if err == nil {
		// Immediately sync local voice state cache so subsequent transition steps see the updated state
		b.mu.Lock()
		if vs, ok := b.voiceMembers[userID]; ok && vs != nil {
			vs.Mute = mute
			vs.Deaf = deaf
		}
		b.mu.Unlock()

		if s.State != nil {
			s.State.Lock()
			for _, g := range s.State.Guilds {
				if g.ID == guildID {
					for _, vs := range g.VoiceStates {
						if vs != nil && vs.UserID == userID {
							vs.Mute = mute
							vs.Deaf = deaf
						}
					}
					break
				}
			}
			s.State.Unlock()
		}
	}
	return err
}

// UnmuteUser removes server mute and server deafen from a single user
func (b *DiscordBot) UnmuteUser(userID string) error {
	return b.UpdateUserMuteState(userID, false, false)
}

// UnmuteAll removes server mute and server deafen from all users in the active voice channel
func (b *DiscordBot) UnmuteAll() {
	voiceStates := b.getVoiceStatesInChannel()
	var wg sync.WaitGroup
	for _, vs := range voiceStates {
		wg.Add(1)
		go func(uid string) {
			defer wg.Done()
			_ = b.UpdateUserMuteState(uid, false, false)
		}(vs.UserID)
	}
	wg.Wait()
}

// MuteAllParticipants puts all participating players in the active voice channel into muted state (Step 1 of phase transitions)
func (b *DiscordBot) MuteAllParticipants(players []memory.PlayerInfo) {
	b.mu.RLock()
	s := b.session
	guildID := b.guildID
	voiceCID := b.voiceCID
	b.mu.RUnlock()

	if s == nil || guildID == "" || voiceCID == "" {
		return
	}

	cfg := b.cfgMgr.Get()
	voiceStates := b.getVoiceStatesInChannel()

	var wg sync.WaitGroup
	for _, vs := range voiceStates {
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

		matchedName, _ := findMatch(userID, username, displayName, cfg.PlayerMappings, players)
		if matchedName == "" {
			// Spectators remain unmuted
			continue
		}

		wg.Add(1)
		go func(uid string) {
			defer wg.Done()
			err := b.UpdateUserMuteState(uid, true, true)
			if err != nil {
				log.Printf("[DiscordBot] Failed to mute participant %s in transition Step 1: %v", uid, err)
			}
		}(userID)
	}

	wg.Wait()
}

// ApplyGameStateToVoice automates muting / unmuting based on Among Us game state
func (b *DiscordBot) ApplyGameStateToVoice(state memory.GameState, players []memory.PlayerInfo) {
	b.ApplyGameStateToVoiceWithOptions(state, players, false)
}

// ApplyGameStateToVoiceWithOptions applies target mute/deaf states to all VC members (force=true sends API requests regardless of cached state)
func (b *DiscordBot) ApplyGameStateToVoiceWithOptions(state memory.GameState, players []memory.PlayerInfo, force bool) {
	b.mu.RLock()
	s := b.session
	guildID := b.guildID
	voiceCID := b.voiceCID
	b.mu.RUnlock()

	if s == nil || guildID == "" || voiceCID == "" {
		return
	}

	cfg := b.cfgMgr.Get()
	voiceStates := b.getVoiceStatesInChannel()

	// Build map of Game player status
	playerStatusMap := make(map[string]memory.PlayerInfo)
	for _, p := range players {
		if p.Name != "" {
			playerStatusMap[strings.ToLower(p.Name)] = p
		}
		playerStatusMap[strings.ToLower(p.Color.String())] = p
	}

	var wg sync.WaitGroup

	for _, vs := range voiceStates {
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

		targetMute, targetDeaf, shouldApply := b.resolveTargetVoiceState(state, matchedName, playerStatusMap)
		if !shouldApply {
			continue
		}

		// Send API request if forced (major state transition) or if state differs from cached voice state
		if force || vs.Mute != targetMute || vs.Deaf != targetDeaf {
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

func (b *DiscordBot) resolveTargetVoiceState(state memory.GameState, matchedName string, playerStatusMap map[string]memory.PlayerInfo) (targetMute bool, targetDeaf bool, shouldApply bool) {
	// If user is NOT participating in the game (unmatched spectator/listener):
	// NEVER mute them! Keep them unmuted and undeafened at all times.
	if matchedName == "" {
		return false, false, true
	}

	// Determine if this in-game player is confirmed dead (死亡状態) vs pseudo-dead (仮死状態)
	confirmedDead := b.isConfirmedDead(matchedName)
	isDead := confirmedDead
	if p, ok := playerStatusMap[strings.ToLower(matchedName)]; ok {
		if b.isConfirmedDead(p.Name) || b.isConfirmedDead(p.Color.String()) {
			confirmedDead = true
		}
		isDead = p.IsDead || p.Disconnected || confirmedDead
	}

	switch state {
	case memory.StateMenu, memory.StateLobby, memory.StateEnded:
		// Lobby / Menu / GameOver: Everyone unmuted & undeafened
		return false, false, true

	case memory.StateTasks:
		// Tasks:
		// Confirmed dead players (死亡状態・幽霊): Unmuted & Undeafened (幽霊同士で会話可能)
		// Alive players & Pseudo-dead players (生存者・仮死状態): Muted & Deafened (ミュート維持・幽霊の声も聞こえない)
		if confirmedDead {
			return false, false, true
		}
		return true, true, true

	case memory.StateDiscussion:
		// Discussion / Meeting:
		// Alive players: Unmuted & Undeafened (can discuss)
		// Dead players: Muted (cannot interrupt discussion, but can listen)
		if isDead {
			return true, false, true
		}
		return false, false, true

	default:
		// Unknown / Default: don't alter
		return false, false, false
	}
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
		log.Printf("[DiscordBot] Failed to send status embed to channel %s: %v. Retrying in 1s...", statusCID, err)
		go func() {
			time.Sleep(1 * time.Second)
			b.UpdateStatusEmbed()
		}()
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

		// Determine dead status: during tasks, newly dead players remain in pseudo-death (仮死状態)
		isPlayerDead := p.IsDead || p.Disconnected
		if gameState == memory.StateTasks {
			colorKey := strings.ToLower(p.Color.String())
			nameKey := strings.ToLower(p.Name)
			if !b.isConfirmedDead(colorKey) && !b.isConfirmedDead(nameKey) {
				isPlayerDead = false
			}
		}

		if isPlayerDead {
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
		for _, vs := range b.getVoiceStatesInChannel() {
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

	// Sort mapped keys by standard Among Us color index for deterministic, stable display
	var sortedKeys []string
	for key := range cfg.PlayerMappings {
		sortedKeys = append(sortedKeys, key)
	}
	sort.Slice(sortedKeys, func(i, j int) bool {
		c1 := memory.ParseColorByName(sortedKeys[i])
		c2 := memory.ParseColorByName(sortedKeys[j])
		if c1 != memory.ColorUnknown && c2 != memory.ColorUnknown {
			return c1 < c2
		}
		if c1 != memory.ColorUnknown {
			return true
		}
		if c2 != memory.ColorUnknown {
			return false
		}
		return sortedKeys[i] < sortedKeys[j]
	})

	for _, key := range sortedKeys {
		uid := cfg.PlayerMappings[key]
		linkedUserIDs[uid] = true
		pColor := memory.ParseColorByName(key)
		emoji := pColor.Emoji()
		colorName := key
		if pColor != memory.ColorUnknown {
			colorName = pColor.String()
		}

		// Find in-game player info
		var inGameName string
		p, hasPlayer := playerStatusMap[strings.ToLower(key)]
		if !hasPlayer {
			// Also check by color match
			for _, pi := range players {
				if pi.Color == pColor {
					p = pi
					hasPlayer = true
					break
				}
			}
		}

		if hasPlayer && p.Name != "" {
			cleanName, _, _, _ := memory.ParseRoleAndCleanName(p.Name, p.RoleType, p.IsImpostor)
			if cleanName != "" {
				inGameName = cleanName
			} else {
				inGameName = p.Name
			}
		}
		if inGameName == "" {
			inGameName = "-"
		}

		// Player game status
		statusBadge := "🟢 生存"
		if gameState == memory.StateLobby || gameState == memory.StateMenu {
			statusBadge = "🎮 待機"
		} else if !hasPlayer {
			statusBadge = "⚪ 未参加"
		} else if gameState == memory.StateTasks {
			// During Tasks: deaths are kept hidden (仮死状態) unless confirmed in previous discussion
			colorKey := strings.ToLower(p.Color.String())
			nameKey := strings.ToLower(p.Name)
			if b.isConfirmedDead(colorKey) || b.isConfirmedDead(nameKey) {
				statusBadge = "💀 死亡"
			} else if p.Disconnected {
				statusBadge = "🔌 切断"
			} else {
				statusBadge = "🟢 生存"
			}
		} else {
			// During Discussion, Ended, or other states: reveal confirmed deaths
			colorKey := strings.ToLower(p.Color.String())
			nameKey := strings.ToLower(p.Name)
			if p.IsDead || b.isConfirmedDead(colorKey) || b.isConfirmedDead(nameKey) {
				statusBadge = "💀 死亡"
			} else if p.Disconnected {
				statusBadge = "🔌 切断"
			} else {
				statusBadge = "🟢 生存"
			}
		}

		// Format: カラーDiscord名（ゲーム内名）｜生存状態
		linkLines = append(linkLines, fmt.Sprintf("%s **%s** <@%s>（%s）｜ %s", emoji, colorName, uid, inGameName, statusBadge))
	}

	mappingText := "*(登録されているプレイヤーはありません)*"
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
				Name:   "プレイヤーカラー",
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
		Value:  "• タスク中: 生存・仮死は Mute & Deaf / 幽霊(死亡確定)は会話可能 ｜ 議論中: 生存者は会話可能 / 死亡者は聞き専\n• ※ 未登録メンバー（観戦）は常時ミュート解除されます。",
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

func (b *DiscordBot) nextTransitionSeq() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.transitionSeq++
	return b.transitionSeq
}

func (b *DiscordBot) getTransitionSeq() uint64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.transitionSeq
}

func (b *DiscordBot) OnGameStateChanged(oldState, newState memory.GameState) {
	var players []memory.PlayerInfo
	if b.gr != nil {
		players = b.gr.GetSnapshotPlayers()
	}

	// State-specific death tracking:
	// - Lobby / Menu / GameOver: reset confirmed deaths
	// - Start of a new match (non-Discussion -> Tasks): reset confirmed deaths so previous match never leaks
	// - Discussion: confirm all currently dead players (transition from pseudo-death to dead)
	// - Discussion -> Tasks: confirm any player exiled at the end of discussion
	if newState == memory.StateLobby || newState == memory.StateMenu || newState == memory.StateEnded {
		b.resetConfirmedDead()
	} else if newState == memory.StateTasks && oldState != memory.StateDiscussion {
		b.resetConfirmedDead()
	} else if newState == memory.StateDiscussion {
		b.confirmDeadPlayers(players)
	} else if oldState == memory.StateDiscussion && newState == memory.StateTasks {
		b.markDiscussionEnded()
		b.confirmDeadPlayers(players)
	}

	if !b.IsConnected() {
		return
	}

	seq := b.nextTransitionSeq()
	b.UpdateStatusEmbed()

	go func(sOld, sNew memory.GameState, initPlayers []memory.PlayerInfo, mySeq uint64) {
		b.transitionMu.Lock()
		defer b.transitionMu.Unlock()

		if b.getTransitionSeq() != mySeq || !b.IsConnected() {
			return
		}

		latestPlayers := initPlayers
		if b.gr != nil {
			latestPlayers = b.gr.GetSnapshotPlayers()
		}

		if sOld == memory.StateTasks && sNew == memory.StateDiscussion {
			// Transition: TASKS -> DISCUSSION
			// Step 1: 一度全員をミュート状態にする (Mute all participants first so ghosts don't leak into discussion)
			b.notifyLog("[DiscordBot] タスク ➔ 議論移行: 一度全員をミュート状態にしています...", "info")
			b.MuteAllParticipants(latestPlayers)

			time.Sleep(250 * time.Millisecond)
			if b.getTransitionSeq() != mySeq || !b.IsConnected() {
				return
			}
			if b.gr != nil {
				latestPlayers = b.gr.GetSnapshotPlayers()
			}
			b.confirmDeadPlayers(latestPlayers)

			// Step 2: 生存者だけミュート解除（死者はマイクミュート）
			b.notifyLog("[DiscordBot] 議論開始: 生存者のみミュート解除（死者はマイクミュート）を適用中...", "info")
			b.ApplyGameStateToVoiceWithOptions(memory.StateDiscussion, latestPlayers, true)
		} else if sOld == memory.StateDiscussion && sNew == memory.StateTasks {
			// Transition: DISCUSSION -> TASKS
			// Step 1: いったん全員ミュートにする (Mute all participants first)
			b.notifyLog("[DiscordBot] 議論 ➔ タスク移行: いったん全員をミュート状態にしています...", "info")
			b.MuteAllParticipants(latestPlayers)

			time.Sleep(250 * time.Millisecond)
			if b.getTransitionSeq() != mySeq || !b.IsConnected() {
				return
			}
			if b.gr != nil {
				latestPlayers = b.gr.GetSnapshotPlayers()
			}
			b.confirmDeadPlayers(latestPlayers)

			// Step 2: 死者はミュート解除（生存者はミュートのまま）
			b.notifyLog("[DiscordBot] タスク再開: 死者のみミュート解除（生存者はミュートのまま）を適用中...", "info")
			b.ApplyGameStateToVoiceWithOptions(memory.StateTasks, latestPlayers, false)
		} else if sNew == memory.StateLobby || sNew == memory.StateMenu || sNew == memory.StateEnded {
			// Lobby / Menu / GameOver: Unmute everyone
			b.notifyLog(fmt.Sprintf("[DiscordBot] ゲーム状態変化: %s ➔ 全員ミュート解除中...", sNew.String()), "info")
			b.ApplyGameStateToVoiceWithOptions(sNew, latestPlayers, true)
		} else {
			// Match start (Lobby -> Tasks, etc.): Force mute all participants
			b.notifyLog(fmt.Sprintf("[DiscordBot] ゲーム状態変化: %s ➔ VC自動ミュート適用中...", sNew.String()), "info")
			b.ApplyGameStateToVoiceWithOptions(sNew, latestPlayers, true)
		}

		b.RefreshVoiceMembers()
		b.UpdateStatusEmbed()

		// Follow-up checks for delayed death flags (body report loading or ejection animation)
		if sNew == memory.StateDiscussion {
			for _, delay := range []time.Duration{400 * time.Millisecond, 900 * time.Millisecond} {
				time.Sleep(delay)
				if b.getTransitionSeq() != mySeq || !b.IsConnected() || b.gr == nil {
					return
				}
				if b.gr.GetCurrentState() != memory.StateDiscussion {
					return
				}
				p := b.gr.GetSnapshotPlayers()
				b.confirmDeadPlayers(p)
				b.ApplyGameStateToVoice(memory.StateDiscussion, p)
				b.RefreshVoiceMembers()
				b.UpdateStatusEmbed()
			}
		} else if sOld == memory.StateDiscussion && sNew == memory.StateTasks {
			for _, delay := range []time.Duration{500 * time.Millisecond, 1000 * time.Millisecond, 1500 * time.Millisecond, 1500 * time.Millisecond, 1500 * time.Millisecond} {
				time.Sleep(delay)
				if b.getTransitionSeq() != mySeq || !b.IsConnected() || b.gr == nil {
					return
				}
				if b.gr.GetCurrentState() != memory.StateTasks || !b.isInExileWindow() {
					return
				}
				p := b.gr.GetSnapshotPlayers()
				b.confirmDeadPlayers(p)
				b.ApplyGameStateToVoice(memory.StateTasks, p)
				b.RefreshVoiceMembers()
				b.UpdateStatusEmbed()
			}
		}
	}(oldState, newState, players, seq)
}

func (b *DiscordBot) OnPlayerChanged(event memory.PlayerChangeEvent) {
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
		if state == memory.StateDiscussion || event.Action == memory.ActionExiled || (state == memory.StateTasks && b.isInExileWindow()) {
			b.confirmDeadPlayers(players)
		}
		if b.IsConnected() {
			go func(st memory.GameState, pl []memory.PlayerInfo) {
				b.ApplyGameStateToVoice(st, pl)
				b.RefreshVoiceMembers()
			}(state, players)
		}
	}
	if b.IsConnected() {
		b.UpdateStatusEmbed()
	}
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
		{"Coral", "コーラル"},
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
		_ = b.UnmuteUser(userID)
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

