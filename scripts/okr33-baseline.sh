#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

GO_BIN="${GO_BIN:-go}"
RUN_LABEL="${RUN_LABEL:-baseline}"
CACHE_ROOT="${CACHE_ROOT:-$repo_root/.okr33-baseline/cache}"
RUN_CACHE="$CACHE_ROOT/$RUN_LABEL"

if [[ "$GO_BIN" == */* ]]; then
  export PATH="$(cd "$(dirname "$GO_BIN")" && pwd):$PATH"
fi

credential_vars=(
  OKEX_API_KEY
  OKEX_API_SECRET
  OKEX_API_PASSPHRASE
  OKEX_APIKEY
  OKEX_SECRET_KEY
  OKEX_PASSPHRASE
  HTX_API_KEY
  HTX_API_SECRET
  BINANCE_API_KEY
  BINANCE_API_SECRET
  LLM_GOOGLEAI_APIKEY
  LLM_OPENAI_TOKEN
  LLM_ANTHROPIC_TOKEN
  ANTHROPIC_API_KEY
  OPENAI_API_KEY
  GOOGLE_API_KEY
  TWITTER_API_KEY
  COZE_API_KEY
  NOTIFY_FEISHU_APP_ID
  NOTIFY_FEISHU_APP_SECRET
  NOTIFY_FEISHU_HOOK_URL
  CHAT_FEISHU_APP_ID
  CHAT_FEISHU_APP_SECRET
  CHAT_FEISHU_EVENT_ENCRYPT_KEY
  CHAT_FEISHU_VERIFICATION_TOKEN
  FEISHU_WEBHOOK_URL
  SLACK_BOT_TOKEN
  SLACK_WEBHOOK_URL
)

for var_name in "${credential_vars[@]}"; do
  unset "$var_name"
done

credential_pattern='^(OKEX|HTX|BINANCE|LLM_|ANTHROPIC|OPENAI|GOOGLE_API|TWITTER_API|COZE_API|NOTIFY_|CHAT_FEISHU|FEISHU_|SLACK_)'
if env | sort | grep -E "$credential_pattern" >/dev/null; then
  echo "credential environment variables remain after unset" >&2
  env | sort | grep -E "$credential_pattern" | sed 's/=.*$/=<redacted>/' >&2
  exit 1
fi

if [ -d "$RUN_CACHE" ]; then
  chmod -R u+w "$RUN_CACHE" 2>/dev/null || true
  rm -rf "$RUN_CACHE"
fi

mkdir -p "$RUN_CACHE/gomod" "$RUN_CACHE/gocache"

export GOTOOLCHAIN=local
export GOMODCACHE="$RUN_CACHE/gomod"
export GOCACHE="$RUN_CACHE/gocache"

echo "credential vars absent"
"$GO_BIN" version
"$GO_BIN" env GOVERSION GOTOOLCHAIN GOMODCACHE GOCACHE GOOS GOARCH CGO_ENABLED

"$GO_BIN" mod download
make build
make unit-test
