# CLI Reference

All commands support interactive **wizard mode** when run without required flags in a terminal. Wizards prompt for missing values, auto-select UTXOs from the wallet, and confirm before broadcasting.

## Machine-Readable Output (`--json`)

All query and operation commands support `--json` for machine-readable JSON output. When `--json` is set, results are printed to stdout as a single JSON object (or array for `find-pools`). Human-readable output and prompts go to stderr and are suppressed where appropriate.

Supported on: `pool-info`, `swap`, `add-liquidity`, `remove-liquidity`, `find-pools`, `check`, and all `wallet` subcommands.

## Fee Estimation

Fee estimation uses `getmempoolinfo` for sub-sat/vB precision (e.g., 0.1 sat/vB on Elements), falling back to `estimatesmartfee`. The fee is computed as `ceil(rate × vsize)` — rounding at the total, not the rate — to avoid overpaying by 10× on low-fee networks.

## Environment Variables

All commands with RPC or network flags support environment variable fallback (flag > env > default):

| Variable | Flag | Description |
|----------|------|-------------|
| `ANCHOR_RPC_URL` | `--rpc-url` | Elements RPC URL |
| `ANCHOR_RPC_USER` | `--rpc-user` | RPC username |
| `ANCHOR_RPC_PASS` | `--rpc-pass` | RPC password |
| `ANCHOR_ESPLORA_URL` | `--esplora-url` | Esplora API URL |
| `ANCHOR_NETWORK` | `--network` | Network: `liquid`, `testnet`, `regtest` |

## Pool Resolution

Most commands find a pool config automatically:

1. `--pool <file>` — explicit path always wins
2. `--pool-id <asset-id>` — look up by LP asset ID via Esplora, compile, and save
3. `pools/*.json` — if one file exists, auto-select; if multiple, prompt
4. `pool.json` in cwd — legacy fallback

---

## check

Validate environment: RPC connection, Esplora connectivity, simc compiler, and pool config.

```
anchor check [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--json` | `false` | Output in JSON format |
| `--pool` | `pool.json` | Pool config file |
| `--rpc-url` | | Elements RPC URL |
| `--rpc-user` | | RPC username |
| `--rpc-pass` | | RPC password |
| `--esplora-url` | | Esplora API URL |

---

## compile

Compile Simplicity contracts and write a pool config. This is a developer tool — most users should use `create-pool` instead.

```
anchor compile [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--build-dir` | `./build` | Directory containing `.shl` files |
| `--out` | `pool.json` | Output pool config path |
| `--network` | | Network |
| `--asset0` | | Asset0 ID hex — patches `.args` before compiling |
| `--asset1` | | Asset1 ID hex — patches `.args` before compiling |
| `--fee-num` | `997` | Fee numerator (997/1000 = 0.3% fee) |
| `--fee-den` | `1000` | Fee denominator |

---

## find-pools

Scan the chain for Anchor pools matching an asset pair or pool ID.

```
anchor find-pools [flags]
```

Two modes:

- **Pool ID lookup**: `--pool-id <lp-asset-id>` finds a single pool via Esplora.
- **Asset pair scan**: `--asset0` / `--asset1` scans OP_RETURN announcements for matching pools.

Results are sorted by depth (deepest first). Interactive mode prompts to save selected pool configs to `pools/`.

| Flag | Default | Description |
|------|---------|-------------|
| `--pool-id` | | Look up a specific pool by LP asset / pool ID |
| `--asset0` | | Asset0 ID to filter by |
| `--asset1` | | Asset1 ID to filter by |
| `--start-block` | `0` | Block height to start scanning from |
| `--esplora-url` | | Esplora API URL |
| `--rpc-url` | | Elements RPC URL |
| `--rpc-user` | | RPC username |
| `--rpc-pass` | | RPC password |
| `--network` | | Network |
| `--save` | `false` | Require at least one pool selection |
| `--out` | | Output filename (default: auto-named in `pools/`) |
| `--index` | `-1` | Pool index to save (non-interactive) |
| `--build-dir` | `./build` | Directory containing `.shl` and `.args` files |

---

## pool-info

Query live pool reserves from chain.

```
anchor pool-info [flags]
```

Two modes:

- **Pool ID lookup**: `--pool-id` queries via Esplora without needing a saved config.
- **Config mode**: loads a pool config and queries reserves via RPC.

Displays reserves, total LP supply, price ratio, and UTXO locations.

