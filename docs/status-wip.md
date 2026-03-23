# Anchor AMM — Roadmap

## Current State (as of 2026-03-22)

All 4 operations (create-pool, swap, add-liquidity, remove-liquidity) confirmed on Liquid
testnet with the LP Reserve architecture. No reissuance dependencies. Fully permissionless.

**Protocol status: COMPLETE.**
**CLI/UX status: Phase 3 COMPLETE.** All commands have interactive wizard modes,
auto-resolve pool configs from `pools/`, support Esplora-backed pool discovery,
`--json` output, improved fee estimation, and wallet utility commands.

**Work remaining: Phase 4+ (mempool awareness, distribution).**

---

## Phase 3 — CLI Quick Wins ✓

Small improvements that don't require protocol changes.

- [x] `--min-out <sats>` — abort swap if computed output falls below threshold (slippage)
- [x] `--json` flag on all subcommands (web UI readiness): `pool-info`, `swap`, `add-liquidity`, `remove-liquidity`, `find-pools`, `check`, `wallet`
- [x] Fee estimation improvement: `getmempoolinfo` for sub-sat/vB precision, `ceil(rate × vsize)` rounding
- [x] `anchor wallet` subcommands: `getbalance`, `listunspent`, `getnewaddress`, `sendtoaddress`
- ~~`anchor quote`~~ — unnecessary; swap wizard already shows quote before confirmation

---

## Phase 4 — Mempool-Aware Operations

Every pool operation reads confirmed UTXO state. Two concurrent operations against the same
confirmed pool UTXOs — only one is accepted; the other gets `txn-mempool-conflict`.

Esplora's `/address/:addr/txs/mempool` endpoint makes this practical.

### 4.1 Pool State Resolution

**`pkg/pool/mempool.go`:**
- `ResolveMempoolTip(esploraClient, poolAddrs)` — check for pending txs at pool
  addresses, walk forward to the tip of the pending chain, return effective outpoints
  + effective reserves
- Read reserve values from pending tx outputs (Esplora returns full decoded txs)

### 4.2 Thread Through All Tx Builders

- `swap`, `add-liquidity`, `remove-liquidity` all receive mempool-resolved outpoints +
  reserves instead of calling `Query()` directly
- `pool-info` shows confirmed state + pending chain + effective tip reserves

### 4.3 Submit-and-Retry Loop

- On `txn-mempool-conflict`: re-resolve tip, rebuild tx with updated reserves, rebroadcast
- Maximum 3 retries with exponential backoff; fail with actionable error if exhausted

### 4.4 Transaction Chaining

Build transactions that spend unconfirmed outputs from the user's own pending pool
operations. See spec.md "Transaction Chaining and Mempool Considerations" for the full
design including chain invalidation concerns.

---

## Phase 5 — Build & Distribution

### 5.1 simc CLI Args Migration

The upstream simc PR has been merged — simc now accepts contract arguments as CLI flags
instead of requiring a separate `.args` file. This eliminates the fragile `.args` file
patching in `pkg/compiler/compiler.go` (writing temp files, managing paths, cleanup).

**Changes:**
- `pkg/compiler/compiler.go` — replace `writeArgsFile()` + file-based invocation with
  direct CLI flag passing (e.g. `simc compile --arg lp_asset:<hex> pool_creation.shl`)
- Remove all `.args` file generation, temp-file cleanup, and related error handling
- `build/` directory simplifies to only `.shl` source files — no `.args` files needed
- Update `//go:embed` patterns (Phase 5.3) to skip `.args` since they no longer exist

**Result:** Simpler build directory, fewer moving parts in the compiler package, no temp
file race conditions.

### 5.2 Data Directory

Platform-appropriate default data directory so pool configs and contract sources have a
stable home regardless of the user's working directory.

**Default paths:**
- Linux/macOS: `~/.anchor/`
- Windows: `%APPDATA%\Anchor\`

**Layout:**
```
~/.anchor/
  contracts/     # .shl source files (extracted from embedded or user-supplied)
  pools/         # pool.json configs (created by create-pool, used by other commands)
  config.toml    # optional: default RPC host, Esplora URL, fee preferences
```

**Changes:**
- New `pkg/datadir/datadir.go` — `Default() string`, `ContractsDir()`, `PoolsDir()`,
  respects `ANCHOR_DATADIR` env var override
- All commands resolve pool configs from `datadir.PoolsDir()` instead of `./pools/`
- `create-pool` writes `pool.json` into the data dir
- `find-pools` saves discovered pools into the data dir
- Prerequisite for contract embedding (5.3) — embedded contracts extract to
  `datadir.ContractsDir()` on first run

**Result:** Users can run `anchor` from any directory. Pool state and contracts live in a
predictable, platform-standard location.

### 5.3 Contract Embedding

Embed compiled `.shl` files in the binary via `//go:embed`.
Users need only `simc` + Elements node + Esplora — no build directory management.
On first run, embedded contracts extract to `datadir.ContractsDir()`.

### 5.4 Cleanup

- [ ] Remove `replace` directive from `go.mod`
- [ ] Delete dead contracts: `contracts/lp_supply*.go`, `build/pool_a.shl`, `build/pool_b.shl`
- [ ] `LICENSE` — MIT, 2025, 0ceanslim
- [ ] `.gitignore`: add `pool.json`, `build/staging/`

### 5.5 CI/CD

```yaml
jobs:
  build-and-test:       # go build, go test, make transpile, stale-check build/*.shl
  regtest-integration:  # go test -tags regtest ./tests/regtest/...
  testnet-integration:  # main branch only, requires RPC secrets
  release:              # triggered by passing testnet-integration; uploads binaries
```

---

## Phase 6 — Post-Release

### Regtest Integration Tests

- `tests/regtest/` package — `Setup()`, `MineBlocks(n)`, `Teardown()`
- Full suite: deploy -> swap -> add -> remove on regtest (self-contained, no external deps)
- Build tag `regtest`; existing `integration` tag for testnet tests

### Pool Lifecycle Edge Cases

- Full pool closure + re-opening test
- Zero-reserve pool graceful handling in all commands

### Web Frontend (separate repo)

The CLI's `--json` output and `find-pools` are the primitives the web frontend consumes.

### Pool Index Server

The web frontend server maintains its own persistent index of all Anchor pools by
scanning ANCHR OP_RETURN creation transactions from genesis on startup, then subscribing
to new blocks to stay current. This eliminates per-request chain scanning — the server
always has a complete pool registry in memory. The CLI's `find-pools` does the same scan
on demand; the server does it once at boot and keeps it hot.

Index entries: LP asset ID, asset pair, fee params, creation block, pool addresses,
last-known reserves. Reserve snapshots are refreshed periodically or on-demand when a
user queries a specific pool.
