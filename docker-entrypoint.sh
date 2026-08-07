#!/bin/bash
set -euo pipefail

# Recreate the ephemeral runtime layout before starting the server or bundled
# agent tools. Durable state lives in Postgres and S3.
mkdir -p \
  /var/lib/factory/home \
  /var/lib/factory/codex \
  /var/lib/factory/projects \
  /var/lib/factory/workflows

# When a broker is configured, make GitHub CLI and HTTPS Git operations use
# short-lived installation tokens. The client validates all broker settings
# here so a bad secret mount fails before any agent process starts.
if [[ -n "${GITHUB_TOKEN_BROKER_URL:-}" ]]; then
  github-token-client configure-git
fi

if [[ -z "${TUNNEL_TOKEN:-}" ]]; then
  exec "$@"
fi

# A remotely managed Cloudflare Tunnel maps its public hostname and external
# intake path to this server. Keep the token in the environment so it never
# appears in the process list. If either process stops, stop the other so the
# container cannot report healthy while its public intake is unavailable.
"$@" &
server_pid=$!
cloudflared tunnel --no-autoupdate --loglevel info run &
tunnel_pid=$!

stop_children() {
  trap - TERM INT
  kill -TERM "$server_pid" "$tunnel_pid" 2>/dev/null || true
}
trap stop_children TERM INT

set +e
wait -n "$server_pid" "$tunnel_pid"
status=$?
set -e
stop_children
wait "$server_pid" 2>/dev/null || true
wait "$tunnel_pid" 2>/dev/null || true

# Both processes are long lived. A clean early exit still means the container
# lost one half of the service and should be restarted.
if (( status == 0 )); then
  status=1
fi
exit "$status"
