#!/usr/bin/env bash
set -euo pipefail

LAUNCH_AGENTS_DIR="$HOME/Library/LaunchAgents"
POLL_LABEL="com.kai.voice-inbox.poll"
CLEANUP_LABEL="com.kai.voice-inbox.cleanup"

launchctl bootout "gui/$UID" "$LAUNCH_AGENTS_DIR/${POLL_LABEL}.plist" >/dev/null 2>&1 || true
launchctl bootout "gui/$UID" "$LAUNCH_AGENTS_DIR/${CLEANUP_LABEL}.plist" >/dev/null 2>&1 || true

rm -f "$LAUNCH_AGENTS_DIR/${POLL_LABEL}.plist" "$LAUNCH_AGENTS_DIR/${CLEANUP_LABEL}.plist"

echo "Uninstalled launchd jobs: $POLL_LABEL, $CLEANUP_LABEL"
