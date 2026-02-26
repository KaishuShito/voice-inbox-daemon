#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
ENV_FILE="$PROJECT_DIR/.env"

if [[ ! -f "$ENV_FILE" ]]; then
  echo "[warn] $ENV_FILE がありません。先に .env.example から .env を作成してください。"
  exit 1
fi

token="${1:-}"
if [[ -z "$token" ]]; then
  read -r -s -p "Discord Bot Token: " token
  echo
fi

if [[ -z "$token" ]]; then
  echo "[warn] token が空です。"
  exit 1
fi

escaped_token=$(printf '%s' "$token" | sed 's/[&/\\]/\\&/g')
perl -0777 -i -pe "s|^DISCORD_BOT_TOKEN=.*$|DISCORD_BOT_TOKEN=${escaped_token}|m" "$ENV_FILE"
chmod 600 "$ENV_FILE"

echo "updated: DISCORD_BOT_TOKEN=[SET]"
