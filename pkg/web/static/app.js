// AmongUsCapture v2 Dedicated Client Controller

const state = {
  hooked: false,
  gameState: 5,
  lobbyCode: '',
  players: [],
  botConnected: false,
  botUsername: '',
  guilds: [],
  selectedGuildId: '',
  selectedVoiceCId: '',
  selectedStatusCId: '',
  voiceMembers: [],
  playerMappings: {},
  lastMatchResult: null,
  debugMode: false,
};

const els = {
  appVersion: document.getElementById('app-version'),
  releasePillLink: document.getElementById('release-pill-link'),
  releaseLabelText: document.getElementById('release-label-text'),
  releaseVersionPill: document.getElementById('release-version-pill'),
  statusVersionBadge: document.getElementById('status-version-badge'),
  statusVersionLabel: document.getElementById('status-version-label'),
  statusVersionText: document.getElementById('status-version-text'),
  footerVersion: document.getElementById('footer-version'),
  overallBadge: document.getElementById('overall-status-badge'),
  gameHook: document.getElementById('game-hook-status'),
  gameHash: document.getElementById('game-hash'),
  gameState: document.getElementById('game-state-text'),
  botStatusText: document.getElementById('bot-status-text'),
  botSubText: document.getElementById('bot-sub-text'),

  // Bot Elements
  botTokenInput: document.getElementById('bot-token'),
  btnToggleToken: document.getElementById('btn-toggle-token'),
  btnBotToggle: document.getElementById('btn-bot-toggle'),
  selectGuild: document.getElementById('select-guild'),
  selectVC: document.getElementById('select-vc'),
  selectStatusChannel: document.getElementById('select-status-channel'),
  btnApplyVC: document.getElementById('btn-apply-vc'),
  btnRefreshVC: document.getElementById('btn-refresh-vc'),
  vcMembersList: document.getElementById('vc-members-list'),
  vcCount: document.getElementById('vc-count'),

  // Logs & Header
  logsCard: document.getElementById('logs-card'),
  logStatusBadge: document.getElementById('log-status-badge'),
  logDisabledNotice: document.getElementById('log-disabled-notice'),
  logConsole: document.getElementById('log-console'),
  btnClearLogs: document.getElementById('btn-clear-logs'),
  btnRefreshOffsets: document.getElementById('btn-refresh-offsets'),

  // Unmute
  btnUnmuteAll: document.getElementById('btn-unmute-all'),

  // Resource Tile
  resourceTile: document.getElementById('resource-tile'),
  resourceStats: document.getElementById('resource-stats'),
  resourceSub: document.getElementById('resource-sub'),
};

const colorMap = {
  Red: { hex: '#c51111', label: '🔴 Red (赤)' },
  Blue: { hex: '#132ed1', label: '🔵 Blue (青)' },
  Green: { hex: '#117f2d', label: '🟢 Green (緑)' },
  Pink: { hex: '#ed54ba', label: '🌸 Pink (ピンク)' },
  Orange: { hex: '#ef7d0d', label: '🟠 Orange (オレンジ)' },
  Yellow: { hex: '#f5f557', label: '🟡 Yellow (黄)' },
  Black: { hex: '#3f474e', label: '⚫ Black (黒)' },
  White: { hex: '#d6e0f0', label: '⚪ White (白)' },
  Purple: { hex: '#6b2fbc', label: '🟣 Purple (紫)' },
  Brown: { hex: '#71491e', label: '🟤 Brown (茶)' },
  Cyan: { hex: '#38fedc', label: '💎 Cyan (シアン)' },
  Lime: { hex: '#50ef39', label: '🍏 Lime (ライム)' },
  Maroon: { hex: '#6b112d', label: '🍷 Maroon (マルーン)' },
  Rose: { hex: '#ec7597', label: '🌹 Rose (ローズ)' },
  Banana: { hex: '#fffe9d', label: '🍌 Banana (バナナ)' },
  Gray: { hex: '#708496', label: '🐘 Gray (グレー)' },
  Tan: { hex: '#928776', label: '🪵 Tan (タン)' },
  Sunset: { hex: '#dc6565', label: '🌅 Sunset (サンセット)' },
};

