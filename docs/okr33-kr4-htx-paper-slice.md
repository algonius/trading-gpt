# OKR-33 KR4 HTX Paper-Safe Slice

Date: 2026-07-26 SGT

Status: first bounded paper/replay configuration and market-mapping slice.

## Embedded bbgo Findings

The pinned `libs/bbgo` submodule has no HTX or Huobi exchange package. The available exchange directories are `binance`, `bitget`, `bybit`, `kucoin`, `max`, and `okex`.

Relevant source boundaries:

- `libs/bbgo/pkg/types/exchange.go` defines valid exchange names as `max`, `binance`, `okex`, `kucoin`, `bitget`, and `bybit`.
- `libs/bbgo/pkg/exchange/factory.go` only constructs those exchange implementations.
- `NewWithEnvVarPrefix` reads `<PREFIX>_API_KEY`, `<PREFIX>_API_SECRET`, and optional `<PREFIX>_API_PASSPHRASE`; this KR4 slice does not call it for HTX.

Consequence: a real HTX session cannot be safely wired through this embedded bbgo revision without adding or upgrading an exchange implementation. This PR intentionally does not modify bbgo or add authenticated session wiring.

## HTX/Huobi Alias

The paper-safe config layer normalizes these names to the canonical local name `htx`:

- `htx`
- `huobi`
- `huobipro`
- `huobi-pro`

The legacy `huobi` names are accepted only at the configuration/mapping layer. They are not registered as bbgo exchange names.

## Public-Market-Data Path

The Huobi/HTX spot API reference describes public/reference and market-data endpoints including symbol metadata, k-lines, and ticker reads. Source: <https://huobiapi.github.io/docs/spot/v1/en/>. The stable spot symbol metadata path used for fixture shape is:

- REST base URL: `https://api.huobi.pro`
- Symbol reference path: `/v1/common/symbols`
- K-line path: `/market/history/kline`
- Ticker path: `/market/detail/merged`

This slice only records those paths and parses committed fixtures. It does not make HTTP requests.

## Symbol and Precision Mapping

`pkg/exchanges/htx` maps an HTX symbol fixture into `types.Market` as follows:

- HTX local symbol `btcusdt` -> canonical strategy symbol `BTCUSDT`
- `base-currency`/`quote-currency` -> uppercase `BaseCurrency`/`QuoteCurrency`
- `price-precision` -> `PricePrecision`
- `amount-precision` -> `VolumePrecision`
- `10^-price-precision` -> `TickSize`
- `10^-amount-precision` -> `StepSize`
- `min-order-amt` -> `MinQuantity`
- `min-order-value` -> `MinNotional`
- non-`online` symbols are filtered out by `OnlineMarkets`

## Paper Boundary

`NormalizeConfig` defaults to `mode: paper`, accepts only `paper` or `replay`, and rejects `allow_authenticated: true`. The committed fixture config sets `public_market_data.enabled: false` so tests stay entirely local and deterministic.

Explicitly out of scope:

- production credentials
- API-key creation
- registration or KYC
- account or balance reads
- live/test exchange calls
- order submit/cancel paths
- bbgo exchange factory registration

## Remaining KR4 Gaps

- Choose whether to add a native bbgo HTX exchange implementation or upgrade to an upstream bbgo version that already supports it, if one is available and license-compatible.
- Add an HTTP client only after a separate safety review defines unauthenticated public-data call limits and fixtures.
- Define paper order lifecycle semantics separately from live HTX order APIs.
- Resolve root repository license metadata risk before distribution or deployment beyond approved internal paper work.

## Paper Lifecycle Follow-Up

Date: 2026-08-02 SGT

The next bounded slice adds a credential-free `PaperLifecycleSession` that composes the existing `MarketData` interface with an in-memory paper account/order ledger. The replay tests cover submit, cancel, deterministic kline-triggered fills, fee debits, order states, closed-trade PnL, and exact ledger reconciliation with zero drift.

Retained boundaries:

- no `PrivateSession`, signer, private client, authenticated HTTP, env credential loading, exchange account access, or live order path;
- paper/replay modes only through the existing fail-closed config normalization;
- spot limit orders only, with invalid price/quantity precision and margin side effects rejected.

