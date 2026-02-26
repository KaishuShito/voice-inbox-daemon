#!/usr/bin/env bash
set -euo pipefail

PROJECT_DIR="/Users/kai/Develop/voice-inbox-daemon"
LAUNCH_AGENTS_DIR="$HOME/Library/LaunchAgents"
LOG_DIR="$HOME/Library/Logs/voice-inbox-daemon"
POLL_LABEL="com.kai.voice-inbox.poll"
CLEANUP_LABEL="com.kai.voice-inbox.cleanup"

mkdir -p "$LAUNCH_AGENTS_DIR" "$LOG_DIR" "$PROJECT_DIR/dist"

if [[ ! -f "$PROJECT_DIR/.env" ]]; then
  echo "[warn] .env が見つかりません。先に .env.example から .env を作成してください。"
  exit 1
fi

chmod 600 "$PROJECT_DIR/.env"

required_keys=("DISCORD_BOT_TOKEN" "OBSIDIAN_API_KEY")
for key in "${required_keys[@]}"; do
  value="$(awk -F= -v k="$key" '$1==k {sub(/^[ \t]+/, "", $2); print $2}' "$PROJECT_DIR/.env" | head -n1)"
  if [[ -z "${value:-}" ]]; then
    echo "[warn] .env の $key が未設定です。設定してから再実行してください。"
    exit 1
  fi
done

(
  cd "$PROJECT_DIR"
  go build -o ./dist/voice-inbox ./cmd/voice-inbox
)

cp "$PROJECT_DIR/launchd/${POLL_LABEL}.plist" "$LAUNCH_AGENTS_DIR/${POLL_LABEL}.plist"
cp "$PROJECT_DIR/launchd/${CLEANUP_LABEL}.plist" "$LAUNCH_AGENTS_DIR/${CLEANUP_LABEL}.plist"

launchctl bootout "gui/$UID" "$LAUNCH_AGENTS_DIR/${POLL_LABEL}.plist" >/dev/null 2>&1 || true
launchctl bootout "gui/$UID" "$LAUNCH_AGENTS_DIR/${CLEANUP_LABEL}.plist" >/dev/null 2>&1 || true

launchctl bootstrap "gui/$UID" "$LAUNCH_AGENTS_DIR/${POLL_LABEL}.plist"
launchctl bootstrap "gui/$UID" "$LAUNCH_AGENTS_DIR/${CLEANUP_LABEL}.plist"

launchctl kickstart -k "gui/$UID/$POLL_LABEL"

echo "Installed launchd jobs:"
launchctl print "gui/$UID/$POLL_LABEL" | head -n 20 || true
launchctl print "gui/$UID/$CLEANUP_LABEL" | head -n 20 || true