function getColorInfo(name) {
  if (!name) return { hex: '#888', label: 'Unknown' };
  return colorMap[name] || { hex: '#888', label: name };
}

function stringsEqual(a, b) {
  if (!a || !b) return false;
  return a.toString().trim().toLowerCase() === b.toString().trim().toLowerCase();
}

function sanitizePlayerName(name, defaultColor = '') {
  if (!name) return `Player (${defaultColor || 'Unknown'})`;
  // Remove control characters and non-printable Unicode
  const clean = name.replace(/[\u0000-\u001F\u007F-\u009F\uE000-\uF8FF\uFFFD]/g, '').trim();
  if (clean.length === 0) {
    return `Player (${defaultColor || 'Unknown'})`;
  }
  return clean;
}

function isMatchLogDisabled() {
  return (state.gameState === 2 || state.gameState === 3) && !state.debugMode;
}

function addLog(msg, type = 'info') {
  if (isMatchLogDisabled()) {
    return;
  }
  const line = document.createElement('div');
  line.className = `log-line log-${type}`;
  const time = new Date().toLocaleTimeString();
  line.textContent = `[${time}] ${msg}`;
  els.logConsole.appendChild(line);
  els.logConsole.scrollTop = els.logConsole.scrollHeight;
}

// Unmute All Button
if (els.btnUnmuteAll) {
  els.btnUnmuteAll.addEventListener('click', () => {
    fetch('/api/bot/unmute', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({}),
    })
      .then((r) => r.json())
      .then(() => {
        addLog('ボイスチャンネル参加者全員のミュートを解除しました', 'success');
      })
      .catch((err) => {
        addLog(`全員ミュート解除エラー: ${err}`, 'error');
      });
  });
}

// Password toggle
els.btnToggleToken.addEventListener('click', () => {
  const type = els.botTokenInput.type === 'password' ? 'text' : 'password';
  els.botTokenInput.type = type;
  els.btnToggleToken.textContent = type === 'password' ? '👁️' : '🔒';
});

// Auto-save Bot Token whenever changed or focus lost
if (els.botTokenInput) {
  const saveTokenToConfig = () => {
    const token = els.botTokenInput.value.trim();
    fetch('/api/config/save-token', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ token }),
    }).catch(() => {});
  };
  els.botTokenInput.addEventListener('change', saveTokenToConfig);
  els.botTokenInput.addEventListener('blur', saveTokenToConfig);
}

// Bot Start/Stop
els.btnBotToggle.addEventListener('click', () => {
  if (state.botConnected) {
    // Stop
    els.btnBotToggle.disabled = true;
    els.btnBotToggle.textContent = '停止中...';
    fetch('/api/bot/stop', { method: 'POST' })
      .then((r) => r.json())
      .then(() => {
        addLog('Discord Bot 停止要求を送信しました', 'warn');
      })
      .catch((err) => {
        addLog(`Bot 停止エラー: ${err}`, 'error');
      })
      .finally(() => {
        setTimeout(() => {
          els.btnBotToggle.disabled = false;
        }, 300);
      });
  } else {
    // Start
    const token = els.botTokenInput.value.trim();
    if (!token) {
      alert('Discord Bot Token を入力してください！');
      return;
    }
    const guildId = els.selectGuild.value || state.selectedGuildId;
    const voiceCId = els.selectVC.value || state.selectedVoiceCId;
    const statusCId = els.selectStatusChannel.disabled ? state.selectedStatusCId : els.selectStatusChannel.value;

    els.btnBotToggle.disabled = true;
    els.btnBotToggle.textContent = '起動中...';
    addLog('Discord Bot 起動中...', 'info');

    fetch('/api/bot/start', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ token, guildId, voiceCId, statusCId }),
    })
      .then((r) => r.json())
      .then((res) => {
        if (!res.success) {
          addLog(`Bot 起動失敗: ${res.error}`, 'error');
        }
      })
      .catch((err) => {
        addLog(`Bot 起動エラー: ${err}`, 'error');
      })
      .finally(() => {
        setTimeout(() => {
          els.btnBotToggle.disabled = false;
        }, 300);
      });
  }
});

