# anchor Protocol Specification

Immutable, permissionless constant-product AMM for Liquid.

---

## Overview

anchor is an on-chain AMM governed entirely by Simplicity contracts. There is no admin key, no treasury, no upgrade mechanism. Pools are permanent UTXOs. The contracts enforce the constant-product invariant (`x * y = k`) for swaps and proportional payout for liquidity operations.

Anyone can create a pool, swap in any pool, add liquidity to any pool, and remove liquidity from any pool — provided they hold the correct LP token for that pool. No single party retains control over a pool after creation.

---

## Pool State: 3 Persistent UTXOs

| # | UTXO | Holds | Locked by |
|---|------|-------|-----------|
| 0 | Pool UTXO A | `reserve0` of Asset0 | `pool_a_swap.shl` / `pool_a_remove.shl` |
| 1 | Pool UTXO B | `reserve1` of Asset1 | `pool_b_swap.shl` / `pool_b_remove.shl` |
| 2 | LP Reserve UTXO | `lp_reserve` LP tokens | `lp_reserve_add.shl` / `lp_reserve_remove.shl` |

**LP tokens** are Liquid asset issuances (standard transferable assets). `LP_ASSET_ID` is computed deterministically from the pool creation input outpoint using Elements deterministic issuance with a 32-zero-byte contract hash.

**LP token value**: LP tokens represent a proportional claim on `reserve0` and `reserve1`. Returning `lp_burned` tokens yields `lp_burned * reserve0 / total_supply` of Asset0 and `lp_burned * reserve1 / total_supply` of Asset1.

**LP Reserve UTXO mechanics**: At pool creation, `LP_PREMINT` (2 quadrillion = `2,000,000,000,000,000`) LP tokens are issued. The initial LP minted for the creator is sent to their wallet; the remainder is locked in the LP Reserve UTXO. On add-liquidity, LP tokens are drawn from the reserve and sent to the depositor. On remove-liquidity, LP tokens are returned from the user to the reserve. The reserve is contract-controlled — no party can withdraw LP tokens from it except through a valid add-liquidity transaction.

**Deriving `total_supply`**: Simplicity contracts cannot access external state — they can only read transaction inputs and outputs. Every contract derives the circulating LP supply from the reserve:

```
total_supply = LP_PREMINT - InputAmount(2)
```

`LP_PREMINT` is a compile-time constant baked into every contract's CMR. `InputAmount(2)` reads the current LP Reserve balance. This eliminates the need for any separate bookkeeping UTXO.

**Why 2 quadrillion?** Elements consensus sums ALL explicit output values across all assets in a single transaction and rejects totals exceeding `MAX_MONEY` (2.1 quadrillion sats = 21M BTC). The create-pool transaction contains `LP_PREMINT` in outputs alongside pool deposit amounts. Setting `LP_PREMINT = 2e15` leaves ~100 trillion sats of headroom for deposits, change, and fees — enough for any pool that physically fits on Liquid.

---

## Transaction Layouts

All inputs to pool transactions must be **explicit (non-confidential) UTXOs**. Confidential inputs break the explicit balance checks enforced by the contracts and will be rejected.

### Swap
```
Inputs:  [0] Pool UTXO A   [1] Pool UTXO B   [2] User UTXO
Outputs: [0] New Pool A    [1] New Pool B    [2] User output
         [3+] change outputs   [last] fee
```
Swaps do **not** include the LP Reserve UTXO. Only `pool_a` and `pool_b` participate. The swap contracts enforce the k-invariant over these two inputs/outputs only.

### Add Liquidity
```
Inputs:  [0] Pool UTXO A   [1] Pool UTXO B   [2] LP Reserve UTXO
         [3] User Asset0 UTXO   [4] User Asset1 UTXO   [5] User L-BTC UTXO
Outputs: [0] New Pool A    [1] New Pool B    [2] New LP Reserve
         [3] LP tokens → User   [4+] change outputs   [last] fee
```
- New Pool A amount = `reserve0 + deposit0`
- New Pool B amount = `reserve1 + deposit1`
- New LP Reserve amount = `lp_reserve - lp_minted`
- Output[3]: `lp_minted` LP tokens sent to depositor (drawn from reserve)
- `total_supply` increases by `lp_minted` (derived: `LP_PREMINT - new_lp_reserve`)

