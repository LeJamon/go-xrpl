package payment

import (
	"testing"

	tx "github.com/LeJamon/go-xrpl/internal/tx"
)

// usd builds an IOU amount with the given mantissa/exponent in the USD currency.
func usd(mantissa int64, exponent int) tx.Amount {
	return tx.NewIssuedAmount(mantissa, exponent, "USD", "")
}

// TestSetCacheLimiting_LargeDiffReplacesCache exercises the large-difference
// branch of DirectStepI.setCacheLimiting (rippled DirectStep.cpp:590-630): when
// the forward input exceeds the cached reverse input by more than 1e-9 and the
// mantissa ratio is above 1.01 (or the exponent differs / cached mantissa is
// zero), the entire cache is replaced with the forward values rather than
// clamped to the per-field minimums.
func TestSetCacheLimiting_LargeDiffReplacesCache(t *testing.T) {
	tests := []struct {
		name string
		// cached reverse-pass values
		cacheIn, cacheSrcToDst, cacheOut tx.Amount
		// forward-pass values
		fwdIn, fwdSrcToDst, fwdOut tx.Amount
		// expected post-call cache
		wantIn, wantSrcToDst, wantOut tx.Amount
	}{
		{
			// fwdIn = 2.0, cacheIn = 1.0: same exponent, ratio 2.0 > 1.01,
			// diff 1.0 > 1e-9 → REPLACE. The forward srcToDst/out are LARGER
			// than the cached ones, so a min-clamp would have kept the cached
			// values; seeing the forward values proves the cache was replaced.
			name:          "replace on mantissa ratio above 1.01",
			cacheIn:       usd(1000000000000000, -15), // 1.0
			cacheSrcToDst: usd(1000000000000000, -15), // 1.0
			cacheOut:      usd(1000000000000000, -15), // 1.0
			fwdIn:         usd(2000000000000000, -15), // 2.0
			fwdSrcToDst:   usd(3000000000000000, -15), // 3.0
			fwdOut:        usd(4000000000000000, -15), // 4.0
			wantIn:        usd(2000000000000000, -15), // 2.0
			wantSrcToDst:  usd(3000000000000000, -15), // 3.0
			wantOut:       usd(4000000000000000, -15), // 4.0
		},
		{
			// fwdIn = 10.0, cacheIn = 1.0: different exponent after
			// normalization → REPLACE regardless of mantissa ratio.
			name:          "replace on differing exponent",
			cacheIn:       usd(1000000000000000, -15), // 1.0
			cacheSrcToDst: usd(1000000000000000, -15), // 1.0
			cacheOut:      usd(1000000000000000, -15), // 1.0
			fwdIn:         usd(1000000000000000, -14), // 10.0
			fwdSrcToDst:   usd(5000000000000000, -15), // 5.0
			fwdOut:        usd(7000000000000000, -15), // 7.0
			wantIn:        usd(1000000000000000, -14), // 10.0
			wantSrcToDst:  usd(5000000000000000, -15), // 5.0
			wantOut:       usd(7000000000000000, -15), // 7.0
		},
		{
			// fwdIn = 1.005, cacheIn = 1.0: same exponent, ratio 1.005 <= 1.01
			// and diff 0.005 > 1e-9 → NO replace, min-clamp applies. Forward
			// srcToDst/out are larger, so the cached minimums are kept; only
			// in is overwritten with fwdIn.
			name:          "min-clamp when ratio within 1.01",
			cacheIn:       usd(1000000000000000, -15), // 1.0
			cacheSrcToDst: usd(1000000000000000, -15), // 1.0
			cacheOut:      usd(1000000000000000, -15), // 1.0
			fwdIn:         usd(1005000000000000, -15), // 1.005
			fwdSrcToDst:   usd(2000000000000000, -15), // 2.0 (larger)
			fwdOut:        usd(2000000000000000, -15), // 2.0 (larger)
			wantIn:        usd(1005000000000000, -15), // 1.005 (in always set)
			wantSrcToDst:  usd(1000000000000000, -15), // 1.0 (min kept)
			wantOut:       usd(1000000000000000, -15), // 1.0 (min kept)
		},
		{
			// fwdIn exceeds cacheIn by only 1e-10 (< 1e-9) → NO replace.
			name:          "min-clamp when diff below 1e-9",
			cacheIn:       usd(1000000000000000, -15), // 1.0
			cacheSrcToDst: usd(1000000000000000, -15), // 1.0
			cacheOut:      usd(1000000000000000, -15), // 1.0
			fwdIn:         usd(1000000000100000, -15), // 1.0000000001
			fwdSrcToDst:   usd(2000000000000000, -15), // 2.0 (larger)
			fwdOut:        usd(2000000000000000, -15), // 2.0 (larger)
			wantIn:        usd(1000000000100000, -15), // in always set
			wantSrcToDst:  usd(1000000000000000, -15), // min kept
			wantOut:       usd(1000000000000000, -15), // min kept
		},
		{
			// fwdIn < cacheIn → outer guard false, plain min-clamp path.
			name:          "min-clamp when forward input is smaller",
			cacheIn:       usd(2000000000000000, -15), // 2.0
			cacheSrcToDst: usd(2000000000000000, -15), // 2.0
			cacheOut:      usd(2000000000000000, -15), // 2.0
			fwdIn:         usd(1000000000000000, -15), // 1.0
			fwdSrcToDst:   usd(1000000000000000, -15), // 1.0 (smaller → kept)
			fwdOut:        usd(3000000000000000, -15), // 3.0 (larger → min keeps cache)
			wantIn:        usd(1000000000000000, -15), // in always set
			wantSrcToDst:  usd(1000000000000000, -15), // min took forward
			wantOut:       usd(2000000000000000, -15), // min kept cache
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &DirectStepI{currency: "USD"}
			s.cache = &directCache{
				in:         tt.cacheIn,
				srcToDst:   tt.cacheSrcToDst,
				out:        tt.cacheOut,
				srcDebtDir: DebtDirectionIssues,
			}

			s.setCacheLimiting(tt.fwdIn, tt.fwdSrcToDst, tt.fwdOut, DebtDirectionRedeems)

			if s.cache.in.Compare(tt.wantIn) != 0 {
				t.Errorf("in: got mant=%d exp=%d, want mant=%d exp=%d",
					s.cache.in.Mantissa(), s.cache.in.Exponent(),
					tt.wantIn.Mantissa(), tt.wantIn.Exponent())
			}
			if s.cache.srcToDst.Compare(tt.wantSrcToDst) != 0 {
				t.Errorf("srcToDst: got mant=%d exp=%d, want mant=%d exp=%d",
					s.cache.srcToDst.Mantissa(), s.cache.srcToDst.Exponent(),
					tt.wantSrcToDst.Mantissa(), tt.wantSrcToDst.Exponent())
			}
			if s.cache.out.Compare(tt.wantOut) != 0 {
				t.Errorf("out: got mant=%d exp=%d, want mant=%d exp=%d",
					s.cache.out.Mantissa(), s.cache.out.Exponent(),
					tt.wantOut.Mantissa(), tt.wantOut.Exponent())
			}
			// srcDebtDir is always set to the forward value.
			if s.cache.srcDebtDir != DebtDirectionRedeems {
				t.Errorf("srcDebtDir: got %v, want %v", s.cache.srcDebtDir, DebtDirectionRedeems)
			}
		})
	}
}
