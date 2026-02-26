#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
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

PAYLOAD_FILE="$(mktemp)"
AVATAR_FILE="$(mktemp)"
trap 'rm -f "$PAYLOAD_FILE" "$AVATAR_FILE"' EXIT

{
  printf 'data:image/png;base64,'
  base64 < "$IMAGE_PATH" | tr -d '\n'
} > "$AVATAR_FILE"

python3 - "$BOT_NAME" "$AVATAR_FILE" "$PAYLOAD_FILE" <<'PY'
import json
import sys

username = sys.argv[1]
avatar_path = sys.argv[2]
out = sys.argv[3]
with open(avatar_path, "r", encoding="utf-8") as f:
    avatar = f.read()
with open(out, "w", encoding="utf-8") as f:
    json.dump({"username": username, "avatar": avatar}, f, ensure_ascii=False)
PY

STATUS=$(curl -sS -o /tmp/voice_inbox_avatar_resp.json -w "%{http_code}" \
  -X PATCH "https://discord.com/api/v10/users/@me" \
  -H "Authorization: Bot $TOKEN" \
  -H "Content-Type: application/json" \
  --data-binary @"$PAYLOAD_FILE")

if [[ "$STATUS" == "200" ]]; then
  echo "updated bot avatar successfully"
else
  echo "failed to update bot avatar: http $STATUS"
  head -c 400 /tmp/voice_inbox_avatar_resp.json; echo
  exit 1
fi
