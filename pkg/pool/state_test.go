package pool

import "testing"

func TestSwapOutput(t *testing.T) {
	tests := []struct {
		name       string
		amountIn   uint64
		reserveIn  uint64
		reserveOut uint64
		feeNum     uint64
		feeDen     uint64
		wantOut    uint64
	}{
		// Zero-fee (feeNum==feeDen==1): standard constant-product formula
		{"no-fee basic", 1000, 100000, 100000, 1, 1, 990},
		{"no-fee large", 10000, 100000, 100000, 1, 1, 9090},
		{"no-fee tiny", 1, 100000, 100000, 1, 1, 0},
		{"no-fee zero in", 0, 100000, 100000, 1, 1, 0},
		{"no-fee asymmetric", 500, 10000, 50000, 1, 1, 2380},
		// 0.3% fee (feeNum=997, feeDen=1000)
		{"0.3% fee basic", 1000, 100000, 100000, 997, 1000, 987},
		{"0.3% fee large", 10000, 100000, 100000, 997, 1000, 9066},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SwapOutput(tc.amountIn, tc.reserveIn, tc.reserveOut, tc.feeNum, tc.feeDen)
			if got != tc.wantOut {
				t.Errorf("SwapOutput(%d, %d, %d, %d, %d) = %d, want %d",
					tc.amountIn, tc.reserveIn, tc.reserveOut, tc.feeNum, tc.feeDen, got, tc.wantOut)
			}
		})
	}
}

func TestSwapOutputFeeInvariant(t *testing.T) {
	// After a fee-adjusted swap, the fee invariant must hold:
	// (r0*(D-N) + newR0*N) * newR1 >= r0*D * r1
	r0, r1 := uint64(100000), uint64(200000)
	amtIn := uint64(5000)
	feeNum, feeDen := uint64(997), uint64(1000)
	amtOut := SwapOutput(amtIn, r0, r1, feeNum, feeDen)
	newR0 := r0 + amtIn
	newR1 := r1 - amtOut
	feeDiff := feeDen - feeNum
	lhs := (r0*feeDiff + newR0*feeNum) * newR1
	rhs := r0 * feeDen * r1
	if lhs < rhs {
		t.Errorf("fee invariant violated: lhs=%d rhs=%d", lhs, rhs)
	}
}

func TestLPMintedForDeposit(t *testing.T) {
	tests := []struct {
		name        string
		d0, d1      uint64
		r0, r1      uint64
		supply      uint64
		wantMinimum uint64
	}{
		{"proportional deposit", 1000, 2000, 10000, 20000, 1000, 100},
		{"skewed to 0", 500, 2000, 10000, 20000, 1000, 50},  // min(50, 100) = 50
		{"zero deposit", 0, 1000, 10000, 20000, 1000, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := LPMintedForDeposit(tc.d0, tc.d1, tc.r0, tc.r1, tc.supply)
			if got < tc.wantMinimum {
				t.Errorf("LPMintedForDeposit(%d, %d, %d, %d, %d) = %d, want >= %d",
					tc.d0, tc.d1, tc.r0, tc.r1, tc.supply, got, tc.wantMinimum)
			}
		})
	}
}

func TestRemovePayouts(t *testing.T) {
	tests := []struct {
		name          string
		burned        uint64
		r0, r1, supply uint64
		want0, want1  uint64
	}{
		{"half remove", 500, 10000, 20000, 1000, 5000, 10000},
		{"full remove", 1000, 10000, 20000, 1000, 10000, 20000},
		{"small remove", 1, 10000, 20000, 1000, 10, 20},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p0, p1 := RemovePayouts(tc.burned, tc.r0, tc.r1, tc.supply)
			if p0 != tc.want0 || p1 != tc.want1 {
				t.Errorf("RemovePayouts(%d, %d, %d, %d) = (%d, %d), want (%d, %d)",
					tc.burned, tc.r0, tc.r1, tc.supply, p0, p1, tc.want0, tc.want1)
			}
		})
	}
}

func TestIntSqrt(t *testing.T) {
	tests := []struct {
		n    uint64
		want uint64
	}{
		{0, 0},
		{1, 1},
		{3, 1},
		{4, 2},
		{8, 2},
		{9, 3},
		{15, 3},
		{16, 4},
		{100, 10},
		{1000000, 1000},
		{999999, 999},
	}
	for _, tc := range tests {
		got := IntSqrt(tc.n)
		if got != tc.want {
			t.Errorf("IntSqrt(%d) = %d, want %d", tc.n, got, tc.want)
		}
	}
}
