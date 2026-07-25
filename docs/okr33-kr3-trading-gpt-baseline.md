# OKR-33 KR3 Trading-GPT Baseline

Date: 2026-07-25 SGT / 2026-07-24 UTC

Status: `KR3_BASELINE_READY_FOR_PAPER_HTX_ADAPTER`

This is a reproducible build/test baseline only. It does not claim HTX support, exchange connectivity, authenticated trading, funding, borrowing, KYC, registration, or order placement.

## Checkout

- Path: `/Users/sulabs_001/workspace/trading-gpt`
- Upstream: `git@github.com:algonius/trading-gpt.git`
- Branch: `main`, tracking `origin/main`
- Commit: `f21cf608c4fbe3c470f383988f19fde6339934ee`
- Commit subject: `fix: add JSON mode and DeepSeek compatibility for OpenAI provider (#76)`
- Submodules:
  - `libs/bbgo`: `a45ad83e41f661907dfe4b40a43a99a1e84f24f3` (`remotes/origin/issue73/okex-reduceonly`)
  - `libs/chatgpt`: `5d1e56ed0023ca03fe793409b99687bccda41adb` (`v0.3.4`)

## Repository Shape

- Go module: `github.com/yubing744/trading-gpt`
- Declared Go version: `go 1.22.0`
- Main entrypoint: `main.go`
- Build target: `make build` -> `CGO_ENABLED=0 go build -o ./build/bbgo ./main.go`
- Unit-test target: `make unit-test` -> `go test ./pkg/...`
- Local replacement dependencies:
  - `github.com/c9s/bbgo => ./libs/bbgo`
  - `github.com/yubing744/chatgpt-go => ./libs/chatgpt`

## Safety Boundary

The baseline explicitly unset exchange, LLM, Twitter, Coze, Feishu, Slack, and notification credential variables before each run. The recorded check printed `credential vars absent` in both runs.

Commands intentionally not run:

- `make run`
- `docker-*` or release targets
- `go test ./test/...`
- Any command that loads `.env.local`, configures an authenticated exchange session, submits/cancels orders, funds/borrows, or uses exchange API keys

Known unsafe tests excluded from the baseline:

- `test/integration/exchange_entity_test.go` loads `../../.env.local`, configures an OKEx session, queries live ticker/session state, and calls open/close position paths.
- `test/integration/orders_cmd_test.go` includes `submit-order --session=okex --symbol=OPUSDT --side=buy --market=true --quantity=1`.

## Environment

- Host Go: `go version go1.26.0 darwin/arm64`
- Baseline Go: `go version go1.23.12 darwin/arm64`
- Baseline invocation: direct toolchain binary at `/Users/sulabs_001/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.23.12.darwin-arm64/bin/go`
- `GOTOOLCHAIN=local`
- Host: `Darwin sulabs.local 25.5.0 Darwin Kernel Version 25.5.0: Tue Jun 9 22:26:22 PDT 2026; root:xnu-12377.121.10~1/RELEASE_ARM64_T8132 arm64`

## Reproducible Command Template

For each run, use a new `runN` cache path:

```bash
GO_BIN=/Users/sulabs_001/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.23.12.darwin-arm64/bin/go
export PATH=/Users/sulabs_001/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.23.12.darwin-arm64/bin:$PATH
export GOTOOLCHAIN=local
export GOMODCACHE=$PWD/.okr33-baseline/cache/runN/gomod
export GOCACHE=$PWD/.okr33-baseline/cache/runN/gocache

unset OKEX_API_KEY OKEX_API_SECRET OKEX_API_PASSPHRASE OKEX_APIKEY OKEX_SECRET_KEY OKEX_PASSPHRASE
unset HTX_API_KEY HTX_API_SECRET BINANCE_API_KEY BINANCE_API_SECRET
unset LLM_GOOGLEAI_APIKEY LLM_OPENAI_TOKEN LLM_ANTHROPIC_TOKEN ANTHROPIC_API_KEY OPENAI_API_KEY GOOGLE_API_KEY
unset TWITTER_API_KEY COZE_API_KEY
unset NOTIFY_FEISHU_APP_ID NOTIFY_FEISHU_APP_SECRET NOTIFY_FEISHU_HOOK_URL
unset CHAT_FEISHU_APP_ID CHAT_FEISHU_APP_SECRET CHAT_FEISHU_EVENT_ENCRYPT_KEY CHAT_FEISHU_VERIFICATION_TOKEN
unset FEISHU_WEBHOOK_URL SLACK_BOT_TOKEN SLACK_WEBHOOK_URL

$GO_BIN mod download
make build
make unit-test
```

