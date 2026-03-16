# Anchor AMM — Roadmap

## Current State (as of 2026-03-16)

All 4 operations (create-pool, swap, add-liquidity, remove-liquidity) confirmed on Liquid
testnet with the LP Reserve architecture. No reissuance dependencies. Fully permissionless.

**Protocol status: COMPLETE.**
- LP Reserve model: 3 UTXOs (pool_a, pool_b, lp_reserve) — all tested end-to-end
- `LP_PREMINT = 2,000,000,000,000,000` (2 quadrillion) — fits within Elements MAX_MONEY
- Fee-adjusted k-invariant contracts (FEE_NUM/FEE_DEN) verified on testnet
- Dual-leaf taproot (swap/add + remove) with ASSERTL/ASSERTR — no CASE nodes
- OP_RETURN pool announcement + `anchor find-pools` discovery
- Full integration test: create → swap → add → remove (TestFullLifecycle)

**Work remaining is all CLI/UX — the protocol layer is done.**

---

## Phase 1 — CLI UX Overhaul (interactive wizard mode)

The CLI currently requires explicit flags for every parameter. Real usage needs a guided
flow that discovers wallet state and prompts the user through each operation.

---

### 1.1 `anchor create-pool` Wizard ✓ COMPLETE

**Implemented flow:**
1. Scan wallet assets via `listunspent`, present numbered list
2. User selects Asset0, then Asset1 (filtered to exclude Asset0)
3. AMM fee tier prompt (default 997/1000 = 0.30%) — press Enter to keep or type num/den
4. Pool duplicate check: compile with (asset0, asset1, feeNum, feeDen) → `scantxoutset` at
   derived pool_a address (O(1), wallet-independent — checks the UTXO set, not wallet state)
   - Secondary fallback: if pool.json exists with same assets/fee but different address
     (contracts changed since creation), also scans that stored address
   - If found: show reserves → "Add liquidity instead? [y/n]" → redirect to 1.2 wizard
   - If not found: "No existing pool found, proceeding to create"
5. Prompt deposit0 (with balance shown), then deposit1
6. Network fee rate prompt (sat/vbyte, default: EstimateSmartFee) → total fee computed
7. Summary + "This will send funds from your wallet and broadcast immediately." → [y/n]
8. Fund via sendmany → wait for confirmations → build tx → sign → broadcast
9. pool.json updated with new LP asset ID, fee params, and contract addresses

**Additional guard:** `anchor compile` warns before overwriting a pool.json that already
has a deployed pool (lp_asset_id set) — prompts to overwrite, save as auto-named file
(`pool-<a0>-<a1>-<bps>bps.json`), or enter a custom filename.

---

### 1.2 `anchor add-liquidity` Wizard ✓ COMPLETE

**Implemented flow:**
1. Scan wallet explicit assets, present numbered list for Asset0/Asset1 selection
2. AMM fee tier prompt (default 997/1000) — compiles contracts to derive pool address
3. Pool discovery: `scantxoutset` at compiled pool_a address; secondary fallback to pool.json
4. LP asset ID resolution: walk back pool_a vin[0] chain to creation tx (vin[0].Issuance != nil)
5. Query pool state, show current reserves and price ratio
6. Prompt deposit0 (with explicit wallet balance shown)
7. Auto-compute proportional deposit1 from reserves ratio
8. If wallet Asset1 balance < required: show shortfall, prompt to lower deposit0
9. Show LP tokens to be minted (quote), prompt fee rate
10. Summary + confirm → auto-select UTXOs → sign → broadcast
11. Multi-UTXO input selection: combines multiple UTXOs when no single one covers needed amount
12. Create-pool duplicate detection redirects to this wizard seamlessly

**Bugs fixed during implementation:**
- LP reserve precision: go-elements stores explicit values as big-endian; `exactOutputSatoshis`
  now uses `elementsutil.ValueFromBytes()` instead of manual little-endian read
- Confidential UTXO filtering: `ListUnspentByAsset` and `walletExplicitAssets` now exclude
  confidential (blinded) UTXOs via `amountblinder` check — prevents `bad-txns-in-ne-out`
- LP asset ID walk-back: primary detection via `vin[0].Issuance != nil` (works for all pools,
  not just those with ANCHR OP_RETURN)

---

### 1.2a `anchor swap` Wizard — TODO

**Current:** Flag-mode only (`--amount`, `--direction`, `--pool`, etc.). Works on testnet.

**Target flow:**
1. Scan wallet assets, select asset to sell
2. Compile contracts, find pool
3. Show reserves, price, expected output + price impact
4. Prompt amount, show slippage, confirm
5. Auto-select UTXOs, sign, broadcast

---

### 1.3 `anchor remove-liquidity` Wizard — TODO

**Current:** Flag-mode only (`--lp-amount` or defaults to full UTXO). Works on testnet.

**Target flow:**
1. Load pool.json, query pool state
2. Scan wallet for LP token UTXOs, show total LP balance
3. Prompt: "Remove what percentage? [100%]:" — or enter specific amount
4. Apply dust cap automatically (pool UTXOs must stay ≥ 330 sats)
5. Show quote: Asset0 payout, Asset1 payout, fee
6. Warn if removing 100% — "This will return all your LP tokens to the reserve"
7. Confirm and broadcast
8. Ensure no LP dust left — if removing 100%, burn the full UTXO amount

---

### 1.4 Wallet Passthrough Commands

Expose common Elements wallet RPCs through the anchor CLI so users don't need to switch
to `elements-cli` when env vars are already set.

