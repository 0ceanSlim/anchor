# Anchor AMM — Lessons Learned

Architecture mistakes, dead ends, and protocol-level gotchas encountered during development.
Kept as a reference to avoid repeating them.

---

## 1. Reissuance Is Impossible with Explicit UTXOs

**The fumble:** Spent days building LP token reissuance (reissuance token output, entropy
tracking, `ReissueAsset` RPC calls, reissuance token UTXO chaining across add-liquidity
operations). The entire approach was fundamentally broken.

**Why it failed:** Elements reissuance requires a non-zero `AssetBlindingNonce` in the
issuance input. For explicit (non-confidential) UTXOs, the blinding factor is zero. A zero
nonce signals "new issuance" to consensus — not reissuance. The `reissueasset` wallet RPC
works around this by using confidential transactions internally, but manually-built explicit
transactions cannot reissue assets at all.

**The fix:** LP Reserve model — premint all LP tokens at pool creation, lock the surplus in
a contract-controlled reserve UTXO, draw from/return to reserve on add/remove liquidity.
No reissuance needed.

**Lesson:** When building on an unfamiliar consensus layer, verify fundamental assumptions
(like "can I reissue this asset?") with a minimal test transaction before building
infrastructure around them.

---

## 2. Elements MAX_MONEY Is Cross-Asset

**The fumble:** Set `LP_PREMINT = 2^53 - 1` (9 quadrillion), then reduced to
`MAX_MONEY = 2,100,000,000,000,000` (2.1 quadrillion). Both failed with
`bad-txns-txouttotal-toolarge`.

**Why it failed:** Elements inherited Bitcoin's `CheckTransaction` which sums ALL explicit
output values in a transaction — regardless of asset type. The LP Reserve output (2.1Q LP
tokens) plus deposit outputs (L-BTC + Asset1) plus fee exceeded `MAX_MONEY` in total. This
is not just a per-output limit; it's a per-transaction total limit across all assets.

**The fix:** `LP_PREMINT = 2,000,000,000,000,000` (2 quadrillion) — leaves ~100 trillion
sats of headroom for deposits, change, and fee outputs.

**Lesson:** Read the consensus validation code, not just the documentation. The cross-asset
sum constraint is inherited from Bitcoin and not prominently documented in Elements.

---

## 3. lp_supply Was Redundant with LP Reserve

**The fumble:** Initially planned to keep the `lp_supply` UTXO alongside the LP Reserve.
The lp_supply UTXO tracked total supply via an L-BTC-denominated counter. With LP Reserve,
total supply is trivially `LP_PREMINT - reserve_amount` — the counter is redundant.

**Why it was wrong:** Two UTXOs tracking the same state is unnecessary complexity. The
reserve amount IS the supply state. Every LP Reserve withdrawal/deposit implicitly updates
total supply.

**The fix:** Replaced `lp_supply` entirely with `lp_reserve`. Pool is 3 UTXOs:
`pool_a`, `pool_b`, `lp_reserve`.

**Lesson:** When replacing a subsystem, check whether the old bookkeeping is still needed.
Often the new architecture makes it derivable.

---

## 4. CASE Nodes Hit Anti-DOS Limits

**The fumble:** Original contracts used Simplicity `CASE` nodes to branch between swap and
remove modes. These create exponential path analysis for the anti-DOS checker.

**Why it failed:** `simc` / Elements anti-DOS validation rejects programs that exceed a
cost threshold. CASE nodes double the analysis paths. A contract with even moderate logic
plus a CASE node can exceed the limit.

**The fix:** Split each contract into two variants (e.g., `pool_a_swap.shl` and
`pool_a_remove.shl`). Use `ASSERTL`/`ASSERTR` (via `unwrap_left`/`unwrap_right`) instead
of `CASE`. Both variants are placed in a 2-leaf taproot tree — same address, but only one
leaf executes per spend.

**Lesson:** Simplicity's anti-DOS model penalizes branching heavily. Prefer assertion-based
path selection over case-based branching.

---

## 5. signrawtransactionwithwallet Destroys Witnesses

**The fumble:** Built Simplicity witnesses, attached them to pool inputs, then called
`signrawtransactionwithwallet` to sign the user's key-spend inputs. The RPC call stripped
all pre-existing witness data.

**The fix:** Sign first (which only touches key-spend inputs), then attach Simplicity
witnesses to pool inputs after signing.

**Lesson:** Wallet RPCs assume they own the entire transaction. Attach non-standard witness
data after all wallet operations are complete.

---

## 6. Confidential UTXOs Break Explicit Transactions

**The fumble:** Used `GetNewAddress()` for change/payout addresses. Elements returns
confidential (`tlq1...`) addresses by default. Outputs to confidential addresses are
blinded, which breaks explicit value balance validation in manually-built transactions.