### Remove Liquidity
```
Inputs:  [0] Pool UTXO A   [1] Pool UTXO B   [2] LP Reserve UTXO
         [3] User LP UTXO (≥ lp_burned tokens)   [4] User L-BTC UTXO (fee)
Outputs: [0] New Pool A    [1] New Pool B    [2] New LP Reserve
         [3] payout0 of Asset0   [4] payout1 of Asset1
         [5] LP change → User (if UserLPAmount > lp_burned)
         [6] L-BTC change → User (if UserLBTCAmount > fee)
         [last] fee
```
- New Pool A amount = `reserve0 - payout0`
- New Pool B amount = `reserve1 - payout1`
- New LP Reserve amount = `lp_reserve + lp_burned`
- `lp_burned` LP tokens are returned to the LP Reserve (not destroyed)
- `total_supply` decreases by `lp_burned` (derived: `LP_PREMINT - new_lp_reserve`)
- Remaining LP tokens (`UserLPAmount - lp_burned`) returned as change if > 0

### Pool Creation (one-time)
```
Inputs:  [0] Creation UTXO (locked by pool_creation.shl, carries LP issuance attachment)
         [1] Asset0 deposit UTXO   [2] Asset1 deposit UTXO
Outputs: [0] Pool UTXO A (deposit0 of Asset0)   [1] Pool UTXO B (deposit1 of Asset1)
         [2] LP Reserve UTXO (LP_PREMINT - lp_minted LP tokens)
         [3] LP tokens to creator (lp_minted)
         [4+] change outputs   [last] fee
```
Input[0] must be pre-funded with at least `fee` L-BTC. The LP issuance is attached to
input[0] — the `LP_ASSET_ID` is deterministically derived from its outpoint before the
transaction is built.

The issuance mints `LP_PREMINT` LP tokens total. `lp_minted` go to the creator (output[3]),
and `LP_PREMINT - lp_minted` go to the LP Reserve (output[2]). The `pool_creation.shl`
contract verifies:
- `IssuanceAssetAmount(0) == LP_PREMINT`
- `OutputAmount(2) == LP_PREMINT - lp_minted` (reserve gets the remainder)

---

## Fee Mechanism

### Per-pool fee rate

Each pool is compiled with two constants baked into the contract CMR:

| Constant | Example (0.3%) | Example (1%) | Zero-fee |
|----------|---------------|--------------|----------|
| `FEE_NUM` | 997 | 990 | 1000 |
| `FEE_DEN` | 1000 | 1000 | 1000 |

The fee rate in basis points is `(FEE_DEN - FEE_NUM) * 10000 / FEE_DEN`.

Because `FEE_NUM` and `FEE_DEN` are compile-time constants in the Simplicity contract, they
are embedded in the CMR. Changing the fee produces a different CMR and a different taproot
address — a deployed pool's fee rate is immutable.

### Fee-adjusted k check

In swap/add mode, the contract enforces:

```
(reserve_in × FEE_DEN + amountIn × FEE_NUM) × newReserve_out
    ≥ reserve_in × reserve_out × FEE_DEN
```

where `amountIn = newReserve_in - reserve_in`. This is equivalent to Uniswap v2's fee check.
Only `FEE_NUM / FEE_DEN` of the input is credited toward the invariant; the remainder stays
in reserves permanently, growing k.

**No-fee simplification:** When `FEE_NUM == FEE_DEN`, this reduces to `kOld <= kNew`.

### Fee denomination

The protocol fee is always paid in the **input asset of the swap** — it never leaves the
pool. There is no separate fee output and no requirement that L-BTC be one of the pool
assets.

- Swap Asset0 → Asset1: fee paid in Asset0, stays in reserve0
- Swap Asset1 → Asset0: fee paid in Asset1, stays in reserve1

A pool containing no L-BTC (e.g. USDT/LCAD) operates identically — the protocol never
touches L-BTC. The only L-BTC requirement is the **on-chain transaction fee** (the miner
fee output), which is a Liquid network requirement and is separate from the protocol fee.

### Impermanent loss

When the external market price of the two assets diverges from the ratio at which liquidity
was deposited, arbitrageurs rebalance the pool to the new price by swapping the cheaper asset
in. This leaves the LP holding more of the asset that fell in price and less of the one that
rose — a worse outcome than simply holding the original assets outside the pool. The loss is
"impermanent" because if the price ratio returns to the original deposit ratio, the LP
position fully recovers. If the LP withdraws while the ratio is different, the loss is realised.