**Commands:**
- `anchor wallet listunspent [--asset <id>]` — show explicit UTXOs, grouped by asset
- `anchor wallet balance [--asset <id>]` — sum of explicit UTXOs per asset
- `anchor wallet newaddress` — generate unconfidential address
- `anchor wallet send --to <addr> --amount <sats> [--asset <id>]` — simple send
- `anchor wallet load --name <wallet>` — load an existing wallet on the node
- `anchor wallet create --name <wallet> [--backup-file <path>]` — create a new wallet;
  if `--backup-file` provided, immediately calls `dumpwallet` to export all keys/HD seed.
  Elements doesn't expose BIP39 mnemonics — the dump file IS the seed backup.
- `anchor wallet restore --name <wallet> --from-file <path>` — create a blank wallet
  then call `importwallet <path>` to load keys from a prior `dumpwallet` export.
  New RPC wrappers needed: `DumpWallet(path)`, `ImportWallet(path)` in `pkg/rpc/client.go`.

These are thin wrappers around the existing `pkg/rpc` client. No new protocol logic.

---

## Phase 2 — Mempool-Aware Operations

Every pool operation reads confirmed UTXO state. Two concurrent operations against the same
confirmed pool UTXOs — only one is accepted; the other gets `txn-mempool-conflict`.
**Critical for multi-user pools.**

---

### 2.1 Pool State Resolution

**`pkg/pool/mempool.go`:**
- `ResolveMempoolTip()` — walk mempool forward from confirmed pool UTXOs to the tip of the
  pending chain; return effective outpoints + effective reserves
- Prefer `gettxspendingprevout` (O(1) per UTXO); fallback to `getrawmempool` + decode
- Read reserve values from pending tx explicit outputs (decode raw hex — `gettxout` only
  sees confirmed UTXOs)

### 2.2 Thread Through All Tx Builders

- `swap`, `add-liquidity`, `remove-liquidity` all receive mempool-resolved outpoints +
  reserves instead of calling `QueryPool()` directly
- `pool-info` shows confirmed state + full pending chain + effective tip reserves

### 2.3 Submit-and-Retry Loop

- On `txn-mempool-conflict`: re-resolve tip, rebuild tx with updated reserves, rebroadcast
- Maximum 3 retries with exponential backoff; fail with actionable error if exhausted

### 2.4 Transaction Chaining

Build transactions that spend unconfirmed outputs from the user's own pending pool
operations. Enables rapid sequential operations without waiting for confirmations.

---

## Phase 3 — CLI Quick Wins

Small improvements that don't require protocol changes.

- [ ] `anchor quote` — print expected output + price impact without building a tx
- [ ] `--min-out <sats>` — abort swap if computed output falls below threshold (slippage protection)
- [ ] `--json` flag on all subcommands (web UI readiness)
- [ ] Fee estimation two-pass refinement: build tx → measure actual vsize → recompute fee
- [ ] `pool-info --json`: `{"reserve0": N, "reserve1": N, "total_supply": N, "lp_reserve": N, "price": F}`
- [ ] `swap/add/remove --json`: `{"txid": "hex", "amount_in": N, "amount_out": N}`

---

## Phase 4 — Pool Discovery Improvements

### 4.1 Pool-Less Operation (pool.json optional)

All data in pool.json is derivable from on-chain state + pool parameters
`(asset0, asset1, feeNum, feeDen)`. Commands should work without pool.json:

| pool.json field | How to derive without it |
|-----------------|--------------------------|
| Contract addresses | Compile with `(asset0, asset1, feeNum, feeDen)` |
| Binaries / CMRs / control blocks | Same compilation |
| LP asset ID | Creation tx outpoint → `ComputeLPAssetID` |
| FeeNum / FeeDen | From OP_RETURN announcement OR user-supplied flag |

- `anchor find-pools --save <file>` — write full pool.json for a discovered pool
- `--pool` flag becomes optional — accept `--asset0`, `--asset1` directly
- Pool lookup by LP asset ID (`--lp-asset <hex>`)

### 4.2 Duplicate Pool Routing

When multiple pools exist at the same address, route by LP asset ID.
`--pool <lp-asset-id>` flag for explicit targeting.
`find-pools` output includes LP asset ID for ergonomics.

---

## Phase 5 — Build & Distribution

### 5.1 Contract Embedding

Embed compiled `.shl` and `.args` files in the binary via `//go:embed`.
Users need only `simc` + Elements node — no build directory management.

### 5.2 Cleanup Before Public

- [ ] Remove `replace` directive from `go.mod`
- [ ] `LICENSE` — MIT, 2025, 0ceanslim
- [ ] Move old `pool_a.shl`, `pool_b.shl`, `lp_supply.shl` → `build/reference/`
- [ ] `.gitignore`: add `pool.json`, `build/staging/`
- [ ] Delete `docs/status-wip.md` + `docs/status-complete.md` before public

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
- Full suite: deploy → swap → add → remove on regtest (self-contained, no external deps)
- Build tag `regtest`; existing `integration` tag for testnet tests

### Pool Lifecycle Edge Cases

- Full pool closure + re-opening test
- Multi-UTXO recovery (consolidation tx for accidental extra UTXOs at pool address)
- Zero-reserve pool graceful handling in all commands

### Web Frontend (separate repo)

The CLI's `--json` output and `find-pools` are the primitives the web frontend consumes.
No central coordinator — all pool data from the Elements node.
