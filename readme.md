# <img src="docs/anchor.svg" alt="anchor" width="64" height="64"> anchor

Immutable, permissionless constant-product AMM for [Liquid](https://liquid.net).

anchor uses [Simplicity](https://github.com/BlockstreamResearch/Simplicity) contracts to enforce AMM rules directly on-chain. Pools are covenant-controlled UTXOs with no admin key, no upgrade path, and no off-chain coordinator.

Anyone can deploy a pool between two Liquid assets, provide liquidity, and earn swap fees proportional to their share of the pool.

---

# ⚠️ Early Development

anchor is **early-stage experimental software**.

This repository is public so the protocol and implementation can receive review. The codebase currently contains incomplete features, bugs, and breaking changes.

**Do not use with real funds.**

---

# Documentation

- **Protocol specification**
  [docs/spec.md](docs/spec.md)

- **Current development roadmap**
  [docs/status-wip.md](docs/status-wip.md)

- **Lessons learned while building anchor**
  [docs/lessons.md](docs/lessons.md)

- **Very early frontend concept**
  [docs/concept.html](docs/concept.html)

---

# Build

Requirements:

- Go 1.21+
- Rust / cargo
- Elements / Liquid node

Clone the repository:

```bash
git clone https://github.com/0ceanslim/anchor
cd anchor
```

Install the patched Simplicity compiler:

```bash
make install-simc
```

Install the Go → SimplicityHL transpiler:

```bash
make install-simgo
```

Build the CLI:

```bash
make build
```

Binary will be created at:

```
bin/anchor
```

---

# CLI Usage

Wizard-style flows are currently being implemented, but **all commands below work today using flags**.

---

## Check environment

Validate RPC connectivity and tool availability before doing anything:

```bash
./anchor check
```

---

## Query pool state

```bash
./anchor pool-info
```

Prints current reserves, LP supply, and implied price from chain. This requires a pool.json currently. Pool discovery and info by lp-asset flag is planned. You can use the example in this repo which is a live pool on testnet right now.

```bash
./anchor pool-info --pool pool.example.json
```

---

## Swap

```bash
./anchor swap \
  --amount-in <satoshis> \
  --asset-in <asset-id-hex> \
  --user-utxo <txid:vout> \
  --user-addr <your-address> \
  [--broadcast]
```

Without `--broadcast` the signed transaction hex is printed but not sent.

---

## Add liquidity

```bash
./anchor add-liquidity \
  --deposit0 <satoshis> \
  --deposit1 <satoshis> \
  --user-addr <your-address> \
  [--broadcast]
```

Deposits Asset0 and Asset1 proportionally and mints LP tokens to `--user-addr`.

---

## Remove liquidity

```bash
./anchor remove-liquidity \
  --lp-amount <lp-tokens-to-burn> \
  --lp-utxo <txid:vout> \
  --user-addr0 <address-for-asset0-payout> \
  --user-addr1 <address-for-asset1-payout> \
  [--broadcast]
```

Burns LP tokens and returns proportional Asset0 and Asset1 to the specified addresses.

---

# Pool Deployment (Advanced)

Each pool is an independent deployment with its own asset IDs, fee rate, and LP token issuance.

Basic workflow:

1. Pick **Asset0** and **Asset1** (any two Liquid assets).
2. Choose a **fee rate**.
3. Compile contracts:

```
./anchor compile
```

4. Create the pool:

```
./anchor create-pool ...
```

The LP asset ID is deterministically derived from the pool creation input outpoint before the transaction is signed, allowing it to be embedded in the contracts before deployment.

Full protocol details are documented in
[docs/spec.md](docs/spec.md).

---

# Repository Layout

```
cmd/anchor/      CLI entrypoint
pkg/             Core implementation
contracts/       Canonical contract logic (Go → SimplicityHL)
build/           Generated contract files
tests/           Integration tests
docs/            Protocol documentation and notes
```

---

# License

MIT License

© 2025–2026 0ceanslim
