# Anchor AMM — Roadmap

## Current State (as of 2026-03-18)

All 4 operations (create-pool, swap, add-liquidity, remove-liquidity) confirmed on Liquid
testnet with the LP Reserve architecture. No reissuance dependencies. Fully permissionless.

**Protocol status: COMPLETE.**
- LP Reserve model: 3 UTXOs (pool_a, pool_b, lp_reserve) — all tested end-to-end
- `LP_PREMINT = 2,000,000,000,000,000` (2 quadrillion) — fits within Elements MAX_MONEY
- Fee-adjusted k-invariant contracts (FEE_NUM/FEE_DEN) verified on testnet
- Dual-leaf taproot (swap/add + remove) with ASSERTL/ASSERTR — no CASE nodes
- OP_RETURN pool announcement + `anchor find-pools` discovery
- Full integration test: create → swap → add → remove (TestFullLifecycle)
- Repo is public: github.com/0ceanslim/anchor
- simc PR merged upstream into BlockstreamResearch/SimplicityHL

**Key architectural constraint discovered:**
Each pool has **unique taproot addresses** because `LP_ASSET_ID` is baked into the
remove and lp_reserve contract CMRs. Pool discovery cannot work by compiling contracts
with `(asset0, asset1, feeNum, feeDen)` alone — the LP_ASSET_ID (derived from the
creation UTXO outpoint) is required, and that's only known from on-chain data.

**Consequence:** All wizard pool discovery and pool-less operation depends on scanning
the chain for ANCHR OP_RETURN creation transactions. The current RPC-based block scan
(`ScanPoolCreations`) is too slow for interactive use (~2.3M blocks on testnet). An
Esplora backend is required to make this fast.

**Work remaining is CLI/UX + Esplora integration — the protocol layer is done.**

---

## Phase 1 — Esplora Client

Foundation for everything else. The Elements chain RPC is too slow and limited for pool
discovery, mempool awareness, and address-based UTXO lookups.

**API docs:** https://github.com/Blockstream/esplora/blob/master/API.md

### 1.1 `pkg/esplora/client.go` — DONE

Minimal typed client for the Esplora HTTP API. Only the endpoints we need:

| Endpoint | Used by | Status |
|----------|---------|--------|
| `GET /api/blocks/tip/height` | Connectivity check (Ping) | done |
| `GET /api/blocks/:start_height` | Pool discovery (block scanning) | done |
| `GET /api/block/:hash/txs/:start_index` | Pool discovery (tx scanning) | done |
| `GET /api/tx/:txid` | Creation tx decode, LP asset derivation | done |
| `GET /api/address/:addr/utxo` | Pool state queries, UTXO lookups | done |
| `GET /api/address/:addr/txs` | Pool tx history | done |
| `GET /api/address/:addr/txs/mempool` | Mempool awareness (Phase 4) | done |

Configuration: `ANCHOR_ESPLORA_URL` env var, `--esplora-url` flag. No auth needed.
Added to `.env.example` and `.env.ps1.example`.

**Files:**
- `pkg/esplora/client.go` — Client, types, all 7 endpoints
- `pkg/esplora/scan.go` — `ScanPoolCreations` (Esplora-backed ANCHR OP_RETURN scanner)
- `tests/esplora_test.go` — all integration tests (Ping, UTXOs, Tx, AddressTxs, Blocks, ScanPoolCreations)

### 1.2 `anchor find-pools` Rewrite — DONE

Replaced the RPC-only scan with Esplora-backed discovery. Falls back to the old RPC
block-by-block scan with a warning if `ANCHOR_ESPLORA_URL` is not set.

Uses Esplora block/tx endpoints for OP_RETURN scanning, then `GetTx` for creation tx
decode (pool addresses + LP asset derivation), then `GetAddressUTXOs` for live reserves.

**Output:** List of discovered pools sorted by depth (reserve0 * reserve1), showing:
pool_a address, fee rate, reserves, LP asset ID. Closed pools marked `[closed]`.

New flags: `--esplora-url`.

**TODO:** `anchor find-pools --save <file>` — writes a full pool.json for a selected
pool, complete with addresses, CMRs, binaries, control blocks. This requires recompiling
the contracts with the discovered pool's LP_ASSET_ID. Not yet implemented.

### 1.3 `anchor check` Update — DONE

Added Esplora connectivity check: shows `OK` with chain tip height, or `WARN` if
unreachable. Does not fail the check — flag-mode operations with pool.json still work
via RPC alone. New flag: `--esplora-url`.

### Testing

All Esplora tests require a running Esplora instance. Set `ANCHOR_ESPLORA_URL` to run:

```bash
# Run esplora integration tests (centralized in tests/)
ANCHOR_ESPLORA_URL="http://127.0.0.1:5000" go test -tags integration -v ./tests/ -run TestEsplora -timeout 120s

# Smoke-test find-pools (scan from a recent block to keep it fast)
ANCHOR_ESPLORA_URL="http://127.0.0.1:5000" anchor find-pools --start-block 2354480

# Smoke-test check
ANCHOR_ESPLORA_URL="http://127.0.0.1:5000" anchor check
```

Verify:
- [ ] `go test -tags integration ./tests/ -run TestEsplora` — all 7 tests pass
- [ ] `find-pools` prints "Scanning via Esplora" and finds pools with correct reserves
- [ ] `find-pools` without `ANCHOR_ESPLORA_URL` prints RPC fallback warning
- [ ] `check` shows Esplora OK with tip height
- [ ] `check` without `ANCHOR_ESPLORA_URL` shows SKIP (not an error)