| Flag | Default | Description |
|------|---------|-------------|
| `--pool-id` | | Look up pool by LP asset / pool ID (via Esplora) |
| `--pool` | `pool.json` | Pool config file |
| `--esplora-url` | | Esplora API URL |
| `--build-dir` | `./build` | Directory containing `.shl` and `.args` files |
| `--network` | | Network |
| `--rpc-url` | | Elements RPC URL |
| `--rpc-user` | | RPC username |
| `--rpc-pass` | | RPC password |

---

## create-pool

Compile contracts, fund UTXOs, build and broadcast a new pool.

```
anchor create-pool [flags]
```

**Wizard mode** (omit `--asset0`, `--asset1`, `--deposit0`, or `--deposit1`): prompts for asset selection from wallet, fee tier, deposit amounts, and confirms before broadcasting. Scans for existing pools to prevent duplicates.

The command handles the full lifecycle: compile contracts, fund the pool creation address and deposit UTXOs via `sendmany`, wait for confirmations, build the transaction, sign, and broadcast.

| Flag | Default | Description |
|------|---------|-------------|
| `--asset0` | | Asset0 ID hex (prompted if omitted) |
| `--asset1` | | Asset1 ID hex (prompted if omitted) |
| `--deposit0` | `0` | Asset0 sats to deposit (prompted if omitted) |
| `--deposit1` | `0` | Asset1 sats to deposit (prompted if omitted) |
| `--fee-num` | `997` | AMM fee numerator (997/1000 = 0.3% fee) |
| `--fee-den` | `1000` | AMM fee denominator |
| `--fee` | `500` | Network fee in satoshis (auto-estimated if not set) |
| `--lbtc-asset` | | L-BTC asset ID (default: network native asset) |
| `--pool` | `pool.json` | Output pool config path |
| `--build-dir` | `./build` | Directory containing `.shl` and `.args` files |
| `--wallet` | `anchor` | Wallet name |
| `--broadcast` | `false` | Sign and broadcast |
| `--no-announce` | `false` | Skip OP_RETURN pool discovery announcement |
| `--force` | `false` | Skip duplicate pool discovery |
| `--start-block` | `0` | Block height to start pool discovery scan from |
| `--pool-id` | | Check if a pool already exists by LP asset / pool ID |
| `--esplora-url` | | Esplora API URL |
| `--rpc-url` | | Elements RPC URL |
| `--rpc-user` | | RPC username |
| `--rpc-pass` | | RPC password |
| `--network` | | Network |

---

## swap

Swap between the two assets in a pool.

```
anchor swap [flags]
```

**Wizard mode** (omit `--amount`): shows pool reserves and fee rate, prompts for swap direction, shows wallet balance for the input asset, prompts for amount, displays a quote with expected output and price impact, then confirms and broadcasts.

When the input asset is not L-BTC, a separate L-BTC UTXO is auto-selected to cover the network fee.

| Flag | Default | Description |
|------|---------|-------------|
| `--pool` | `pool.json` | Pool config file |
| `--pool-id` | | Resolve pool by LP asset / pool ID (via Esplora) |
| `--in-asset` | `asset0` | Input asset: `asset0`, `asset1`, or a full asset ID |
| `--amount` | `0` | Amount to swap in satoshis |
| `--min-out` | `0` | Minimum acceptable output in satoshis |
| `--user-utxo` | | Input UTXO as `txid:vout` (auto-selected if omitted) |
| `--user-addr` | | Output address for received asset (auto-derived if omitted) |
| `--asset0` | | Asset0 ID (read from pool config if omitted) |
| `--asset1` | | Asset1 ID (read from pool config if omitted) |
| `--lbtc-asset` | | L-BTC asset ID |
| `--fee` | `500` | Network fee in satoshis (auto-estimated if not set) |
| `--wallet` | `anchor` | Wallet name |
| `--broadcast` | `false` | Broadcast transaction via RPC |
| `--esplora-url` | | Esplora API URL |
| `--build-dir` | `./build` | Directory containing `.shl` and `.args` files |
| `--rpc-url` | | Elements RPC URL |
| `--rpc-user` | | RPC username |
| `--rpc-pass` | | RPC password |
| `--network` | | Network |

---

## add-liquidity

Add liquidity to a pool and receive LP tokens.

```
anchor add-liquidity [flags]
```

**Wizard mode** (omit `--deposit0`): prompts for asset selection from wallet, searches for an existing pool, prompts for deposit amount, computes proportional deposit1, shows LP tokens to receive, and confirms before broadcasting.

In flag mode, `deposit1` is auto-adjusted to the floor proportion of the reserves (`deposit1 = floor(deposit0 * reserve1 / reserve0)`).