Fee income offsets impermanent loss: every swap that moves the price also pays a fee into
reserves. At higher fee rates or higher swap volume, accumulated fees can exceed the IL,
making LP provision net positive. At zero fee there is no offset — any price divergence is
pure loss for the LP.

### LP fee earnings

Swap fees are **not extracted to a separate fee address**. They remain in the pool as
increased reserves, growing k with every swap. LP tokens represent a proportional share of
both reserves, so each LP token redeems for more Asset0 and Asset1 over time.

Fees are realized at remove-liquidity time: the LP returns tokens and receives proportionally
more reserves than they originally deposited (by the fraction of total swap fees accumulated).

### Zero-fee pools

Setting `FEE_NUM = FEE_DEN` (e.g. 1000/1000) produces a pool with no protocol fee — every
swap executes at the exact constant-product curve with no fee accumulation in reserves.

**Full IL exposure with no compensation.** Skipping the OP_RETURN announcement does not make
a pool private — the creation transaction is on-chain and the pool UTXOs are always visible
to anyone scanning the chain. Any discovered zero-fee pool is a free arbitrage target: if the
external price of the pair moves, arbitrageurs drain the pool to the new price and the LP
absorbs the full impermanent loss with no fee income to offset it.

Zero-fee is a valid configuration but should only be deployed by LPs who explicitly accept
that tradeoff — for example, a pool between two assets whose price ratio is expected to be
stable, where fee drag matters more than IL protection.

---

## Contract Logic

Each pool contract exists as two deployed variants. There is no runtime mode detection — the
operation type (swap vs remove, add vs remove) is selected by which taproot leaf is spent.

All three pool UTXOs (pool_a, pool_b, lp_reserve) participate in every transaction. Each
contract enforces its own self-covenant (`OutputScriptHash(N) == CurrentScriptHash()`) to
ensure the UTXO chain is unbreakable.

Every contract that needs `total_supply` derives it as:
```
total_supply = LP_PREMINT - InputAmount(2)
```

### pool_a_swap.shl

Spends Pool UTXO A in swap or add-liquidity mode. Uses `ASSERTL` (unwrap_left) to assert
`is_remove_mode = false` — fails immediately if called in remove context.

**Invariants enforced:**
- `OutputAsset(0) == Asset0`, `OutputAsset(1) == Asset1`
- `OutputScriptHash(0) == CurrentScriptHash()` (self-covenant)
- `newReserve0 >= reserve0` (reserve does not decrease)
- Fee-adjusted k: `(reserve0 × FEE_DEN + amountIn × FEE_NUM) × newReserve1 >= reserve0 × reserve1 × FEE_DEN`
  where `amountIn = newReserve0 - reserve0`

### pool_a_remove.shl

Spends Pool UTXO A in remove-liquidity mode. Uses `ASSERTR` (unwrap_right) to assert
`is_remove_mode = true`.

**Invariants enforced:**
- `OutputAsset(0) == Asset0`, `OutputAsset(1) == Asset1`
- `OutputScriptHash(0) == CurrentScriptHash()` (self-covenant)
- `newReserve0 < reserve0` (reserve decreases)
- Derives `totalSupply = LP_PREMINT - InputAmount(2)`
- Reads `lpReturned = InputAmount(LP_RETURN_INPUT)`
- Verifies `InputAsset(LP_RETURN_INPUT) == LP_ASSET_ID`
- `payout0 = reserve0 - newReserve0`
- Floor: `payout0 * totalSupply <= lpReturned * reserve0`
- Ceiling: `lpReturned * reserve0 <= (payout0+1) * totalSupply`

`LP_RETURN_INPUT` is the user's LP token input index (3 in the standard layout).

### pool_b_swap.shl / pool_b_remove.shl

Symmetric to pool_a variants, operating on `reserve1`/`newReserve1` and covenanting
`OutputScriptHash(1)`. Apply the same fee-adjusted k check from the Asset1 side.

### lp_reserve_add.shl

Spends the LP Reserve UTXO in add-liquidity mode. Uses `ASSERTR` to assert `is_add_mode = true`.