// Load Guilds & Channels
function fetchGuilds() {
  fetch('/api/bot/guilds')
    .then((r) => r.json())
    .then((res) => {
      if (res.success && res.guilds) {
        state.guilds = res.guilds;
        populateGuilds(res.guilds);
      }
    });
}

function populateGuilds(guilds) {
  els.selectGuild.innerHTML = '<option value="">サーバーを選択...</option>';
  guilds.forEach((g) => {
    const opt = document.createElement('option');
    opt.value = g.id;
    opt.textContent = g.name;
    if (g.id === state.selectedGuildId) opt.selected = true;
    els.selectGuild.appendChild(opt);
  });
  els.selectGuild.disabled = false;
  populateChannels();
}

els.selectGuild.addEventListener('change', () => {
  state.selectedGuildId = els.selectGuild.value;
  populateChannels();
});

function populateChannels() {
  const g = state.guilds.find((x) => x.id === state.selectedGuildId);
  
  // 1. Voice Channels
  els.selectVC.innerHTML = '<option value="">ボイスチャンネルを選択...</option>';
  if (g && g.vcs && g.vcs.length > 0) {
    g.vcs.forEach((vc) => {
      const opt = document.createElement('option');
      opt.value = vc.id;
      opt.textContent = `🔊 ${vc.name}`;
      if (vc.id === state.selectedVoiceCId) opt.selected = true;
      els.selectVC.appendChild(opt);
    });
    els.selectVC.disabled = false;
  } else {
    els.selectVC.disabled = true;
  }

  // 2. Text Channels (Status Channel)
  els.selectStatusChannel.innerHTML = '<option value="">(ステータス通知なし / 選択)</option>';
  if (g && g.tcs && g.tcs.length > 0) {
    g.tcs.forEach((tc) => {
      const opt = document.createElement('option');
      opt.value = tc.id;
      opt.textContent = `#️⃣ ${tc.name}`;
      if (tc.id === state.selectedStatusCId) opt.selected = true;
      els.selectStatusChannel.appendChild(opt);
    });
    els.selectStatusChannel.disabled = false;
  } else {
    els.selectStatusChannel.disabled = true;
  }

  els.btnApplyVC.disabled = !g;
}

els.btnApplyVC.addEventListener('click', () => {
  const guildId = els.selectGuild.value;
  const voiceCId = els.selectVC.value;
  const statusCId = els.selectStatusChannel.value;

  if (!guildId) {
    alert('サーバーを選択してください。');
    return;
  }
  state.selectedGuildId = guildId;
  state.selectedVoiceCId = voiceCId;
  state.selectedStatusCId = statusCId;

  fetch('/api/bot/set-channel', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ guildId, voiceCId, statusCId }),
  })
    .then((r) => r.json())
    .then(() => {
      addLog(`チャンネル設定更新: VC=${voiceCId || '未選択'}, Status=${statusCId || 'なし'}`, 'success');
    });
});

els.btnRefreshVC.addEventListener('click', () => {
  fetch('/api/sync', { method: 'POST' });
});

// WebSocket Connection
let ws;
function connectWS() {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  const wsUrl = `${protocol}//${window.location.host}/ws`;

  ws = new WebSocket(wsUrl);

  ws.onopen = () => {
    addLog('内部 WebSocket 接続確立', 'success');
  };

  ws.onmessage = (event) => {
    try {
      const data = JSON.parse(event.data);
      handleBackendMessage(data);
    } catch (e) {
      console.error(e);
    }
  };

  ws.onclose = () => {
    setTimeout(connectWS, 1500);
  };
}

