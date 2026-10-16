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

### 4. 👻 ネタバレ防止・仮死状態制御 ＆ 幽霊通話（ゴーストチャット）
- **仮死状態（タスク中のキル直後）**: タスク中にキルされても即座にアンミュートせず、会議が始まるまでは**仮死状態としてミュート（Deaf）を維持**し、ネタバレを防ぎます。
- **死亡状態（会議移行後・追放後）**: 緊急会議（DISCUSSION）中は聞き専（Mute）となり、次のタスクフェーズ（TASKS）からは**死亡確定した幽霊同士で自由にリアルタイム会話**できます（生存者・仮死者は Deaf のため幽霊の声は聞こえません）。

### 5. ⚡ 超軽量・リアルタイムリソースモニター内蔵（メモリ ~8MB / CPU 0.1%）
- ステータス欄に**このアプリ自身の物理メモリ使用量（Working Set）と CPU 使用率を毎秒リアルタイム表示**。
- メモリ消費量はわずか **約 8MB**（旧 WPF 版の約 150MB〜300MB から 95% 以上削減）。
- 起動時間は **0.05 秒以下**。単一の `AmongUsCapture-v2.exe` だけで動作します。

### 6. 🔄 起動時の完全自動セルフアップデート機能
- アプリ起動時に GitHub Releases の最新バージョンを自動確認し、新しいリリースが存在する場合は**自動で最新の `AmongUsCapture-v2.exe` をダウンロード・自己差し替えして再起動**します。
- 設定ファイル（`config.json`）はそのまま引き継がれ、古い実行ファイル（`.old`）も次回起動時に自動でクリーンアップされます。

---

## 🎮 自動ミュートの挙動

| ゲーム状態 | 生存プレイヤー | 仮死状態 (タスク中キル) | 死亡状態 (幽霊・死亡確定) | ゲーム不参加者 (観戦等) |
| :--- | :---: | :---: | :---: | :---: |
| **LOBBY / MENU (ロビー)** | 🔊 会話可能 | - | - | 🔊 会話可能 |
| **TASKS (タスク作業中)** | 🔇 **ミュート (Deaf)** | 🔇 **ミュート維持 (Deaf)** | 🔊 **会話可能 (幽霊通話)** | 🔊 **会話可能 (対象外)** |
| **DISCUSSION (緊急会議)** | 🔊 **会話可能** | *(会議開始で死亡状態へ移行)* | 🔇 **ミュート (聞き専)** | 🔊 **会話可能 (対象外)** |
| **ENDED (ゲーム終了)** | 🔊 会話可能 | 🔊 会話可能 | 🔊 会話可能 | 🔊 会話可能 |

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

- 🎨 **カラー選択（ロビー待機中限定）**: メニューから自分の色（🔴 Red, 🔵 Blue 等）を選ぶと、自分の Discord アカウントとゲーム内の色が紐付けられます（※ゲーム中やメニュー画面では誤爆・不正防止のため自動でメニューがロックされます）。
- 🚫 **登録解除・不参加**: ゲームに参加しない観戦者や待機者は「登録解除・不参加」を選ぶことで、自動ミュートの対象から除外されます。
- 🔒 **通知は自分だけに見える仕様（Ephemeral）**: メニューを選択した際の通知は**操作した本人にだけ表示**され、チャンネル内の他のメンバーには見えないため、Discord のチャット欄が荒れません。
- 🧹 **スラッシュコマンド廃止**: コマンド入力の手間をなくし、誤爆や重複を防ぐため、すべてのスラッシュコマンドは完全廃止・自動削除されます。

## 🛠️ コマンドライン引数

```bash
# 通常起動（専用デスクトップウィンドウ表示・自動アップデート有効）
.\AmongUsCapture-v2.exe

# 起動時の自動アップデートを無効化する場合
.\AmongUsCapture-v2.exe -no-update

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

## 📬 連絡先
- **開発・お問い合わせ**: 夜風るる（ https://x.com/Yorukaze_LURU ）

---

## 📄 ライセンス
[MIT License](LICENSE) © 2026 yorukaze luru
