#!/bin/sh
set -eu

if [ -z "${GITHUB_TOKEN_BROKER_URL:-}" ]; then
  exec /usr/local/libexec/gh "$@"
fi

exec /usr/local/bin/github-token-client exec -- /usr/local/libexec/gh "$@"
