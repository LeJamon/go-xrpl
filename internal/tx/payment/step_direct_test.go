package payment

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
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

// directQuality builds the expected Quality the same way the implementation
// does: from the integer srcQOut/dstQIn quality values via QualityFromAmounts.
// The argument order encodes the rippled getRate semantics for each branch:
//   - legacy:   getRate(srcQOut, dstQIn) = dstQIn / srcQOut → QualityFromAmounts(dstQIn, srcQOut)
//   - post-fix: getRate(dstQIn, srcQOut) = srcQOut / dstQIn → QualityFromAmounts(srcQOut, dstQIn)
func directQuality(in, out uint32) Quality {
	inAmt := NewIOUEitherAmount(state.NewIssuedAmountFromValue(int64(in), 0, "", ""))
	outAmt := NewIOUEitherAmount(state.NewIssuedAmountFromValue(int64(out), 0, "", ""))
	return QualityFromAmounts(inAmt, outAmt)
}

// putDirectTrustLine writes a trust line between low and high carrying an
// explicit balance (from the low account's perspective) and HighQualityIn.
func (m *paymentMockLedgerView) putDirectTrustLine(low, high [20]byte, currency string, balanceLow float64, highQualityIn uint32) {
	rs := &state.RippleState{
		Balance:       tx.NewIssuedAmountFromFloat64(balanceLow, currency, state.EncodeAccountIDSafe(high)),
		LowLimit:      tx.NewIssuedAmountFromFloat64(1000, currency, state.EncodeAccountIDSafe(low)),
		HighLimit:     tx.NewIssuedAmountFromFloat64(1000, currency, state.EncodeAccountIDSafe(high)),
		HighQualityIn: highQualityIn,
	}
	data, _ := state.SerializeRippleState(rs)
	key := keylet.Line(low, high, currency)
	m.data[key.Key] = data
}