**Invariants enforced:**
- `OutputScriptHash(2) == CurrentScriptHash()` (self-covenant)
- `OutputAsset(2) == LP_ASSET_ID` (output carries LP tokens)
- `lpMinted = InputAmount(2) - OutputAmount(2)` (reserve decreases by lpMinted)
- `lpMinted > 0` (non-zero minting)
- Derives `totalSupply = LP_PREMINT - InputAmount(2)`, `newTotalSupply = LP_PREMINT - OutputAmount(2)`
- `deposit0 = OutputAmount(0) - InputAmount(0)`
- `deposit1 = OutputAmount(1) - InputAmount(1)`
- Proportional deposit: `deposit0 * reserve1 == deposit1 * reserve0` (floor-range check)
- LP mint floor: `lpMinted * reserve0 <= deposit0 * totalSupply`
- LP mint ceiling: `deposit0 * totalSupply <= (lpMinted+1) * reserve0`

Note: when `totalSupply = 0` (closed pool being re-opened), the proportional check
trivially passes as `0 == 0`. Any deposit ratio is accepted, and new LP is minted at
`floor(sqrt(deposit0 * deposit1))`.

### lp_reserve_remove.shl

Spends the LP Reserve UTXO in remove-liquidity mode. Uses `ASSERTL` to assert `is_add_mode = false`.

**Invariants enforced:**
- `OutputScriptHash(2) == CurrentScriptHash()` (self-covenant)
- `OutputAsset(2) == LP_ASSET_ID` (output carries LP tokens)
- `lpReturned = OutputAmount(2) - InputAmount(2)` (reserve increases by lpReturned)
- `lpReturned > 0` (non-zero return)
- Verifies `InputAsset(LP_RETURN_INPUT) == LP_ASSET_ID` (user input carries LP tokens)
- Verifies `lpReturned <= InputAmount(LP_RETURN_INPUT)` (user holds enough LP tokens)

### pool_creation.shl

One-time creation contract. Witness: `lpMinted` (uint64) = `floor(sqrt(deposit0 * deposit1))`.

- Verifies `OutputAsset(0) == Asset0`, `OutputAsset(1) == Asset1`
- Verifies `IssuanceAssetAmount(0) == LP_PREMINT` (full pre-mint)
- Verifies `OutputAmount(2) == LP_PREMINT - lpMinted` (remainder to LP Reserve)
- sqrt floor: `lpMinted^2 <= deposit0 * deposit1`
- sqrt ceiling: `(lpMinted+1)^2 > deposit0 * deposit1`

---

## Dual-Leaf Taproot

Each pool address is a **P2TR (pay-to-taproot) address** containing a two-leaf script tree.
The two leaves are the swap/add variant and the remove variant of the same contract.

```
internal_key: NUMS point (unspendable — no known discrete log)
script_tree:  leaf_0 = swap/add variant CMR
              leaf_1 = remove variant CMR
tweaked_key:  internal_key + H(internal_key || script_tree) × G
```

The **internal key** is a NUMS (nothing-up-my-sleeve) point with no known discrete log,
making the key-path spend provably unspendable. All spends must go through a script leaf.

A **control block** is required at spend time to prove which leaf is being executed. It
contains the parity of the tweaked key and the sibling leaf hash (the Merkle proof). Each
variant's control block is pre-computed at pool creation and stored in `pool.json` — it does
not change unless the contracts are recompiled.

**Why dual-leaf?** Both the swap and remove variants must be spendable from the same UTXO
address. Without a shared address, a swap would send funds to a different address than the
one remove expects, breaking the covenant chain.

---

## Simplicity Witness Format

Every Simplicity input uses the following witness stack (4 items):

```
[0] empty byte slice    — bitstring witness values (none for these contracts, but required)
[1] program bytes       — compiled Simplicity binary
[2] cmr hex             — commitment Merkle root (32 bytes, hex-encoded)
[3] control block bytes — taproot control block proving leaf membership
```

The empty first item is required even when there are no witness values. The wallet signing
flow must not touch Simplicity inputs — build and attach the Simplicity witness separately,
after `signrawtransactionwithwallet` processes the non-Simplicity inputs.

---

## LP Asset ID Derivation

`LP_ASSET_ID` is a standard Elements **deterministic asset issuance**, derived from the
creation input outpoint:

```
entropy   = SHA256d( SHA256d(txid_bytes || vout_le32) || contract_hash )
asset_id  = SHA256d( entropy || 0x00 )

where contract_hash = 32 zero bytes  (no reissuance token, no contract)
```

This is computed before the creation transaction is signed, so it can be baked into the
pool_a, pool_b, and lp_reserve contracts at compile time.