**Why it failed:** Pool inputs are explicit (non-confidential). If any output is
confidential, Elements requires a proper blinding proof for value balance. Manually-built
transactions don't include blinding proofs.

**The fix:** All auto-derived addresses use `GetUnconfidentialAddress()` to get explicit
`tex1...` addresses. Every input and output in pool transactions must be explicit.

**Lesson:** On Liquid/Elements, "confidential by default" means every address derivation
must explicitly opt out of confidentiality for manual transaction building.

---

## 7. OP_RETURN Outputs Can't Hold Non-Zero Asset Value

**The fumble:** Tried to burn LP tokens by sending them to an OP_RETURN output with explicit
asset value. Elements rejected this as dust.

**Why it failed:** OP_RETURN outputs are `IsUnspendable()` but Elements dust rules still
apply to explicit asset outputs on unspendable scripts (at least in the version tested).

**The fix (at the time):** Sent LP tokens to a valid address instead of burning. Later made
moot by the LP Reserve model — LP tokens return to the reserve rather than being burned.

**Lesson:** Don't assume Bitcoin idioms (OP_RETURN burn) work identically on Elements.
Test dust rules for non-L-BTC assets on OP_RETURN.

---

## 8. LP_PREMINT = 2^53-1 Exceeded Single Output Limit Too