function handleBackendMessage(data) {
  switch (data.type) {
    case 'initial_state':
    case 'state_update':
      updateUI(data.payload);
      break;
    case 'log':
      addLog(data.payload.message, data.payload.level || 'info');
      break;
  }
}

function updateUI(s) {
  // Version & New Release Detection
  if (s.hasUpdate && s.latestVersion) {
    // 1. Header Pill: 赤文字で「ニューリリース」と最新バージョン、リンク先を最新に差し替え
    if (els.releasePillLink) {
      els.releasePillLink.classList.add('new-release');
      if (s.releaseUrl) els.releasePillLink.href = s.releaseUrl;
      els.releasePillLink.title = `最新バージョン ${s.latestVersion} が利用可能です！クリックしてダウンロード`;
    }
    if (els.releaseLabelText) {
      els.releaseLabelText.textContent = 'ニューリリース:';
    }
    if (els.releaseVersionPill) {
      els.releaseVersionPill.textContent = s.latestVersion;
    }

    // 2. Status Overview Card Header
    if (els.statusVersionBadge) {
      els.statusVersionBadge.classList.add('new-release');
    }
    if (els.statusVersionLabel) {
      els.statusVersionLabel.textContent = 'ニューリリース:';
    }
    if (els.statusVersionText) {
      els.statusVersionText.textContent = s.latestVersion;
    }

    // 3. Footer
    if (els.footerVersion) {
      els.footerVersion.className = 'text-red-alert';
      els.footerVersion.textContent = `${s.version || 'v2.2.8'} (最新: ${s.latestVersion})`;
    }
  } else {
    // Regular / Up-to-date state
    if (els.releasePillLink) {
      els.releasePillLink.classList.remove('new-release');
      els.releasePillLink.href = 'https://github.com/yorukaze-luru/amongus-capture-v2/releases';
      els.releasePillLink.title = 'GitHub リリースページ・更新履歴を開く';
    }
    if (els.releaseLabelText) {
      els.releaseLabelText.textContent = 'リリースバージョン:';
    }
    if (els.releaseVersionPill && s.version) {
      els.releaseVersionPill.textContent = s.version;
    }

    if (els.statusVersionBadge) {
      els.statusVersionBadge.classList.remove('new-release');
    }
    if (els.statusVersionLabel) {
      els.statusVersionLabel.textContent = 'リリースバージョン:';
    }
    if (els.statusVersionText && s.version) {
      els.statusVersionText.textContent = s.version;
    }

    if (els.footerVersion && s.version) {
      els.footerVersion.className = '';
      els.footerVersion.textContent = s.version;
    }
  }

  if (s.version && els.appVersion) {
    els.appVersion.textContent = s.version;
  }
  state.hooked = s.hooked;
  state.gameState = s.gameState;
  state.lobbyCode = s.lobbyCode;
  state.players = s.players || [];
  state.botConnected = s.botConnected;
  state.botUsername = s.botUsername;
  state.voiceMembers = s.voiceMembers || [];
  state.playerMappings = s.playerMappings || {};
  state.selectedGuildId = s.guildId || '';
  state.selectedVoiceCId = s.voiceCId || '';
  state.selectedStatusCId = s.statusCId || '';
  state.lastMatchResult = s.lastMatchResult || null;
  state.debugMode = !!s.debugMode;

  // Toggle Live Log Console during match (TASKS / DISCUSSION) unless Debug Mode is enabled
  const logDisabled = isMatchLogDisabled();
  if (els.logConsole && els.logDisabledNotice) {
    if (logDisabled) {
      els.logConsole.style.display = 'none';
      els.logDisabledNotice.style.display = 'flex';
      if (els.btnClearLogs) els.btnClearLogs.disabled = true;
    } else {
      els.logConsole.style.display = 'flex';
      els.logDisabledNotice.style.display = 'none';
      if (els.btnClearLogs) els.btnClearLogs.disabled = false;
    }
  }
  if (els.logStatusBadge) {
    if (state.debugMode) {
      els.logStatusBadge.style.display = 'inline-block';
      els.logStatusBadge.className = 'badge badge-success';
      els.logStatusBadge.textContent = '🛠️ DEBUG MODE';
    } else if (logDisabled) {
      els.logStatusBadge.style.display = 'inline-block';
      els.logStatusBadge.className = 'badge badge-warning';
      els.logStatusBadge.textContent = '🔒 試合中無効';
    } else {
      els.logStatusBadge.style.display = 'none';
    }
  }

  // Game Hook
  if (s.hooked) {
    els.gameHook.textContent = `● 接続中 (${s.processName || 'Among Us'})`;
    els.gameHook.className = 'tile-value text-success';
    els.gameHash.textContent = `Hash: ${s.gameHash ? s.gameHash.substring(0, 10) + '...' : 'Auto-Detected'}`;
  } else {
    els.gameHook.textContent = '● 未検出';
    els.gameHook.className = 'tile-value text-danger';
    els.gameHash.textContent = 'Among Us 起動待機中...';
  }

  // Game State
  const stateNames = ['MENU', 'LOBBY', 'TASKS', 'DISCUSSION', 'ENDED', 'UNKNOWN'];
  const stateName = stateNames[s.gameState] || 'UNKNOWN';
  if (els.gameState) els.gameState.textContent = stateName;

  // Bot Status
  const wasBotConnected = state.botConnected;
  state.botConnected = s.botConnected;

  if (s.botConnected) {
    els.botStatusText.textContent = `● Bot 稼働中`;
    els.botStatusText.className = 'tile-value text-success';
    els.botSubText.textContent = `@${s.botUsername || 'DiscordBot'}`;
    els.btnBotToggle.textContent = 'Bot停止';
    els.btnBotToggle.className = 'btn btn-danger';
    els.btnBotToggle.disabled = false;
    els.overallBadge.textContent = 'ACTIVE';
    els.overallBadge.className = 'badge badge-success';
    if (!wasBotConnected || state.guilds.length === 0) {
      fetchGuilds();
    }
  } else {
    els.botStatusText.textContent = `● Bot 停止中`;
    els.botStatusText.className = 'tile-value text-danger';
    els.botSubText.textContent = s.botError ? `エラー: ${s.botError}` : 'Token を入力して起動';
    els.btnBotToggle.textContent = 'Bot起動';
    els.btnBotToggle.className = 'btn btn-primary';
    els.btnBotToggle.disabled = false;
    els.overallBadge.textContent = 'STANDBY';
    els.overallBadge.className = 'badge badge-warning';
  }

  if (s.botToken && (!els.botTokenInput.value || document.activeElement !== els.botTokenInput)) {
    els.botTokenInput.value = s.botToken;
  }

  // Real-time Resource Usage (RAM & CPU of this software only)
  if (s.metrics) {
    const wsMB = s.metrics.working_set_mb || 0;
    const cpu = s.metrics.cpu_percent || 0;

    if (els.resourceStats) {
      els.resourceStats.textContent = `${wsMB.toFixed(1)} MB`;
      if (cpu >= 15.0) {
        els.resourceStats.className = 'tile-value text-warning';
      } else {
        els.resourceStats.className = 'tile-value text-success';
      }
    }

    if (els.resourceSub) {
      els.resourceSub.textContent = `CPU ${cpu.toFixed(1)}%`;
    }

    if (els.resourceTile) {
      els.resourceTile.title = `RAM: ${wsMB.toFixed(1)} MB | CPU: ${cpu.toFixed(1)}%`;
    }
  }

  // Render VC Members with direct Color Link dropdowns
  renderVoiceMembers(state.voiceMembers);
}

