#!/usr/bin/env bash
set -euo pipefail

PROJECT_DIR="/Users/kai/Develop/voice-inbox-daemon"
ENV_FILE="$PROJECT_DIR/.env"
IMAGE_PATH="${1:-$PROJECT_DIR/assets/bot-avatar.png}"
BOT_NAME="${2:-voice-inbox}"

if [[ ! -f "$ENV_FILE" ]]; then
  echo "[warn] .env がありません: $ENV_FILE"
  exit 1
fi

if [[ ! -f "$IMAGE_PATH" ]]; then
  echo "[warn] 画像がありません: $IMAGE_PATH"
  exit 1
fi

TOKEN="$(awk -F= '/^DISCORD_BOT_TOKEN=/{print $2}' "$ENV_FILE" | head -n1)"
if [[ -z "$TOKEN" ]]; then
  echo "[warn] DISCORD_BOT_TOKEN が未設定です"
  exit 1
fi

AVATAR_DATA_URI="data:image/png;base64,$(base64 < "$IMAGE_PATH" | tr -d '\n')"

STATUS=$(curl -sS -o /tmp/voice_inbox_avatar_resp.json -w "%{http_code}" \
  -X PATCH "https://discord.com/api/v10/users/@me" \
  -H "Authorization: Bot $TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"$BOT_NAME\",\"avatar\":\"$AVATAR_DATA_URI\"}")

if [[ "$STATUS" == "200" ]]; then
  echo "updated bot avatar successfully"
else
  echo "failed to update bot avatar: http $STATUS"
  head -c 400 /tmp/voice_inbox_avatar_resp.json; echo
  exit 1
fi
