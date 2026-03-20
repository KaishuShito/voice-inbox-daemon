# OPERATIONS

以下は `voice-inbox-daemon` リポジトリのルートを `PROJECT_DIR` とした例です。

```bash
PROJECT_DIR="/path/to/voice-inbox-daemon"
```

## 日常運用

1. 定期処理確認:
```bash
launchctl print "gui/$UID/com.kai.voice-inbox.poll" | head -n 40
```
2. 状態確認:
```bash
"$PROJECT_DIR/dist/voice-inbox" status --json
```
3. 失敗再処理:
```bash
"$PROJECT_DIR/dist/voice-inbox" retry --json
```

通常は `poll` が5分ごとに retry due を自動で吸い上げます。`retry` は即時に回復させたい時の手動実行用です。

## HTTP ingest / serve

Android Voice Inbox の backend として使う時は `serve` を起動します。

```bash
INGEST_AUTH_TOKEN=... \
INGEST_LISTEN_ADDR=127.0.0.1:8787 \
"$PROJECT_DIR/dist/voice-inbox" serve
```

追加の ingest env:

- `INGEST_MAX_BODY_MB` 既定 `32`
- `INGEST_SOURCE_NAME` 既定 `android-voice-inbox`

`serve` は `POST /v0/captures` を受け付け、raw file 保存と SQLite の durable registration の両方が終わるまで成功を返しません。

## トラブル時の確認順

1. `doctor` 実行
```bash
"$PROJECT_DIR/dist/voice-inbox" doctor --json
```
2. ログ確認
```bash
ls -la "$HOME/Library/Logs/voice-inbox-daemon"
tail -n 200 "$HOME/Library/Logs/voice-inbox-daemon/poll.err.log"
```
3. SQLite 状態確認
```bash
sqlite3 "$HOME/Library/Application Support/voice-inbox-daemon/state.db" \
  "select status,count(*) from messages group by status;"
```

## よくある原因

- `401/403` (Discord): Bot token 権限不足 or 誤設定
- `401` (Obsidian): API key/header 不一致
- `whisper failed`: モデル未キャッシュ or 入力音声形式異常
- `reaction_pending` が増える: Discord API 一時障害

## 手動1サイクル実行

```bash
"$PROJECT_DIR/dist/voice-inbox" poll --once --json
```

## Cleanup 手動実行

```bash
"$PROJECT_DIR/dist/voice-inbox" cleanup --json
```

## リリース更新手順

1. 変更後にテスト
```bash
cd "$PROJECT_DIR"
go test ./...
```
2. ビルド
```bash
go build -o ./dist/voice-inbox ./cmd/voice-inbox
```
3. launchd 再起動
```bash
launchctl kickstart -k "gui/$UID/com.kai.voice-inbox.poll"
```