## Two-Run Result

| Run | UTC window | Cache paths | Result |
| --- | --- | --- | --- |
| 1 | `2026-07-24T16:36:14Z` to `2026-07-24T16:39:42Z` | `.okr33-baseline/cache/run1/{gomod,gocache}` | PASS: `go mod download`, `make build`, `make unit-test` |
| 2 | `2026-07-24T16:40:24Z` to `2026-07-24T16:53:40Z` | `.okr33-baseline/cache/run2/{gomod,gocache}` | PASS: `go mod download`, `make build`, `make unit-test` |

Representative unit-test output from both runs:

```text
ok  	github.com/yubing744/trading-gpt/pkg/apis/alternative
ok  	github.com/yubing744/trading-gpt/pkg/apis/coze
ok  	github.com/yubing744/trading-gpt/pkg/apis/twitterapi
ok  	github.com/yubing744/trading-gpt/pkg/chat/feishu
ok  	github.com/yubing744/trading-gpt/pkg/env
ok  	github.com/yubing744/trading-gpt/pkg/memory
ok  	github.com/yubing744/trading-gpt/pkg/types
ok  	github.com/yubing744/trading-gpt/pkg/utils
ok  	github.com/yubing744/trading-gpt/pkg/utils/xtemplate
```

Artifacts:

- Baseline config script: `scripts/okr33-baseline.sh`
- Credential-free CI workflow: `.github/workflows/okr33-baseline.yml`
- Dependency/license audit: `docs/okr33-kr3-dependency-license-audit.md`
- Run 1 log: `.okr33-baseline/logs/run1-go1.23.12.log`
- Run 2 log: `.okr33-baseline/logs/run2-go1.23.12.log`
- Local CI-equivalent retry log: `.okr33-baseline/logs/local-ci-check-go1.23.12-retry.log`
- Transient provider failure log: `.okr33-baseline/logs/local-ci-check-go1.23.12-failure.log`
- Run 1 log SHA-256: `4a2a349be65d3fcc559b75c3fe509f1bfae01a84008e9cd604e0f4917ce7f10b`
- Run 2 log SHA-256: `03297e41b3b5ef986d2f614c83bba5241ba895231e2c68cf11a3cdca0a2b3b49`
- Local CI-equivalent retry log SHA-256: `160d46b4622440611161120387d2d1e39f78cba6d556faba8e61b412c7a87043`
- Transient provider failure log SHA-256: `88e4dbff66508a443e0c3bf966d624e73f9e13590173b838bf6141f410b2d5f1`
- Final built binary SHA-256, computed after Run 2 before cleanup: `7d2ccc9ce2ebcf27028628fbf738f76bf613afd044bfa1e5389052ea1a729a89`
- Local CI-equivalent retry binary SHA-256, computed before cleanup: `40091d025f9f477162bc47a7b37b3c84eed9ea45b80a1916020a966605fbc5f4`

No provider failure occurred during the final two Go 1.23.12 fresh-cache runs.

During PR packaging, one local CI-equivalent check failed in `go mod download` with:

```text
go: cloud.google.com/go@v0.115.0: Get "https://proxy.golang.org/cloud.google.com/go/@v/v0.115.0.mod": tls: received record with version 5117 when expecting version 303
```

An independent fresh-cache retry passed `go mod download`, `make build`, and `make unit-test`, so that TLS error is classified as transient environment/provider behavior rather than a reproducible baseline blocker.

## License Findings

- The checkout has no root `LICENSE`, `COPYING`, or `NOTICE` file.
- GitHub repository metadata observed during baseline setup reported `licenseInfo=null`.
- Submodule license files exist:
  - `libs/bbgo/LICENSE`: GNU Affero General Public License v3.0
  - `libs/chatgpt/LICENSE`: MIT License

Absent root/upstream license metadata is a documented risk, not permission. Any KR4 adapter work should treat distribution/reuse of this baseline as blocked on repository owner license clarification or counsel-approved internal-use constraints.

## Recommendation

Use commit `f21cf608c4fbe3c470f383988f19fde6339934ee` as the reproducible baseline for a paper-only HTX adapter investigation. Keep KR4 entry limited to unauthenticated build/test and paper adapter design until license risk and safe paper-mode boundaries are resolved.
