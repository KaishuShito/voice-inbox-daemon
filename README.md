# voice-inbox-daemon

個人用の、ちいさな音声インボックス。

Discord の `#voice-input` に投げた音声メモを、5分おきに拾って `whisper` で文字起こしし、Obsidian Journal に追記します。

![bot avatar](./assets/bot-avatar.png)

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
- ✅ リアクションで処理済みマーキング
- launchdで常駐（5分ポーリング + 日次cleanup）

## Quick start

```bash
cd /Users/kai/Develop/voice-inbox-daemon
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
```

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
