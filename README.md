# voice-inbox-daemon

![hero](./assets/hero.png)

個人用の、ちいさな音声インボックス。

Discord の `#voice-input` に投げた音声メモを、5分おきに拾って `whisper` で文字起こしし、Obsidian Journal に追記します。Android からの HTTP 音声アップロードにも対応。

## Why

- 思いついた瞬間にしゃべる
- ローカルに蓄積する
- 後から見返せる

それだけです。

## What it does

- Go製の単独CLI (`voice-inbox`)
- Discord APIで新着音声を取得（チャンネルID + 投稿者ID + `audio/*`）
- `ffmpeg` で正規化 → `whisper` で転写
- Obsidian Local REST API へ Journal 追記
- SQLiteで重複防止 / retryキュー / 状態管理
- 5分ごとの poll で期限到来した retry も自動再処理
- `serve` で Android などからの HTTP 音声アップロードも受け付け
- ✅ リアクションで処理済みマーキング
- launchdで常駐（5分ポーリング + 日次cleanup）

## Quick start

```bash
git clone https://github.com/KaishuShito/voice-inbox-daemon.git
cd voice-inbox-daemon
cp .env.example .env
chmod 600 .env

# Discord tokenを設定
./scripts/set-discord-token.sh

# 疎通確認
./dist/voice-inbox doctor --json

# 1回実行
./dist/voice-inbox poll --once --json

# 常駐化
./scripts/install-launchd.sh
```

## Commands

```bash
./dist/voice-inbox doctor --json
./dist/voice-inbox poll --once --json
./dist/voice-inbox retry --json
./dist/voice-inbox cleanup --json
./dist/voice-inbox status --json
./dist/voice-inbox serve
```

## HTTP ingest (v0.0.1)

Android Voice Inbox 向けの最小 ingest endpoint:

- `POST /v0/captures`
- `Authorization: Bearer $INGEST_AUTH_TOKEN`
- `multipart/form-data`
- fields: `audio`, `capture_id`, `device_id`, `captured_at`

主な env:

- `INGEST_LISTEN_ADDR` 既定 `127.0.0.1:8787`
- `INGEST_AUTH_TOKEN` 必須
- `INGEST_MAX_BODY_MB` 既定 `32`
- `INGEST_SOURCE_NAME` 既定 `android-voice-inbox`

`serve` は raw file の永続化と SQLite 登録の両方が成功するまで ACK を返しません。同じ `capture_id` は idempotent に扱います。

## Bot avatar

生成済みアイコン:

- `assets/bot-avatar.png` (default)
- `assets/voice-inbox-bot-ambient-v1.png`
- `assets/voice-inbox-bot-ambient-v2.png`
- `assets/voice-inbox-bot-ambient-v3.png`

Discord bot に反映:

```bash
./scripts/set-discord-avatar.sh
# or
./scripts/set-discord-avatar.sh ./assets/voice-inbox-bot-ambient-v3.png voice-inbox
```

## Notes

- `.env` はコミットしません
- `OBSIDIAN_VERIFY_TLS=false` はローカル用途前提
- 詳細運用は `docs/OPERATIONS.md`

## License

MIT
