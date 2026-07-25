# OKR-33 KR3 Dependency and License Audit

Date: 2026-07-25 SGT

Scope: `algonius/trading-gpt` at commit `f21cf608c4fbe3c470f383988f19fde6339934ee`, including pinned submodules.

This audit supports the credential-free KR3 baseline only. It is not permission to distribute, operate live trading, register accounts, create API credentials, submit KYC, fund, borrow, or place/cancel orders.

## Upstream Metadata

- Repository: `https://github.com/algonius/trading-gpt`
- Default branch: `main`
- Visibility observed by `gh repo view`: public
- GitHub license metadata observed by `gh repo view --json licenseInfo`: `null`
- Root checkout license files found: none

Conclusion: the upstream/root repository license is absent. Treat absent metadata as a legal/distribution risk, not permission.

## Local Replacement Dependencies

The root `go.mod` depends on local replacement modules that require initialized submodules:

```text
replace github.com/c9s/bbgo => ./libs/bbgo
replace github.com/yubing744/chatgpt-go => ./libs/chatgpt
```

Pinned submodule state:

```text
a45ad83e41f661907dfe4b40a43a99a1e84f24f3 libs/bbgo     remotes/origin/issue73/okex-reduceonly
5d1e56ed0023ca03fe793409b99687bccda41adb libs/chatgpt   v0.3.4
```

Submodule license files:

- `libs/bbgo/LICENSE`: GNU Affero General Public License v3.0
- `libs/chatgpt/LICENSE`: MIT License

## Dependency Shape

The module is a Go application with a large transitive dependency graph. Direct root dependencies include:

- `github.com/anthropics/anthropic-sdk-go`
- `github.com/c9s/bbgo` through the local `libs/bbgo` replacement
- `github.com/dop251/goja`
- `github.com/google/generative-ai-go`
- `github.com/joho/godotenv`
- `github.com/larksuite/oapi-sdk-go/v3`
- `github.com/sirupsen/logrus`
- `github.com/stretchr/testify`
- `github.com/tmc/langchaingo`
- `google.golang.org/api`

The authoritative dependency source for the baseline is the checked-in `go.mod` and `go.sum` at the audited commit, plus the two pinned submodules above. No vendored dependency tree is committed by this baseline PR.

## Safe Baseline Classification

Safe baseline commands:

```text
go mod download
make build
make unit-test
```

Excluded because they can require credentials, configure authenticated sessions, or submit orders:

- `make run`
- `docker-*` and release publishing targets
- `go test ./test/...`
- `test/integration/exchange_entity_test.go`
- `test/integration/orders_cmd_test.go`

The existing `make unit-test` target is credential-free and limited to `go test ./pkg/...`. One package test calls the unauthenticated public Fear & Greed Index API through the existing upstream unit-test path; it is not an exchange API and does not place orders.

## KR4 Entry Recommendation

KR4 can start from this commit for a paper-only HTX adapter investigation, but should not distribute or deploy this code outside an approved internal boundary until the missing root license metadata is resolved. Keep all live trading switches, credential use, and integration smoke tests out of the baseline until a separate safety review approves them.
