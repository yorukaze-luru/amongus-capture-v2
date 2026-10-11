# 🚀 AmongUsCapture v2 (Dedicated Client & Self-Hosted Discord Bot)

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Platform](https://img.shields.io/badge/Platform-Windows%20(x64)-brightgreen.svg)]()
[![Memory Usage](https://img.shields.io/badge/Memory%20Usage-~8MB-success.svg)]()
[![Single Binary](https://img.shields.io/badge/Single%20Binary-Zero%20Dependencies-purple.svg)]()

Among Us のゲーム状態を高精度・超低負荷でキャプチャし、**Discord ボイスチャンネルの自動ミュート・スピーカーミュート（Deafen）を完全自動化**する専用デスクトップアプリケーションです。外部サーバーは一切不要で、Bot Token を入力するだけで誰でも即座にセルフホスト運用できます。

---

## 🌟 主な特徴

### 1. 🖥️ 専用デスクトップウィンドウアプリ（マルチモニター・位置記憶対応）
- ブラウザ不要で、**独立した Windows 専用クライアントウィンドウ**（1040x780）として直接立ち上がります。
- 閉じた際の画面座標（サブモニター等）、ウィンドウサイズ、最大化状態を自動記憶し、次回起動時に正確に復元します。
- Windows 標準の WebView2 を利用し、追加ランタイムなしで美しいダークテーマ UI を提供します。

### 2. 🤖 完全セルフホスト Discord Bot エンジン内蔵
- 外部 AutoMuteUs サーバーや Docker / Redis / Postgres 等の環境構築は**一切不要**。
- Discord Developer Portal で作成した **Bot Token** を入力して「Bot起動」を押すだけで動作します（Tokenは自動永続化されます）。

### 3. 🛡️ ゲーム不参加者（観戦者・待機者）のミュート除外
- ボイスチャンネルにいるが Among Us のゲームに参加していないメンバーは、**ミュート・デミュートの対象から自動で除外**され、自由に会話・聴取が可能です。

### 4. 👻 ゴーストチャット（タスク中の死者通話）対応
- タスク中、生存者はミュート（Deaf）されますが、**死亡者（幽霊）同士は自由にリアルタイム会話**できます。
- 会議中は生存者のみ発言可能になり、死者は聞き専（Mute）に自動切り替えされます。

### 5. ⚡ 超軽量・リアルタイムリソースモニター内蔵（メモリ ~8MB / CPU 0.1%）
- ステータス欄に**このアプリ自身の物理メモリ使用量（Working Set）と CPU 使用率を毎秒リアルタイム表示**。
- メモリ消費量はわずか **約 8MB**（旧 WPF 版の約 150MB〜300MB から 95% 以上削減）。
- 起動時間は **0.05 秒以下**。単一の `AmongUsCapture-v2.exe` だけで動作します。

---

## 🎮 自動ミュートの挙動

| ゲーム状態 | 生存プレイヤー | 死亡プレイヤー (幽霊) | ゲーム不参加者 (観戦等) |
| :--- | :---: | :---: | :---: |
| **LOBBY / MENU (ロビー)** | 🔊 会話可能 | 🔊 会話可能 | 🔊 会話可能 |
| **TASKS (タスク作業中)** | 🔇 **ミュート (Deaf)** | 🔊 **会話可能 (幽霊通話)** | 🔊 **会話可能 (対象外)** |
| **DISCUSSION (緊急会議)** | 🔊 **会話可能** | 🔇 **ミュート (聞き専)** | 🔊 **会話可能 (対象外)** |
| **ENDED (ゲーム終了)** | 🔊 会話可能 | 🔊 会話可能 | 🔊 会話可能 |

---

## 📖 セットアップと使い方

### ステップ 1: Discord Bot の作成（初回のみ）
1. [Discord Developer Portal](https://discord.com/developers/applications) でアプリケーションを作成し、左メニューの **「Bot」** を開きます。
2. **「Privileged Gateway Intents」** で **Server Members Intent** を ON にします。
3. **「Reset Token」** を押して **Bot Token** をコピーします。
4. **「OAuth2」→「URL Generator」** で Scopes に `bot`（権限: `Mute Members`, `Deafen Members`, `View Channels`）を選択し、生成された URL から Bot を Discord サーバーに招待します。

### ステップ 2: アプリの起動と設定
1. **[`AmongUsCapture-v2.exe`](file:///c:/Users/a200y/Desktop/amongus-capture-v2/AmongUsCapture-v2.exe)** をダブルクリックして起動します。
2. コピーした **Bot Token** を貼り付けて **「Bot起動」** をクリックします。
3. **「対象サーバー」** と **「通話中のボイスチャンネル」** を選択し、**「VC追跡開始」** をクリックします。
4. Among Us をプレイすると、ゲーム進行に合わせて自動ミュートが実行されます！

---

## 💬 Discord Embed 直下のメニューによるプレイヤー紐付け

チャット欄にコマンドを入力する必要は**一切ありません**。  
通知チャンネルに自動表示される **リアルタイムステータス Embed の真下にあるドロップダウンメニュー** から、各々が自分の色を選択するだけで即座に紐付けされます：

- 🎨 **カラー選択**: メニューから自分の色（🔴 Red, 🔵 Blue 等）を選ぶと、自分の Discord アカウントとゲーム内の色が紐付けられます。
- 🚫 **登録解除・不参加**: ゲームに参加しない観戦者や待機者は「登録解除・不参加」を選ぶことで、自動ミュートの対象から除外されます。
- 🔒 **通知は自分だけに見える仕様（Ephemeral）**: メニューを選択した際の通知は**操作した本人にだけ表示**され、チャンネル内の他のメンバーには見えないため、Discord のチャット欄が荒れません。
- 🧹 **スラッシュコマンド廃止**: コマンド入力の手間をなくし、誤爆や重複を防ぐため、すべてのスラッシュコマンドは完全廃止・自動削除されます。

## 🛠️ コマンドライン引数

```bash
# 通常起動（専用デスクトップウィンドウ表示）
.\AmongUsCapture-v2.exe

# 外部ブラウザで開く場合
.\AmongUsCapture-v2.exe -browser

# ヘッドレスモード（ウィンドウなしでバックグラウンド実行）
.\AmongUsCapture-v2.exe -headless

# ポート番号を手動指定
.\AmongUsCapture-v2.exe -port 8123
```

---

## 🛠️ ソースコードからのビルド方法

```bash
# Windows GUI サブシステム（ターミナル非表示）で最適化ビルド
go build -ldflags="-s -w -H windowsgui" -o AmongUsCapture-v2.exe ./cmd/aucapture
```

---

## 📁 プロジェクト構成

```
amongus-capture-v2/
├── AmongUsCapture-v2.exe     # 専用デスクトップアプリ単体バイナリ (~8.4MB)
├── cmd/
│   └── aucapture/
│       └── main.go          # ネイティブウィンドウGUI起動 & CLI
├── pkg/
│   ├── bot/                 # セルフホスト Discord Bot エンジン (Mute/Deaf & 不参加者除外)
│   ├── config/              # 設定永続化 (Token, Guild, Mappings)
│   ├── memory/              # Win32 メモリキャプチャ & AOB シグネチャスキャン
│   └── web/                 # 組み込み UI (WebView2 レンダリング用)
├── go.mod
├── go.sum
└── README.md
```

---

## 📄 ライセンス
MIT License
