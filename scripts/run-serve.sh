#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."
set -a
source .env
set +a
# Expand ~ in path variables
export STATE_DB_PATH="${STATE_DB_PATH/#\~//Users/kai}"
export AUDIO_STORE_DIR="${AUDIO_STORE_DIR/#\~//Users/kai}"
export LOG_DIR="${LOG_DIR/#\~//Users/kai}"
exec ./dist/voice-inbox serve