Residual gaps before 100-closed-trade evidence can be claimed:

- the ledger is in-memory only; append-only file persistence/export is still needed for retained evidence artifacts;
- fill semantics are deterministic full fills on crossed closed klines, with no partial-fill, spread, depth, or slippage model yet;
- there is no committed 100-trade replay harness or fixture set yet.

## Retained Evidence Export Follow-Up

Date: 2026-08-02 SGT

The retained-evidence slice adds a canonical versioned JSON export for one completed credential-free `PaperLifecycleSession` replay run. The export contains sorted balances, orders, trades, ledger entries, closed trades, summary turnover/fee/PnL fields, and reconciliation readback so the final balances can be independently recomputed from the retained ledger deltas.

Retained scope:

- deterministic JSON and rollback-capable JSONL append helpers over injected sinks/readers;
- sorted map-backed balances, orders, and trades, contiguous ledger-sequence enforcement, sorted closed trades;
- fail-closed export on non-zero drift, locked funds, incomplete/working orders, missing orders, or missing closed trades;
- strict JSONL readback that validates version, exchange, mode, required fields, and an explicit bounded record size;
- no wall-clock-only evidence fields: `exported_at` comes from the injected paper lifecycle clock.

Closed-trade evidence separates price PnL from fees. `entry_notional` is the matched gross entry cost for the retained closed quantity, base-denominated entry fees are valued in quote currency at the entry price, `gross_pnl` is `exit_notional - entry_notional`, and `net_pnl` deducts both entry and exit fees.

Remaining gaps:

- this is still an in-memory session export helper, not a durable file/DB retention service;
- evidence covers the bounded one-closed-trade replay fixture path only and does not claim KR5 or 100 closed trades;
- fill modeling remains deterministic crossed closed-kline full fills without depth, partial-fill, spread, or slippage modeling.

## Durable Evidence Store Follow-Up

Date: 2026-08-03 SGT

The next bounded retained-evidence slice adds a path-scoped `PaperLifecycleEvidenceFileStore` for paper/replay lifecycle evidence. It appends only records that satisfy the same strict JSONL contract as the in-memory helper: versioned identity, required-field validation, fee-separated gross/net PnL, rollback on failed writes, and the shared 1 MiB record limit including the trailing newline.

Retained scope:

- file-backed append and restart readback for JSONL evidence records;
- complete read/deduplicate/append/sync serialization per canonical path inside one process;
- exact-record duplicate retry handling so a restarted retry does not append the same deterministic record twice;
- fail-closed readback for corrupt or truncated tails instead of silently skipping bad data;
- preservation of already committed records when a later append fails before commit.

Remaining gaps:

- this is storage/recovery infrastructure only and does not fabricate or claim cumulative KR5 PnL evidence;
- evidence still covers the bounded replay fixture path and does not claim 100 closed trades;
- the file store does not provide multi-process locking; multiple processes must use an external coordinator or separate files;
- no private/authenticated account, live exchange, credential, funding, or factory wiring is introduced.

## Cumulative Evidence Recorder Follow-Up

Date: 2026-08-03 SGT

The cumulative recorder slice adds `PaperLifecycleEvidenceRecorder`, which persists completed paper/replay run evidence with an explicit local replay source identity (`mode`, `source`, `dataset`) and per-run input (`source`, `dataset`, `run`). The retained `run_id` is a deterministic SHA-256 of the explicit run input, so restart retries for the same run are idempotent and cannot double count unless the retained evidence is manually corrupted, in which case readback fails closed.

Readback recomputes aggregate turnover, raw total fees, entry/exit fee attribution, gross/net closed PnL, closed/open trade counts, and reconciliation drift from retained records. Records are accepted only when the recorder source identity, schema version, run identity, canonical JSONL byte form, and embedded `PaperLifecycleEvidence` validation all match.

Boundaries:

- deterministic fixtures validate recorder/storage/recovery mechanics only;
- fixture PnL is not daily, cumulative, live, or competition performance;
- this slice does not satisfy or claim KR5's 100 closed trades;
- the recorder inherits the file store's in-process per-path serialization and multi-process external-coordination requirement.