**The fumble:** Before discovering the cross-asset sum issue (#2), the first failure was
`bad-txns-vout-toolarge` — a single output exceeded `MAX_MONEY`.

**Why it matters:** Even if the cross-asset sum wasn't an issue, no single output can exceed
`MAX_MONEY`. The 2^53-1 value (9Q) is 4x larger than MAX_MONEY (2.1Q).

**Lesson:** `MAX_MONEY` constrains both individual outputs AND the total. Check both.

---

## 9. Swap Contracts Must Not Assert Reserve Non-Decrease

**The fumble:** `pool_a_swap` and `pool_b_swap` originally asserted that their respective
reserve did not decrease. This is correct for the receiving side but always fails on the
paying side — in any swap, one reserve increases and the other decreases.

**The fix:** Removed the non-decrease assertion from swap variants. The fee-adjusted
k-invariant (`new_reserve0 * new_reserve1 >= k * FEE_DEN^2 / FEE_NUM^2`) is the sole
security guarantee. Each pool contract verifies its own asset identity and that the
k-invariant holds.

**Lesson:** In a constant-product AMM, the k-invariant IS the security model. Per-reserve
monotonicity constraints are wrong by definition — swaps require one side to decrease.

---

## 10. go-elements Stores Explicit Values as Big-Endian

**The fumble:** `exactOutputSatoshis` in `pkg/pool/state.go` read raw output `.Value` bytes
with `binary.LittleEndian.Uint64(val[1:9])`. This produced garbage values (e.g.,
`12907191957800024320` instead of `6324`), cascading into wrong `TotalSupply()`, wrong
`LPMintedForDeposit()`, and `bad-txns-vout-toolarge` on add-liquidity.

**Why it failed:** go-elements' `ValueToBytes` writes the uint64 as little-endian, then
reverses the byte slice — so the stored format is `0x01 || BE8(value)`. Reading it back
as little-endian inverts the byte order.

**The fix:** Replace manual decoding with `elementsutil.ValueFromBytes(val)`, which handles
the prefix and byte-order reversal correctly.

**Lesson:** Never hand-decode go-elements internal byte formats. Use the library's own
encode/decode functions — they handle non-obvious byte-order conventions.

---

## 11. Wallet listunspent Returns Confidential UTXOs

**The fumble:** After fixing the endianness bug (#10), add-liquidity still failed with
`bad-txns-in-ne-out`. The wizard was selecting wallet UTXOs via `listunspent` without
checking whether they were confidential (blinded).

**Why it failed:** On Liquid, wallet UTXOs are confidential by default. `listunspent`
decrypts amounts for display, but the on-chain UTXO has a Pedersen commitment. Using a
confidential input in a transaction with explicit outputs breaks value balance verification —
Elements can't prove inputs = outputs when some are hidden behind commitments.

**The fix:** Filter wallet UTXOs on the `amountblinder` field. All zeros (or empty) means
explicit. Added `WalletUTXO.IsExplicit()` method, applied in `ListUnspentByAsset` and
`walletExplicitAssets`.

**Lesson:** `listunspent` showing an amount does NOT mean the UTXO is explicit. Always
check `amountblinder` before using wallet UTXOs in manually-built explicit transactions.

---

## 12. Single-UTXO Selection Breaks on Fragmented Balances

**The fumble:** The create-pool wizard detected an existing pool and redirected to
add-liquidity. The user had 2487 sats of Asset1 split across two UTXOs (~1487 + ~1000).
`autoSel` only picked one UTXO — neither alone covered the 1653 sats needed. Error:
`no suitable asset1 UTXO (need 1653 sats)`.

**The fix:** Replaced `autoSel` with `selectInputs` that tries the smallest sufficient
single UTXO first, then falls back to combining multiple UTXOs largest-first. Changed
`AddLiquidityParams` from single UTXO fields to `[]UserInput` slices.

**Lesson:** Never assume a user's balance for an asset lives in a single UTXO. UTXO-based
systems fragment balances naturally — always support multi-UTXO input selection.

---

## 13. LP Asset ID Detection Must Not Rely on OP_RETURN Alone

**The fumble:** `resolveLPAsset` walked `vin[0]` backward looking for the ANCHR OP_RETURN
announcement to identify the pool creation tx. Pools created without an announcement (or
with `--no-announce`) caused `ANCHR OP_RETURN not found after 200 hops`.

**The fix:** Check `vin[0].Issuance != nil` as the primary creation tx signal — the LP
issuance event is always present regardless of announcement. Fall back to OP_RETURN as
secondary.

**Lesson:** Use protocol-inherent signals (issuance events) over application-layer
conventions (OP_RETURN) for critical detection logic. Application conventions are optional;
protocol events are guaranteed.

---

## 14. Track UTXO Amounts Before User Prompts Modify Them

**The fumble:** `remove-liquidity` captured `lpUTXOAmount := lpAmount` after the wizard
prompt had already reduced `lpAmount` from 35355 to 17677 (user chose 50%). The tx builder
received `UserLPAmount = 17677` and `LPBurned = 17677`, saw no change needed, and produced
no LP change output. The actual UTXO input had 35355 tokens — `value in != value out`.

**The fix:** Capture `lpUTXOAmount` immediately after UTXO selection, before any user
prompt can modify `lpAmount`.

**Lesson:** When a variable serves double duty (display to user AND tx builder input),
snapshot the original value before any interactive modification. The tx builder needs the
real UTXO amount, not the user's requested subset.

---

## 15. Swap Transactions Need Change Outputs

**The fumble:** `BuildSwap` assumed the user UTXO contained exactly `amountIn + fee` and
produced no change output. In practice, UTXO auto-selection picks the smallest UTXO that
*covers* the needed amount — any surplus was silently lost, causing `value in != value out`.

**Also:** The swap tx assumed the fee could always come from the user's input UTXO. This
only works when the input asset is L-BTC. Swapping a non-L-BTC asset had no way to pay the
miner fee.

**The fix:** Added `UserInputAmount`, `ChangeAddr`, and separate fee UTXO fields
(`FeeTxID`/`FeeVout`/`FeeAmount`) to `SwapParams`. `BuildSwap` now emits change outputs for
input asset surplus and L-BTC fee surplus. When input asset != L-BTC, a separate L-BTC UTXO
is selected for the fee.

**Lesson:** Every tx builder must account for the full value of every input. "Exact amount"
UTXOs are a test convenience — real wallets have fragmented balances. Always emit change.

---

## 16. Multiple Buffered Readers on stdin Steal Bytes

**The fumble:** `resolvePoolFile` used `bufio.NewScanner(os.Stdin)` for its prompt while
wizard code used a global `stdinReader = bufio.NewReader(os.Stdin)`. Two buffered readers on
the same underlying `os.Stdin` race to consume bytes — one reader's buffer steals input
intended for the other, causing prompts to silently skip or read garbage.

**The fix:** Single shared `stdinReader` (`bufio.NewReader`) used by all prompt helpers
(`promptString`, `promptChoice`, `promptUint64`, `promptMultiChoice`). Removed all
`bufio.NewScanner(os.Stdin)` and `fmt.Scanln` from command files.

**Lesson:** Never create multiple buffered readers on a shared unbuffered source. Use a
single shared reader and route all reads through it.

---

## 17. LP_ASSET_ID Makes Pool Addresses Unique — Compile-and-Scan Doesn't Work

**The fumble:** Pool discovery tried to find pools by compiling contracts with
`(asset0, asset1, feeNum, feeDen)` and scanning for the resulting address. This always
failed because `LP_ASSET_ID` is baked into the remove-variant and lp_reserve contract CMRs,
which affect the dual-leaf taproot address. Without the correct LP_ASSET_ID (derived from
the creation tx's vin[0] outpoint), the compiled address doesn't match.

**The fix:** Pool discovery scans the chain for ANCHR OP_RETURN creation announcements,
extracts pool addresses from the creation tx outputs, and only then compiles contracts with
the correct LP_ASSET_ID to verify compatibility.

**Lesson:** When contract parameters are baked into the address derivation, you cannot
derive the address without all parameters. If a parameter is only known from on-chain data,
you must scan first, compile second.