### 1.4 `find-pools --save` — DONE

Recompiles contracts with a discovered pool's LP_ASSET_ID and writes a full pool.json:

1. Scans chain for ANCHR OP_RETURN pool announcements
2. Verifies each pool's compatibility by compiling contracts and checking pool_a address
   matches on-chain (pools created with older contract versions are filtered out)
3. Displays compatible pools sorted by depth with index numbers
4. Prompts for pool selection (or `--index N` for non-interactive use)
5. Recompiles all contracts with the selected pool's parameters + LP_ASSET_ID
6. Writes pool.json with addresses, CMRs, binaries, control blocks, and metadata

**Flags:** `--save`, `--out <file>`, `--index <N>`, `--build-dir <dir>`

The saved pool.json enables flag-mode swap/add/remove against any discovered pool,
even without having created it yourself. Only `--pool <file>` and RPC env vars needed.

**simc type format update:** The newer simc binary requires `u256`/`u64`/`u32` types
in .args files instead of the old `(Word256)`/`(Word16)`/`(Word64)` format. All .args
files and Go code updated to use the new format.

### 1.5 `create-pool` Duplicate Check — Known Limitation

The `create-pool` wizard's duplicate pool detection compiles contracts and scans the
derived pool_a address for existing UTXOs. This is effectively a no-op because:
- The LP_ASSET_ID is baked into the contract CMRs → different taproot addresses
- LP_ASSET_ID is derived from the creation UTXO outpoint, which doesn't exist yet
- So it compiles with a placeholder LP_ASSET_ID and scans an address no real pool has

The `--force` flag skips this check entirely. When using `--force`, all required flags
must be provided (no wizard prompts): `--asset0`, `--asset1`, `--deposit0`, `--deposit1`,
plus the usual RPC and UTXO flags.

**Future fix:** Replace compile-and-scan with Esplora-backed `find-pools` pool discovery.

---

## Phase 2 — Wizard Pool Discovery

With Esplora and find-pools working, integrate pool discovery into all wizards. This is
the blocker that prevents wizards from working without pool.json.

### 2.1 Fix `create-pool` Wizard Duplicate Detection

Replace the broken compile-and-scan approach with Esplora pool discovery. If pools found
for the selected asset pair + fee, show them sorted by depth and offer "Add liquidity
instead?" redirect.

### 2.2 Fix `add-liquidity` Wizard

**Currently broken:** Same compile-and-scan issue. Finds the wrong pool or no pool.

**Fix:** Replace compile-and-scan with Esplora pool discovery. Select deepest pool by
default. Recompile contracts with the discovered LP_ASSET_ID to get the correct CMRs,
binaries, and control blocks for tx building.

### 2.4 `swap` Wizard — NEW

**Current:** Flag-mode only (`--amount`, `--direction`, `--pool`, etc.). Works on testnet.

**Target flow:**
1. Scan wallet assets, select asset to sell
2. `DiscoverPools` for the selected asset pair — select deepest
3. Show reserves, price, expected output + price impact
4. Prompt amount, show slippage, confirm
5. Auto-select UTXOs, sign, broadcast

### 2.5 `remove-liquidity` Wizard — NEW

**Current:** Flag-mode only (`--lp-amount` or defaults to full UTXO). Works on testnet.

**Target flow:**
1. Scan wallet for LP token UTXOs — each LP asset ID identifies a pool
2. If multiple LP assets found, present list with pool info (reserves, pair)
3. `DiscoverPools` to get full pool config for the selected LP asset
4. Query pool state, show current reserves
5. Prompt: "Remove what percentage? [100%]:" — or enter specific amount
6. Apply dust cap automatically (pool UTXOs must stay >= 330 sats)
7. Show quote: Asset0 payout, Asset1 payout, fee
8. Warn if removing 100% — "This will return all your LP tokens to the reserve"
9. Confirm and broadcast

---

## Phase 3 — CLI Quick Wins

Small improvements that don't require protocol changes.

- [ ] `anchor quote` — print expected output + price impact without building a tx
- [ ] `--min-out <sats>` — abort swap if computed output falls below threshold (slippage)
- [ ] `--json` flag on all subcommands (web UI readiness)
- [ ] Fee estimation two-pass refinement: build tx -> measure actual vsize -> recompute fee
- [ ] `pool-info --json`: `{"reserve0": N, "reserve1": N, "total_supply": N, ...}`
- [ ] `swap/add/remove --json`: `{"txid": "hex", "amount_in": N, "amount_out": N}`
- [ ] Wallet passthrough commands (`anchor wallet balance`, `listunspent`, `send`, etc.)

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

### 5.1 Contract Embedding

Embed compiled `.shl` and `.args` files in the binary via `//go:embed`.
Users need only `simc` + Elements node + Esplora — no build directory management.

### 5.2 Cleanup

- [ ] Remove `replace` directive from `go.mod`
- [ ] Delete dead contracts: `contracts/lp_supply*.go`, `build/pool_a.shl`, `build/pool_b.shl`
- [ ] `LICENSE` — MIT, 2025, 0ceanslim
- [ ] `.gitignore`: add `pool.json`, `build/staging/`

### 5.3 CI/CD

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