UTXOs for asset0, asset1, and L-BTC fee are auto-selected from the wallet. Multiple UTXOs are combined if no single UTXO covers the required amount.

| Flag | Default | Description |
|------|---------|-------------|
| `--pool` | `pool.json` | Pool config file |
| `--pool-id` | | Resolve pool by LP asset / pool ID (via Esplora) |
| `--deposit0` | `0` | Asset0 amount to deposit |
| `--deposit1` | `0` | Asset1 amount to deposit |
| `--asset0-utxo` | | Asset0 UTXO as `txid:vout` (auto-selected if omitted) |
| `--asset1-utxo` | | Asset1 UTXO as `txid:vout` (auto-selected if omitted) |
| `--lbtc-utxo` | | L-BTC UTXO for fee (auto-selected if omitted) |
| `--user-addr` | | Address to receive LP tokens (auto-derived if omitted) |
| `--asset0` | | Asset0 ID (read from pool config if omitted) |
| `--asset1` | | Asset1 ID (read from pool config if omitted) |
| `--lp-asset` | | LP token asset ID (read from pool config if omitted) |
| `--lbtc-asset` | | L-BTC asset ID |
| `--fee` | `500` | Network fee in satoshis (auto-estimated if not set) |
| `--wallet` | `anchor` | Wallet name |
| `--broadcast` | `false` | Broadcast transaction via RPC |
| `--build-dir` | `./build` | Directory containing `.shl` files |
| `--esplora-url` | | Esplora API URL |
| `--rpc-url` | | Elements RPC URL |
| `--rpc-user` | | RPC username |
| `--rpc-pass` | | RPC password |
| `--network` | | Network |

---

## remove-liquidity

Burn LP tokens and withdraw proportional reserves from a pool.

```
anchor remove-liquidity [flags]
```

**Wizard mode** (omit `--lp-amount`): auto-selects the LP UTXO from the wallet, prompts for how much to remove (percentage like `50%` or absolute token amount), shows a payout quote, and confirms before broadcasting.

Burn amount is capped to preserve a dust minimum (330 sats) in each pool output.

| Flag | Default | Description |
|------|---------|-------------|
| `--pool` | `pool.json` | Pool config file |
| `--pool-id` | | Resolve pool by LP asset / pool ID (via Esplora) |
| `--lp-amount` | `0` | LP tokens to burn (prompted if omitted) |
| `--lp-utxo` | | LP UTXO as `txid:vout` (auto-selected if omitted) |
| `--lbtc-utxo` | | L-BTC UTXO for fee (auto-selected if omitted) |
| `--user-addr0` | | Address for Asset0 payout (auto-derived if omitted) |
| `--user-addr1` | | Address for Asset1 payout (auto-derived if omitted) |
| `--change-addr` | | Address for LP and L-BTC change (auto-derived if omitted) |
| `--asset0` | | Asset0 ID (read from pool config if omitted) |
| `--asset1` | | Asset1 ID (read from pool config if omitted) |
| `--lp-asset` | | LP token asset ID (read from pool config if omitted) |
| `--lbtc-asset` | | L-BTC asset ID |
| `--fee` | `500` | Network fee in satoshis (auto-estimated if not set) |
| `--wallet` | `anchor` | Wallet name |
| `--broadcast` | `false` | Broadcast transaction via RPC |
| `--build-dir` | `./build` | Directory containing `.shl` and `.args` files |
| `--esplora-url` | | Esplora API URL |
| `--rpc-url` | | Elements RPC URL |
| `--rpc-user` | | RPC username |
| `--rpc-pass` | | RPC password |
| `--network` | | Network |

---

## wallet

Wallet utility subcommands that mirror Elements CLI naming. All subcommands share `--rpc-url`, `--rpc-user`, `--rpc-pass`, `--wallet`, `--network`, `--lbtc-asset`, and `--json` flags.

### wallet getbalance

Show wallet asset balances (explicit UTXOs only).

```
anchor wallet getbalance [--asset <id>]
```

Without `--asset`, lists all assets. With `--asset`, shows balance for that specific asset.

### wallet listunspent

List unspent outputs (explicit only).

```
anchor wallet listunspent [--asset <id>]
```

### wallet getnewaddress

Generate and print a new unconfidential receiving address.

```
anchor wallet getnewaddress
```

### wallet sendtoaddress

Send funds to an address. Default asset is L-BTC.

```
anchor wallet sendtoaddress <address> <amount> [--asset <id>] [--yes]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--asset` | | Asset ID to send (default: L-BTC) |
| `--yes` | `false` | Skip confirmation prompt |