**No reissuance token.** The issuance uses `tokenAmount = 0` — no reissuance token is
created. The full LP token supply (`LP_PREMINT`) is minted once at pool creation and never
changes. This eliminates any dependency on reissuance mechanics and ensures the pool is
fully permissionless from the moment of creation.

---

## LP Token Reserve

LP tokens use a **fixed pre-mint** model instead of on-demand reissuance.

**At pool creation:**
- `LP_PREMINT = 2,000,000,000,000,000` (2 quadrillion) LP tokens are minted in a single issuance
- `lp_minted = floor(sqrt(deposit0 * deposit1))` tokens go to the pool creator
- `LP_PREMINT - lp_minted` tokens are locked in the LP Reserve UTXO

**On add-liquidity:**
- `lp_minted` tokens are transferred from the LP Reserve to the depositor
- LP Reserve decreases: `new_lp_reserve = lp_reserve - lp_minted`

**On remove-liquidity:**
- `lp_burned` tokens are returned from the user to the LP Reserve
- LP Reserve increases: `new_lp_reserve = lp_reserve + lp_burned`

**Conservation invariant:** At all times, `total_supply + lp_reserve == LP_PREMINT`. This is
enforced by the contracts — `total_supply` is always derived as `LP_PREMINT - lp_reserve`,
never stored independently.

**Why not reissuance?** Elements reissuance requires a reissuance token UTXO with a valid
`AssetBlindingNonce`. For explicit (non-confidential) UTXOs — which the protocol requires
for all inputs — the blinding factor is zero. A zero nonce signals "new issuance" to the
consensus code, not reissuance, making manual reissuance of explicit tokens impossible at
the protocol level. Even using the wallet RPC `reissueasset` centralizes LP minting to the
reissuance token holder, which is incompatible with a permissionless AMM. The LP Reserve
model eliminates all reissuance dependencies.

**Sizing `LP_PREMINT`:** The premint must fit within a create-pool transaction whose total
explicit output value (summed across all assets) cannot exceed Elements `MAX_MONEY`
(2,100,000,000,000,000). The `LP_PREMINT` of 2 quadrillion leaves ~100 trillion sats of
headroom for pool deposits, change outputs, and the fee output.

---

## Pool Lifecycle

### Zero reserves

A pool can reach `reserve0 = 0`, `reserve1 = 0`, `total_supply = 0` if all LP is returned.
The pool UTXOs remain on-chain (the covenant chain is unbroken) but are economically inert.
No swaps are meaningful at zero reserves. The LP Reserve UTXO will hold all `LP_PREMINT`
tokens.

**Re-opening:** A closed pool can be re-opened by adding liquidity. With `total_supply = 0`,
the proportional deposit check passes trivially, new LP is minted at
`floor(sqrt(deposit0 * deposit1))`, and the pool resumes normal operation.

### Duplicate pools

Multiple pools can exist for the same asset pair and fee parameters. The protocol is
permissionless and has no global registry.

**Each pool has unique addresses.** `LP_ASSET_ID` is baked into the remove and lp_reserve
contracts at compile time. Since `LP_ASSET_ID` is derived from the creation UTXO outpoint
(which is unique per pool), the remove-variant CMRs differ between pools — producing
different dual-leaf taproot addresses even when all other parameters match. Only the
swap-variant CMRs are deterministic from `(asset0, asset1, feeNum, feeDen)` alone, but
the dual-leaf address depends on both leaves.

**Pool ID = LP asset ID.** Each pool has a unique LP asset ID derived from its creation
UTXO outpoint. This is the only globally unique on-chain identifier per pool.

When multiple pools exist for the same asset pair, operations default to the pool with the
deepest liquidity. A `--pool <lp-asset-id>` flag allows explicit targeting.

### Pool discovery

Pool creation transactions include an OP_RETURN output encoding the pool parameters:

```
[5]  magic:   "ANCHR"
[32] asset0:  internal byte order
[32] asset1:  internal byte order
[2]  feeNum:  uint16 big-endian
[2]  feeDen:  uint16 big-endian
```

Pool addresses **cannot** be derived from these parameters alone — `LP_ASSET_ID` (which
affects the remove-variant CMRs and thus the taproot addresses) is only known from the
creation transaction. Discovering pools requires scanning the chain for ANCHR OP_RETURN
outputs, then decoding each creation transaction to derive the LP asset ID and pool
addresses. The `anchor find-pools` command performs this scan.