// TestDirectStepI_QualityUpperBound covers the four arms of
// DirectStepI.QualityUpperBound with exact, formula-derived expected qualities:
//
//	(a) offer crossing  → identity quality (qualityOne), ignoring trust-line state.
//	(b) legacy branch   → getRate(srcQOut, dstQIn) = dstQIn/srcQOut, with the
//	                      transfer rate charged when prevStepDir redeems && src issues,
//	                      and dstQIn capped at QualityOne when isLast.
//	(c) post-fix issue  → getRate(dstQIn, srcQOut) = srcQOut/dstQIn via
//	                      qualitiesSrcIssuesDir(prevStepDir).
//	    post-fix redeem → qualitiesSrcRedeems (srcQOut=QualityOne with no prevStep) → 1.0.
//	(d) post-fix issue with the propagated prevStepDir actually toggling srcQOut —
//	    the direct proof that prevStepDir is honoured rather than ignored.
//
// alice < bob, so alice is the LOW account on the alice↔bob trust line; a
// positive balance (low perspective) means alice is owed and the step redeems.
// Reference: rippled DirectStep.cpp qualityUpperBound() lines 839-878.
func TestDirectStepI_QualityUpperBound(t *testing.T) {
	var alice, bob [20]byte
	copy(alice[:], []byte("alice12345678901234"))
	copy(bob[:], []byte("bob1234567890123456"))

	const rate = uint32(1_250_000_000) // 1.25 transfer rate (QualityOne = 1e9)
	const qIn = uint32(2_000_000_000)  // 2.0 trust-line QualityIn

	allSupported := amendment.AllSupportedRules()
	legacyRules := amendment.NewRulesBuilder().
		FromPreset(amendment.PresetAllSupported).
		DisableByName("fixQualityUpperBound").
		Build()

	tests := []struct {
		name          string
		offerCrossing bool
		isLast        bool
		rules         *amendment.Rules
		// trust-line setup: redeem makes alice owed (positive balance, low view);
		// dstQIn is the HighQualityIn on the alice↔bob line (0 → QualityOne).
		redeem  bool
		dstQIn  uint32
		prevDir DebtDirection
		want    Quality
	}{
		{
			// (a) Offer crossing short-circuits to identity, ignoring rate/quality.
			name:          "offer crossing → identity",
			offerCrossing: true,
			rules:         allSupported,
			redeem:        false,
			dstQIn:        qIn,
			prevDir:       DebtDirectionRedeems,
			want:          qualityOne,
		},
		{
			// (b) Legacy, src issues, prevDir redeems → srcQOut = rate, dstQIn = qIn
			// (not last, so not capped). Quality = dstQIn/srcQOut.
			name:    "legacy issue, prev redeems, not last",
			rules:   legacyRules,
			redeem:  false,
			dstQIn:  qIn,
			prevDir: DebtDirectionRedeems,
			want:    directQuality(qIn, rate),
		},
		{
			// (b) Legacy, isLast caps dstQIn at QualityOne (qIn=2.0 > 1.0 → 1.0).
			// srcQOut = rate (prev redeems, src issues). Quality = QualityOne/rate.
			name:    "legacy issue, prev redeems, isLast caps dstQIn",
			rules:   legacyRules,
			isLast:  true,
			redeem:  false,
			dstQIn:  qIn,
			prevDir: DebtDirectionRedeems,
			want:    directQuality(QualityOne, rate),
		},
		{
			// (b) Legacy, prevDir issues → srcQOut stays QualityOne even though src
			// issues. Quality = dstQIn/QualityOne.
			name:    "legacy issue, prev issues, no rate charged",
			rules:   legacyRules,
			redeem:  false,
			dstQIn:  qIn,
			prevDir: DebtDirectionIssues,
			want:    directQuality(qIn, QualityOne),
		},
		{
			// (c) Post-fix, src redeems → qualitiesSrcRedeems. With no prevStep,
			// srcQOut = QualityOne and dstQIn = QualityOne → identity quality.
			name:    "post-fix redeem → qualitiesSrcRedeems identity",
			rules:   allSupported,
			redeem:  true,
			dstQIn:  qIn, // irrelevant on the redeem arm
			prevDir: DebtDirectionIssues,
			want:    directQuality(QualityOne, QualityOne),
		},
		{
			// (c) Post-fix, src issues, prevDir redeems → srcQOut = rate, dstQIn = qIn.
			// Quality = srcQOut/dstQIn (post-fix getRate arg order).
			name:    "post-fix issue, prev redeems → rate/qIn",
			rules:   allSupported,
			redeem:  false,
			dstQIn:  qIn,
			prevDir: DebtDirectionRedeems,
			want:    directQuality(rate, qIn),
		},
		{
			// (d) Post-fix, src issues, prevDir issues → srcQOut = QualityOne.
			// Same setup as the case above EXCEPT prevDir; the result changes from
			// rate/qIn to QualityOne/qIn purely because the propagated prevStepDir
			// is honoured. Pairing these two cases is the direct proof of the fix.
			name:    "post-fix issue, prev issues → QualityOne/qIn (honours prevDir)",
			rules:   allSupported,
			redeem:  false,
			dstQIn:  qIn,
			prevDir: DebtDirectionIssues,
			want:    directQuality(QualityOne, qIn),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view := newPaymentMockLedgerView()
			view.rules = tt.rules
			view.createAccountWithTransferRate(alice, 100_000_000, rate)
			view.createAccount(bob, 100_000_000, 0)
			// Positive balance (low/alice perspective) → alice owed → redeems;
			// negative → alice has issued → issues.
			balance := -10.0
			if tt.redeem {
				balance = 10.0
			}
			view.putDirectTrustLine(alice, bob, "USD", balance, tt.dstQIn)
			sandbox := NewPaymentSandbox(view)

			step := NewDirectStepI(alice, bob, "USD", nil, false, tt.isLast)
			step.offerCrossing = tt.offerCrossing

			q, dir := step.QualityUpperBound(sandbox, tt.prevDir)
			if q == nil {
				t.Fatal("expected non-nil quality")
			}
			if q.Value != tt.want.Value {
				t.Errorf("quality: got %d, want %d", q.Value, tt.want.Value)
			}

			wantDir := DebtDirectionIssues
			if tt.redeem {
				wantDir = DebtDirectionRedeems
			}
			if dir != wantDir {
				t.Errorf("debt direction: got %v, want %v", dir, wantDir)
			}
		})
	}
}
