#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
LAUNCH_AGENTS_DIR="$HOME/Library/LaunchAgents"
LOG_DIR="$HOME/Library/Logs/voice-inbox-daemon"
POLL_LABEL="com.kai.voice-inbox.poll"
CLEANUP_LABEL="com.kai.voice-inbox.cleanup"
POLL_TEMPLATE="$PROJECT_DIR/launchd/${POLL_LABEL}.plist"
CLEANUP_TEMPLATE="$PROJECT_DIR/launchd/${CLEANUP_LABEL}.plist"
POLL_TARGET="$LAUNCH_AGENTS_DIR/${POLL_LABEL}.plist"
CLEANUP_TARGET="$LAUNCH_AGENTS_DIR/${CLEANUP_LABEL}.plist"

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

python3 - "$POLL_TEMPLATE" "$POLL_TARGET" "$PROJECT_DIR" <<'PY'
import pathlib
import sys

template = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
out_path = pathlib.Path(sys.argv[2])
project_dir = sys.argv[3]
out_path.write_text(template.replace("__PROJECT_DIR__", project_dir), encoding="utf-8")
PY

python3 - "$CLEANUP_TEMPLATE" "$CLEANUP_TARGET" "$PROJECT_DIR" <<'PY'
import pathlib
import sys

template = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
out_path = pathlib.Path(sys.argv[2])
project_dir = sys.argv[3]
out_path.write_text(template.replace("__PROJECT_DIR__", project_dir), encoding="utf-8")
PY

launchctl bootout "gui/$UID" "$POLL_TARGET" >/dev/null 2>&1 || true
launchctl bootout "gui/$UID" "$CLEANUP_TARGET" >/dev/null 2>&1 || true

launchctl bootstrap "gui/$UID" "$POLL_TARGET"
launchctl bootstrap "gui/$UID" "$CLEANUP_TARGET"

launchctl kickstart -k "gui/$UID/$POLL_LABEL"

echo "Installed launchd jobs:"
launchctl print "gui/$UID/$POLL_LABEL" | head -n 20 || true
launchctl print "gui/$UID/$CLEANUP_LABEL" | head -n 20 || true