An indexed backend such as Electrs/Esplora significantly accelerates pool discovery by
providing block and transaction lookups without scanning the full UTXO set.

---

## Compile Sequence

The `anchor compile` and `anchor create-pool` commands handle this automatically.

**Phase 1 — `anchor compile`** (fee constants only, no LP asset ID yet):
```bash
# 1. Set ASSET0, ASSET1, FEE_NUM, FEE_DEN in .args files
# 2. Compile pool_creation.shl to get its CMR and binary
# 3. Write pool.json with pool_creation address
# NOTE: pool_a/b/lp_reserve addresses are NOT final here — they depend on
# LP_ASSET_ID which is only known after the creation UTXO is chosen.
```

**Phase 2 — `anchor create-pool`** (LP asset ID known from chosen creation UTXO):
```bash
# 1. Compute LP_ASSET_ID from the creation UTXO outpoint (txid, vout)
# 2. Patch LP_ASSET_ID into pool_a_swap.shl, pool_a_remove.shl,
#    pool_b_swap.shl, pool_b_remove.shl,
#    lp_reserve_add.shl, lp_reserve_remove.shl
# 3. Compile all 6 variants:

simc build/pool_a_swap.shl --json      # emits {"program": "...", "cmr": "..."}
simc build/pool_a_remove.shl --json
simc build/pool_b_swap.shl --json
simc build/pool_b_remove.shl --json
simc build/lp_reserve_add.shl --json
simc build/lp_reserve_remove.shl --json

# 4. Derive dual-leaf taproot addresses from (swapCMR, removeCMR) pairs
#    - pool_a:     (pool_a_swap, pool_a_remove)
#    - pool_b:     (pool_b_swap, pool_b_remove)
#    - lp_reserve: (lp_reserve_add, lp_reserve_remove)
# 5. Pre-compute control blocks for each variant
# 6. Write pool.json with all addresses, CMRs, binaries, control blocks, asset IDs, fee params
# 7. Sign and broadcast the pool creation transaction
```

### Why split contracts?

Each contract has two variants (swap/add and remove) because the original single contract
uses a `match` expression to detect the mode. `match` compiles to a CASE node in Simplicity,
which counts both branches toward anti-DOS weight regardless of which branch executes —
causing `SIMPLICITY_ERR_ANTIDOS = -42` at spend time.

Splitting into two variants (`ASSERTL` for swap/add, `ASSERTR` for remove) eliminates all
CASE nodes. Both variants share one taproot address via a two-leaf script tree.

---

## CLI Behavior

### Signing flow

Every pool operation must follow this sequence when broadcasting:

1. Build the transaction with Simplicity witnesses NOT attached
2. Call `signrawtransactionwithwallet` — signs wallet-owned inputs (user UTXOs)
3. Attach Simplicity witnesses to pool inputs (0, 1, 2) AFTER signing

`signrawtransactionwithwallet` cannot process pre-existing witness data. Attaching
Simplicity witnesses before signing will cause the wallet to clear or corrupt them.

### Output address requirements

All output addresses in pool transactions must be **unconfidential** (explicit). Using a
blinded address (e.g. `tlq1...` on testnet, `lq1...` on mainnet) produces a confidential
output script, which breaks explicit asset balance enforcement. The CLI calls
`getunconfidentialaddress` on all wallet-derived addresses before use.

### Proportional deposit constraint

`add-liquidity` enforces `deposit0 * reserve1 == deposit1 * reserve0` (floor-range check
as required by `lp_reserve_add.shl`). The valid deposit unit is:

```
unit0 = reserve0 / gcd(reserve0, reserve1)
unit1 = reserve1 / gcd(reserve0, reserve1)
```

Deposits must be exact multiples of `(unit0, unit1)`. If `gcd = 1`, the minimum deposit is
the full reserve ratio. The CLI snaps inputs to the nearest valid multiple and prints the
adjustment. If `deposit0 < unit0`, the operation is rejected with a clear error.

### LP return constraint

`remove-liquidity` requires that the LP token input (input[3]) contains **at least**
`lpBurned` tokens — `lp_reserve_remove.shl` asserts `lpBurned <= InputAmount(LP_RETURN_INPUT)`.
Partial returns from a single UTXO are supported: `lpBurned` LP tokens are returned to the
LP Reserve and any remainder is returned to the user as a change output. There is no need
to pre-split the LP UTXO.

