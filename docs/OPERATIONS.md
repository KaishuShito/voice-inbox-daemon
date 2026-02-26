# OPERATIONS

## 日常運用

1. 定期処理確認:
```bash
launchctl print "gui/$UID/com.kai.voice-inbox.poll" | head -n 40
```
2. 状態確認:
```bash
/Users/kai/Develop/voice-inbox-daemon/dist/voice-inbox status --json
```
3. 失敗再処理:
```bash
/Users/kai/Develop/voice-inbox-daemon/dist/voice-inbox retry --json
```

## トラブル時の確認順

1. `doctor` 実行
```bash
/Users/kai/Develop/voice-inbox-daemon/dist/voice-inbox doctor --json
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
/Users/kai/Develop/voice-inbox-daemon/dist/voice-inbox poll --once --json
```

## Cleanup 手動実行

```bash
/Users/kai/Develop/voice-inbox-daemon/dist/voice-inbox cleanup --json
```

## リリース更新手順

1. 変更後にテスト
```bash
cd /Users/kai/Develop/voice-inbox-daemon
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
