# Anchor AMM — Test Suite

## Quick Reference

| Command | What | Requirements | Time |
|---------|------|-------------|------|
| `go test ./pkg/...` | Unit tests | None | <5s |
| `go test -tags esplora ./tests/...` | Esplora API | ANCHOR_ESPLORA_URL | ~10s |
| `go test -tags integration ./tests/... -timeout 60m` | Full lifecycle | RPC + simc + testnet | 10-30min |

## Unit Tests (in-package, no build tag)

- `pkg/pool/state_test.go` — SwapOutput, LPMintedForDeposit, RemovePayouts, IntSqrt
- `pkg/pool/config_test.go` — Config round-trip, TotalSupply
- `pkg/taproot/taproot_test.go` — Address derivation, ControlBlock, dual-leaf
- `pkg/compiler/compiler_test.go` — simc output parsing (decodeOutput)
- `pkg/tx/tx_test.go` — Witness builders, issuance entropy, byte helpers
- `pkg/esplora/scan_test.go` — ANCHR OP_RETURN parsing
- `pkg/rpc/scan_test.go` — ANCHR OP_RETURN parsing (RPC variant)

## Integration Tests (`tests/`)

- `integration_test.go` (tag: `integration`) — Full lifecycle on testnet
- `esplora_test.go` (tag: `esplora`) — Esplora API coverage

## Environment Variables

| Variable | Tags | Default |
|----------|------|---------|
| ANCHOR_RPC_URL | integration | http://localhost:18884 |
| ANCHOR_RPC_USER | integration | — |
| ANCHOR_RPC_PASS | integration | — |
| ANCHOR_ESPLORA_URL | esplora | (skips if unset) |
| SIMC_PATH | integration | ./bin/simc |
