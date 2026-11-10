#!/bin/sh
# Read-only reporting entrypoint for the VPS operator.
set -eu
env_file="${VIDSAVE_STATS_ENV_FILE:-/home/chatgpt-work/.config/vidsave/readonly.env}"
if [ ! -r "$env_file" ]; then
  echo "Read-only metrics credentials are not configured" >&2
  exit 2
fi
set -a
. "$env_file"
set +a
cd /root/project/xa4y_vidsave_bot
exec go run ./cmd/stats
