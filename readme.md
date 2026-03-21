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

- **CLI reference** — all commands and flags
  [docs/cli.md](docs/cli.md)

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

All commands support interactive **wizard mode** — run without flags and the CLI will prompt for everything it needs. Every command also works fully non-interactively via flags.

Set up your environment once:

```bash
export ANCHOR_RPC_URL=http://localhost:7041
export ANCHOR_RPC_USER=user
export ANCHOR_RPC_PASS=pass
export ANCHOR_NETWORK=testnet
export ANCHOR_ESPLORA_URL=http://localhost:3000
```

### Commands

| Command | Description |
|---------|-------------|
| `check` | Validate RPC, Esplora, compiler, and pool config |
| `compile` | Compile Simplicity contracts (developer tool) |
| `find-pools` | Discover pools by asset pair or pool ID |
| `pool-info` | Query live pool reserves and price |
| `create-pool` | Deploy a new AMM pool |
| `swap` | Swap between pool assets |
| `add-liquidity` | Deposit assets and receive LP tokens |
| `remove-liquidity` | Burn LP tokens and withdraw reserves |

### Quick Examples

```bash
# Find a pool by its LP asset ID
./anchor find-pools --pool-id <lp-asset-hex>

# Query pool reserves
./anchor pool-info --pool-id <lp-asset-hex>

# Interactive swap (prompts for everything)
./anchor swap

# Non-interactive swap
./anchor swap --pool-id <lp-asset-hex> --in-asset asset0 --amount 10000 --broadcast
```

Full flag reference: **[docs/cli.md](docs/cli.md)**

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
