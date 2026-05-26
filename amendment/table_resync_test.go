// Copyright (c) 2024-2025. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package amendment

import "testing"

// unknownAmendment is a hash not present in the registry.
var unknownAmendment = [32]byte{0xDE, 0xAD, 0xBE, 0xEF}

func TestEnable_TracksUnsupported(t *testing.T) {
	tbl := NewAmendmentTable()

	tbl.Enable(FeatureDID) // supported
	if tbl.unsupportedEnabled {
		t.Fatal("enabling a supported amendment must not set unsupportedEnabled")
	}
	if tbl.IsBlocked() {
		t.Fatal("supported amendment must not block")
	}

	tbl.Enable(FeatureXChainBridge) // SupportedNo
	if !tbl.unsupportedEnabled {
		t.Fatal("enabling an unsupported amendment must set unsupportedEnabled")
	}

	tbl.Enable(unknownAmendment)
	if !tbl.unsupportedEnabled {
		t.Fatal("enabling an unknown amendment must set unsupportedEnabled")
	}
}

func TestDoValidatedLedger_EnablesAndBlocks(t *testing.T) {
	tbl := NewAmendmentTable()

	enabled := map[[32]byte]bool{
		FeatureDID:          true,
		FeatureXChainBridge: true, // unsupported
	}
	tbl.DoValidatedLedger(256, enabled, nil)

	if !tbl.IsEnabled(FeatureDID) || !tbl.IsEnabled(FeatureXChainBridge) {
		t.Fatal("DoValidatedLedger must enable all amendments in the set")
	}
	if !tbl.HasUnsupportedEnabled() {
		t.Fatal("expected HasUnsupportedEnabled after enabling XChainBridge")
	}
	if !tbl.IsBlocked() {
		t.Fatal("node must be blocked once an unsupported amendment activates")
	}
	if _, ok := tbl.FirstUnsupportedExpected(); ok {
		t.Fatal("no majorities supplied; firstUnsupportedExpected must be unset")
	}
}

func TestDoValidatedLedger_FirstUnsupportedExpected(t *testing.T) {
	tbl := NewAmendmentTable()

	const unsupportedMajorityTime uint32 = 1_000_000
	majorities := map[[32]byte]uint32{
		FeatureXChainBridge: unsupportedMajorityTime,       // unsupported, not enabled
		FeatureDID:          unsupportedMajorityTime - 100, // supported → ignored
	}
	tbl.DoValidatedLedger(256, nil, majorities)

	exp, ok := tbl.FirstUnsupportedExpected()
	if !ok {
		t.Fatal("expected firstUnsupportedExpected to be set for an unsupported majority")
	}
	if want := unsupportedMajorityTime + majorityTimeSeconds; exp != want {
		t.Fatalf("firstUnsupportedExpected = %d, want %d (majority time + 14d)", exp, want)
	}
	if tbl.IsBlocked() {
		t.Fatal("an unsupported amendment only in majority (not enabled) must not block")
	}

	// Once the unsupported amendment is enabled and no longer in majority, the
	// projection clears.
	tbl.DoValidatedLedger(512, map[[32]byte]bool{FeatureXChainBridge: true}, nil)
	if _, ok := tbl.FirstUnsupportedExpected(); ok {
		t.Fatal("firstUnsupportedExpected must clear once majority no longer holds")
	}
	if !tbl.IsBlocked() {
		t.Fatal("node must be blocked after the unsupported amendment activates")
	}
}

func TestLastVote_RoundTripAndDefensiveCopy(t *testing.T) {
	tbl := NewAmendmentTable()
	if tbl.LastVote() != nil {
		t.Fatal("fresh table must have no last vote")
	}

	src := &LastVote{
		TrustedValidations: 5,
		Threshold:          4,
		Votes:              map[[32]byte]int{FeatureDID: 3},
	}
	tbl.SetLastVote(src)

	// Mutating the source after SetLastVote must not affect the stored copy.
	src.Votes[FeatureDID] = 99
	src.TrustedValidations = 0

	got := tbl.LastVote()
	if got == nil || got.TrustedValidations != 5 || got.Threshold != 4 || got.Votes[FeatureDID] != 3 {
		t.Fatalf("stored last vote not isolated from source: %+v", got)
	}

	// Mutating the returned copy must not affect the stored value.
	got.Votes[FeatureDID] = 1
	if again := tbl.LastVote(); again.Votes[FeatureDID] != 3 {
		t.Fatal("LastVote must return a defensive copy")
	}
}

func TestNeedValidatedLedger_Windowing(t *testing.T) {
	tbl := NewAmendmentTable()

	// Fresh table (lastUpdateSeq 0) always needs the first sync.
	if !tbl.NeedValidatedLedger(100) {
		t.Fatal("fresh table must need a validated-ledger sync")
	}

	tbl.DoValidatedLedger(100, nil, nil)

	if tbl.NeedValidatedLedger(200) {
		t.Fatal("seq 200 is in the same 256-window as 100; no sync needed")
	}
	if !tbl.NeedValidatedLedger(300) {
		t.Fatal("seq 300 crosses into a new 256-window; sync needed")
	}
}
