package consensus

import "testing"

// TestGetNextLedgerTimeResolution_ParityTable mirrors rippled's
// LedgerTiming_test.cpp::testGetNextLedgerTimeResolution exactly,
// iterating the helper across rounds and counting how many rounds
// stepped coarser (rippled's "increase"), finer ("decrease"), or
// stayed the same ("equal"). The rippled test asserts:
//
//   run(previousAgree=false, rounds=10):
//     increase = 3, decrease = 0, equal = 7
//   run(previousAgree=false, rounds=100):  // test repeats false
//     increase = 3, decrease = 0, equal = 97
//
// The `increase`/`decrease` names follow rippled's test source
// comparing by seconds-per-bin: "nextCloseResolution > closeResolution"
// counts as an increase (coarser bin; LARGER seconds).
//
// Reference: rippled src/test/consensus/LedgerTiming_test.cpp:29-75.
func TestGetNextLedgerTimeResolution_ParityTable(t *testing.T) {
	type counts struct{ decrease, equal, increase int }

	run := func(previousAgree bool, rounds uint32) counts {
		var res counts
		closeResolution := LedgerDefaultTimeResolution
		var round uint32
		for round = 1; round <= rounds; round++ {
			next := GetNextLedgerTimeResolution(closeResolution, previousAgree, round)
			switch {
			case next < closeResolution:
				res.decrease++
			case next > closeResolution:
				res.increase++
			default:
				res.equal++
			}
			closeResolution = next
		}
		return res
	}

	// Rippled's first case: run(false, 10). Starting at 30s, each
	// round steps coarser (seq % 1 == 0 always), hitting 60/90/120
	// in rounds 1/2/3, then saturating.
	got := run(false, 10)
	if got.increase != 3 || got.decrease != 0 || got.equal != 7 {
		t.Errorf("run(false, 10): got %+v, want {decrease:0 equal:7 increase:3}", got)
	}

	// Rippled's second case: run(false, 100). Comment says
	// "If we always agree" but the code passes false. We replicate
	// the actual code path.
	got = run(false, 100)
	if got.increase != 3 || got.decrease != 0 || got.equal != 97 {
		t.Errorf("run(false, 100): got %+v, want {decrease:0 equal:97 increase:3}", got)
	}

	// Additional parity: if we always agree, we should be able to
	// step FINER at seq 8 (30 → 20) and seq 16 (20 → 10), then
	// saturate at the finest bin for all remaining rounds.
	got = run(true, 100)
	// From 30s (idx 2): at seq 8 step to 20s (idx 1, decrease). At
	// seq 16 step to 10s (idx 0, decrease). seq 24/32/... no further
	// step possible (at finest). Every other seq: equal.
	if got.decrease != 2 || got.increase != 0 || got.equal != 98 {
		t.Errorf("run(true, 100): got %+v, want {decrease:2 equal:98 increase:0}", got)
	}
}

// TestGetNextLedgerTimeResolution_SingleStep covers specific
// (parentRes, previousAgree, newSeq) tuples that the parity table
// does not directly expose, anchoring the plan's stated expectation
// ("parent at 30s, previousAgree=true, newSeq=8 → 20s") and the
// symmetric disagreement case.
func TestGetNextLedgerTimeResolution_SingleStep(t *testing.T) {
	cases := []struct {
		name          string
		parentRes     uint32
		previousAgree bool
		newSeq        uint32
		want          uint32
	}{
		// --- previousAgree=true: step FINER at seq % 8 == 0 ---
		{"agree,30s,seq8→20s (finer)", 30, true, 8, 20},
		{"agree,30s,seq16→20s (finer)", 30, true, 16, 20},
		{"agree,30s,seq7→30s (no step; seq%8!=0)", 30, true, 7, 30},
		{"agree,30s,seq1→30s (no step; seq%8!=0)", 30, true, 1, 30},
		{"agree,20s,seq8→10s (finer)", 20, true, 8, 10},
		{"agree,10s,seq8→10s (saturated finest)", 10, true, 8, 10},
		{"agree,10s,seq16→10s (saturated finest)", 10, true, 16, 10},

		// --- previousAgree=false: step COARSER at seq % 1 == 0 (always) ---
		{"disagree,30s,seq1→60s (coarser)", 30, false, 1, 60},
		{"disagree,60s,seq2→90s (coarser)", 60, false, 2, 90},
		{"disagree,90s,seq3→120s (coarser)", 90, false, 3, 120},
		{"disagree,120s,seq4→120s (saturated coarsest)", 120, false, 4, 120},

		// --- Edge cases ---
		{"newSeq=0 returns parent unchanged", 30, true, 0, 30},
		{"invalid parentRes passes through", 45, true, 8, 45},
		{"invalid parentRes passes through (disagree)", 45, false, 1, 45},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := GetNextLedgerTimeResolution(tc.parentRes, tc.previousAgree, tc.newSeq)
			if got != tc.want {
				t.Errorf("GetNextLedgerTimeResolution(%d, %v, %d) = %d, want %d",
					tc.parentRes, tc.previousAgree, tc.newSeq, got, tc.want)
			}
		})
	}
}

// TestGetNextLedgerTimeResolution_BinArray pins the exact bin set to
// rippled's (10, 20, 30, 60, 90, 120) so a future edit to the array
// fails noisily.
func TestGetNextLedgerTimeResolution_BinArray(t *testing.T) {
	want := []uint32{10, 20, 30, 60, 90, 120}
	if len(ledgerPossibleTimeResolutions) != len(want) {
		t.Fatalf("bin count mismatch: got %d want %d", len(ledgerPossibleTimeResolutions), len(want))
	}
	for i, v := range want {
		if ledgerPossibleTimeResolutions[i] != v {
			t.Errorf("bin[%d]: got %d want %d", i, ledgerPossibleTimeResolutions[i], v)
		}
	}
	if LedgerDefaultTimeResolution != 30 {
		t.Errorf("LedgerDefaultTimeResolution: got %d want 30", LedgerDefaultTimeResolution)
	}
	if LedgerGenesisTimeResolution != 10 {
		t.Errorf("LedgerGenesisTimeResolution: got %d want 10", LedgerGenesisTimeResolution)
	}
	if increaseLedgerTimeResolutionEvery != 8 {
		t.Errorf("increaseLedgerTimeResolutionEvery: got %d want 8", increaseLedgerTimeResolutionEvery)
	}
	if decreaseLedgerTimeResolutionEvery != 1 {
		t.Errorf("decreaseLedgerTimeResolutionEvery: got %d want 1", decreaseLedgerTimeResolutionEvery)
	}
}