let lastRenderedVCJson = '';

function renderVoiceMembers(members) {
  if (els.vcCount) els.vcCount.textContent = members ? members.length : 0;

  if (!els.vcMembersList) return;

  const currentPayloadStr = JSON.stringify({
    m: members,
    p: state.playerMappings,
  });
  if (currentPayloadStr === lastRenderedVCJson) {
    return; // Fast path: state did not change, skip full DOM rebuild!
  }
  lastRenderedVCJson = currentPayloadStr;

  if (!members || members.length === 0) {
    els.vcMembersList.innerHTML = '<div class="empty-state">ボイスチャンネルに参加しているメンバーはいません</div>';
    return;
  }

  els.vcMembersList.innerHTML = '';
  members.forEach((m) => {
    const chip = document.createElement('div');
    chip.className = 'member-chip';

    // Find currently linked color for this user from state.playerMappings
    let linkedColor = '';
    if (state.playerMappings) {
      for (const [col, uid] of Object.entries(state.playerMappings)) {
        if (uid === m.userId && colorMap[col]) {
          linkedColor = col;
          break;
        }
      }
    }

    const cInfo = linkedColor ? getColorInfo(linkedColor) : null;
    const accentHex = cInfo ? cInfo.hex : '';
    if (accentHex) {
      chip.style.borderLeftColor = accentHex;
    } else {
      chip.style.borderLeftColor = 'transparent';
    }

    const avatar = document.createElement('div');
    avatar.className = 'member-avatar';
    if (m.avatarUrl) {
      avatar.style.backgroundImage = `url(${m.avatarUrl})`;
    } else {
      avatar.style.backgroundColor = accentHex || '#5865F2';
      avatar.textContent = (m.displayName || m.username).charAt(0).toUpperCase();
    }

    const details = document.createElement('div');
    details.className = 'member-details';

    // Header row: Member Name + Action Controls (Dropdown & Unmute)
    const headerRow = document.createElement('div');
    headerRow.className = 'member-header-row';

    const nameEl = document.createElement('div');
    nameEl.className = 'member-name';
    nameEl.textContent = m.displayName || m.username;
    nameEl.title = m.displayName || m.username;

    // Action Controls Container
    const actions = document.createElement('div');
    actions.className = 'member-actions';

    // Color Selector Dropdown (Link Color to this Member)
    const selectColor = document.createElement('select');
    selectColor.className = 'form-input form-input-sm member-color-select';
    selectColor.title = 'ゲーム内の色を紐付け（不参加・観戦は「登録解除・不参加」）';

    // Option 1: Unlink / Spectator
    const optNone = document.createElement('option');
    optNone.value = '';
    optNone.textContent = '🚫 登録解除・不参加';
    if (!linkedColor) optNone.selected = true;
    selectColor.appendChild(optNone);

    // Color Options (18 colors)
    for (const [colKey, info] of Object.entries(colorMap)) {
      const opt = document.createElement('option');
      opt.value = colKey;
      opt.textContent = info.label || colKey;
      if (linkedColor && stringsEqual(linkedColor, colKey)) {
        opt.selected = true;
      }
      selectColor.appendChild(opt);
    }

    // Color Change Event Listener
    selectColor.addEventListener('change', (e) => {
      e.stopPropagation();
      const val = selectColor.value;
      const userName = m.displayName || m.username;

      if (!val) {
        // Unlink user: immediately update local state
        if (state.playerMappings) {
          for (const [colKey, uid] of Object.entries(state.playerMappings)) {
            if (uid === m.userId) {
              delete state.playerMappings[colKey];
            }
          }
        }
        m.matchedName = '';
        m.matchedColor = '';
        lastRenderedVCJson = '';
        renderVoiceMembers(state.voiceMembers);

        fetch('/api/link/remove', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ userId: m.userId }),
        })
          .then((r) => r.json())
          .then(() => {
            addLog(`リンク解除: ${userName} を不参加に設定しました`, 'warn');
          })
          .catch((err) => addLog(`リンク解除エラー: ${err}`, 'error'));
      } else {
        // Set link: immediately update local state
        if (state.playerMappings) {
          for (const [colKey, uid] of Object.entries(state.playerMappings)) {
            if (uid === m.userId) {
              delete state.playerMappings[colKey];
            }
          }
          delete state.playerMappings[val];
          state.playerMappings[val] = m.userId;
        }
        m.matchedName = val;
        lastRenderedVCJson = '';
        renderVoiceMembers(state.voiceMembers);

        fetch('/api/link/set', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ color: val, userId: m.userId }),
        })
          .then((r) => r.json())
          .then((res) => {
            if (res.success) {
              addLog(`リンク登録完了: ${val} ↔ ${userName}`, 'success');
            } else {
              addLog(`リンク登録失敗: ${res.error || '不明なエラー'}`, 'error');
            }
          })
          .catch((err) => addLog(`リンク登録エラー: ${err}`, 'error'));
      }
    });

    // Unmute Button for individual member
    const btnUnmute = document.createElement('button');
    btnUnmute.className = 'btn btn-secondary btn-xs btn-unmute-member';
    btnUnmute.innerHTML = '🔊 解除';
    btnUnmute.title = `${m.displayName || m.username} のミュートを解除`;
    btnUnmute.addEventListener('click', (e) => {
      e.stopPropagation();
      fetch('/api/bot/unmute', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ userId: m.userId }),
      })
        .then((r) => r.json())
        .then(() => addLog(`${m.displayName || m.username} のミュートを解除しました`, 'success'))
        .catch((err) => addLog(`ミュート解除エラー: ${err}`, 'error'));
    });

    actions.appendChild(selectColor);
    actions.appendChild(btnUnmute);

    headerRow.appendChild(nameEl);
    headerRow.appendChild(actions);

    // Status Tags
    const tags = document.createElement('div');
    tags.className = 'tags';

    if (linkedColor) {
      const c = getColorInfo(linkedColor);
      tags.innerHTML += `<span class="tag tag-linked-color" style="border-left: 3px solid ${c.hex || '#38bdf8'};">🔗 ${c.label || linkedColor}</span>`;
    } else {
      tags.innerHTML += '<span class="tag tag-spectator">👁️ 不参加 / 観戦</span>';
    }

    if (m.isMuted) {
      tags.innerHTML += '<span class="tag tag-muted">🔇 Mute</span>';
    } else {
      tags.innerHTML += '<span class="tag tag-unmuted">🔊 Unmute</span>';
    }

    if (m.isDeafened) {
      tags.innerHTML += '<span class="tag tag-deaf">🔕 Deaf</span>';
    }

    details.appendChild(headerRow);
    details.appendChild(tags);

    chip.appendChild(avatar);
    chip.appendChild(details);
    els.vcMembersList.appendChild(chip);
  });
}

els.btnClearLogs.addEventListener('click', () => {
  els.logConsole.innerHTML = '';
});

els.btnRefreshOffsets.addEventListener('click', () => {
  addLog('リモートオフセット更新中...', 'info');
  fetch('/api/refresh-offsets', { method: 'POST' })
    .then((r) => r.json())
    .then(() => addLog('オフセット更新完了', 'success'));
});

// Notify app to exit completely when window is closed
window.addEventListener('beforeunload', () => {
  try {
    if (window.appExit) {
      window.appExit();
    }
    navigator.sendBeacon('/api/app/quit');
  } catch (e) {}
});
window.addEventListener('pagehide', () => {
  try {
    if (window.appExit) {
      window.appExit();
    }
    navigator.sendBeacon('/api/app/quit');
  } catch (e) {}
});

// Initialize on Load
connectWS();

