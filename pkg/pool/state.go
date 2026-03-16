package pool

import (
	"fmt"
	"math"
	"math/big"

	"github.com/0ceanslim/anchor/pkg/rpc"
	"github.com/vulpemventures/go-elements/elementsutil"
	"github.com/vulpemventures/go-elements/transaction"
)

// State holds live on-chain pool reserves.
type State struct {
	Reserve0       uint64 // pool_a asset0 satoshis
	Reserve1       uint64 // pool_b asset1 satoshis
	LPReserve      uint64 // lp_reserve LP token amount
	PoolATxID      string
	PoolAVout      uint32
	PoolBTxID      string
	PoolBVout      uint32
	LpReserveTxID  string
	LpReserveVout  uint32
}

// TotalSupply derives the circulating LP token supply from the reserve.
func (s *State) TotalSupply() uint64 {
	return LPPremint - s.LPReserve
}

// Query fetches live pool state from the chain.
func Query(cfg *Config, client *rpc.Client) (*State, error) {
	poolA, err := findUTXO(client, cfg.PoolA.Address)
	if err != nil {
		return nil, fmt.Errorf("pool_a: %w", err)
	}
	poolB, err := findUTXO(client, cfg.PoolB.Address)
	if err != nil {
		return nil, fmt.Errorf("pool_b: %w", err)
	}
	lpReserve, err := findUTXO(client, cfg.LpReserve.Address)
	if err != nil {
		return nil, fmt.Errorf("lp_reserve: %w", err)
	}

	// LP reserve amounts are large (up to 2 quadrillion), which exceeds safe float64
	// round-trip precision when divided by 1e8.  Parse the raw transaction to read
	// the exact 64-bit satoshi value from the serialized output bytes.
	lpReserveExact, err := exactOutputSatoshis(client, lpReserve.TxID, lpReserve.Vout)
	if err != nil {
		// Fall back to float64 conversion if raw parsing fails.
		lpReserveExact = satoshis(lpReserve.Amount)
	}

	return &State{
		Reserve0:      satoshis(poolA.Amount),
		Reserve1:      satoshis(poolB.Amount),
		LPReserve:     lpReserveExact,
		PoolATxID:     poolA.TxID,
		PoolAVout:     poolA.Vout,
		PoolBTxID:     poolB.TxID,
		PoolBVout:     poolB.Vout,
		LpReserveTxID: lpReserve.TxID,
		LpReserveVout: lpReserve.Vout,
	}, nil
}

// exactOutputSatoshis fetches the raw transaction and reads the satoshi amount
// of a specific output directly from the serialized explicit value field.
// An explicit output value is: 0x01 (1 byte) | amount (8 bytes, big-endian).
// This avoids the float64 precision loss that occurs with scantxoutset/gettxout
// for large LP token amounts (up to 2 quadrillion).
func exactOutputSatoshis(client *rpc.Client, txid string, vout uint32) (uint64, error) {
	rawHex, err := client.GetRawTransaction(txid)
	if err != nil {
		return 0, fmt.Errorf("getrawtransaction %s: %w", txid, err)
	}
	parsedTx, err := transaction.NewTxFromHex(rawHex)
	if err != nil {
		return 0, fmt.Errorf("parse tx %s: %w", txid, err)
	}
	if int(vout) >= len(parsedTx.Outputs) {
		return 0, fmt.Errorf("vout %d out of range (tx has %d outputs)", vout, len(parsedTx.Outputs))
	}
	val := parsedTx.Outputs[vout].Value
	// Use go-elements' own decoder — it handles the 0x01 prefix + big-endian bytes.
	sats, err := elementsutil.ValueFromBytes(val)
	if err != nil {
		return 0, fmt.Errorf("output %d: %w", vout, err)
	}
	return sats, nil
}

func findUTXO(client *rpc.Client, addr string) (*rpc.ScanResult, error) {
	results, err := client.ScanAddress(addr)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no UTXO at %s", addr)
	}
	if len(results) > 1 {
		return nil, fmt.Errorf("multiple UTXOs at %s — pool is in unexpected state", addr)
	}
	return &results[0], nil
}

func satoshis(btc float64) uint64 {
	return uint64(math.Round(btc * 1e8))
}

// SwapOutput computes the maximum output amount for a fee-adjusted swap.
// Formula: out = reserveOut * amountIn * feeNum / (reserveIn * feeDen + amountIn * feeNum)
// This is the fee-adjusted constant-product invariant used by the Simplicity contracts.
// If feeNum or feeDen is zero (e.g. legacy pool.json without fee fields), falls back to
// the no-fee formula (feeNum = feeDen = 1), which is the standard constant-product k formula.
// Uses big.Int to avoid overflow for mainnet-scale reserves.
func SwapOutput(amountIn, reserveIn, reserveOut, feeNum, feeDen uint64) uint64 {
	if feeNum == 0 || feeDen == 0 {
		feeNum, feeDen = 1, 1
	}
	num := new(big.Int).SetUint64(reserveOut)
	num.Mul(num, new(big.Int).SetUint64(amountIn))
	num.Mul(num, new(big.Int).SetUint64(feeNum))

	den := new(big.Int).SetUint64(reserveIn)
	den.Mul(den, new(big.Int).SetUint64(feeDen))
	den.Add(den, new(big.Int).Mul(new(big.Int).SetUint64(amountIn), new(big.Int).SetUint64(feeNum)))

	return new(big.Int).Div(num, den).Uint64()
}

// LPMintedForDeposit computes LP tokens to mint for add-liquidity.
func LPMintedForDeposit(deposit0, deposit1, reserve0, reserve1, totalSupply uint64) uint64 {
	// lp = floor(min(deposit0 * supply / reserve0, deposit1 * supply / reserve1))
	lp0 := deposit0 * totalSupply / reserve0
	lp1 := deposit1 * totalSupply / reserve1
	if lp0 < lp1 {
		return lp0
	}
	return lp1
}

// RemovePayouts computes proportional payouts for remove-liquidity.
func RemovePayouts(lpBurned, reserve0, reserve1, totalSupply uint64) (payout0, payout1 uint64) {
	payout0 = lpBurned * reserve0 / totalSupply
	payout1 = lpBurned * reserve1 / totalSupply
	return
}

// IntSqrt computes floor(sqrt(n)) using integer arithmetic.
func IntSqrt(n uint64) uint64 {
	if n == 0 {
		return 0
	}
	x := uint64(math.Sqrt(float64(n)))
	// Correct for floating-point rounding
	for x*x > n {
		x--
	}
	for (x+1)*(x+1) <= n {
		x++
	}
	return x
}