### Fee estimation

The `--fee` flag accepts an explicit satoshi amount. When omitted, `estimatesmartfee(2)`
is called and the result (in sat/vB) is multiplied by an empirical vbyte estimate:

| Command | vByte estimate | Basis |
|---------|---------------|-------|
| `create-pool` | 1600 | estimate |
| `swap` | 1200 | estimate |
| `add-liquidity` | 1400 | estimate (3 pool inputs + user inputs) |
| `remove-liquidity` | 1400 | similar to add-liquidity |

Simplicity witnesses are large (dominant fraction of tx weight). The discount factor for
witness data means virtual size ≈ 42% of raw byte count for these transactions. Two-pass
fee estimation (build → measure → rebuild) is a planned improvement.

---

## Security Properties

- **No admin key** — contracts are immutable once deployed
- **No oracle** — all checks are over transaction data only
- **No reissuance dependency** — LP tokens are fully pre-minted; no party holds a reissuance token
- **Proportionality guarantees** — floor/ceiling arithmetic ensures LP minting and burning round in the pool's favor, bounded by ±1 satoshi
- **Asset binding** — constants `Asset0`, `Asset1`, `LP_ASSET_ID` are baked into each contract's CMR; substituting a different asset will produce a different script hash and fail the covenant check
- **Self-covenant** — each pool UTXO checks its own script hash in the output, forming a permanent chain
- **Unspendable key path** — the NUMS internal key makes key-path taproot spends provably impossible; the pool can only be spent via a valid Simplicity script leaf
- **LP Reserve integrity** — the LP Reserve contract only releases tokens during valid add-liquidity operations and only accepts tokens during valid remove-liquidity operations; direct withdrawal is impossible

---

## Transaction Chaining and Mempool Considerations

### The single-spender bottleneck

Each pool operation consumes the current pool UTXOs and produces new ones. Only one transaction
can spend a given UTXO — so two concurrent operations against the same confirmed pool state will
conflict, and only one will be accepted. On a chain with 1-minute block times this is manageable
at low volume, but at scale it becomes the primary throughput constraint.

### Transaction chaining

Clients SHOULD implement **transaction chaining**: building a new operation that spends the
*unconfirmed outputs* of a pending pool transaction rather than waiting for confirmation. A chain
of N pending swaps can execute within a single block interval, giving the pool effective
throughput of N operations per block instead of 1.

Chaining requires knowing the exact outpoints and output values of pending transactions before
they confirm. This is not practical with the Elements chain RPC alone (`gettxout` only returns
confirmed UTXOs, and `getrawmempool` + decode is slow). Clients that implement chaining will
need an indexed mempool-aware backend such as **Electrs/Esplora**, which provides:

- `/address/:addr/txs/mempool` — pending transactions at a pool address
- `/tx/:txid` — full decoded transaction (outputs, values, assets)
- `/address/:addr/utxo` — confirmed + unconfirmed UTXOs at an address

Without an indexed backend, clients are limited to one operation per block per pool.

### Chain invalidation and fee competition

A transaction chain is only as strong as its first link. If any transaction in the chain is
replaced or outbid by a conflicting transaction that spends the same confirmed pool UTXOs, the
entire chain is invalidated.

This creates a tension: a single high-fee transaction that conflicts with the base of a chain
can invalidate N pending operations. Clients are **discouraged** from broadcasting transactions
that spend confirmed pool UTXOs when a pending chain already exists at that pool — doing so
invalidates every chained transaction, harming all users who built on top of the chain.

That said, this cannot be enforced at the protocol level, and in a free market the ability to
outbid exists for good reason — it is the mechanism by which price-sensitive actors correct
stale chains or capture arbitrage. A sufficiently sophisticated client could even detect an
existing chain, evaluate whether outbidding it is profitable, and offer that option to the user.

### Practical impact

With Liquid's 1-minute block times, the single-spender bottleneck is unlikely to be a problem
for most pools in early adoption. Transaction chaining becomes important when:

- A pool sees sustained volume (multiple swaps per minute)
- Liquidity operations need to execute without waiting for confirmation
- Multiple users interact with the same pool concurrently

Clients that only target single-user or low-frequency use cases can safely ignore chaining and
simply retry on `txn-mempool-conflict` after the next block.
