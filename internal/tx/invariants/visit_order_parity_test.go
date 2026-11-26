package invariants

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

func TestAccountRootsDeletedClean_PseudoAccountLinkedObject(t *testing.T) {
	linked := [32]byte{0x42}
	tests := []struct {
		name string
		set  func(*state.AccountRoot)
	}{
		{name: "AMMID", set: func(a *state.AccountRoot) { a.AMMID = linked }},
		{name: "VaultID", set: func(a *state.AccountRoot) { a.VaultID = linked }},
		{name: "LoanBrokerID", set: func(a *state.AccountRoot) { a.LoanBrokerID = linked }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acct := &state.AccountRoot{Account: "rrrrrrrrrrrrrrrrrrrrrhoLvTp", Flags: LsfDisableMaster | LsfDefaultRipple | LsfDepositAuth}
			tt.set(acct)
			data := mustSerializeAccount(t, acct)
			entries := []InvariantEntry{{EntryType: entry.TypeAccountRoot, IsDelete: true, Before: data, DeleteFinal: data}}
			rules := rulesWithSingleAssetVault()

			left := bookMapView{entries: map[[32]byte][]byte{linked: {0}}}
			if v := checkAccountRootsDeletedClean(entries, left, rules); v == nil {
				t.Fatal("expected violation when the linked object survives the pseudo-account")
			}
			gone := bookMapView{entries: map[[32]byte][]byte{}}
			if v := checkAccountRootsDeletedClean(entries, gone, rules); v != nil {
				t.Fatalf("unexpected violation after the linked object was deleted: %v", v)
			}
		})
	}
}

func TestCheckValidBookDirectory_MPTRootExchangeRate(t *testing.T) {
	on := amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_2_0})
	mpt := [24]byte{1}
	for _, side := range []string{"TakerPaysMPT", "TakerGetsMPT"} {
		t.Run(side, func(t *testing.T) {
			var key [32]byte
			key[0] = 0xB0
			binary.BigEndian.PutUint64(key[24:], 0xABCD)
			dir := &state.DirectoryNode{RootIndex: key}
			if side == "TakerPaysMPT" {
				dir.TakerPaysMPT = &mpt
			} else {
				dir.TakerGetsMPT = &mpt
			}
			data, err := state.SerializeDirectoryNode(dir, true)
			if err != nil {
				t.Fatalf("SerializeDirectoryNode: %v", err)
			}
			entries := []InvariantEntry{{Key: key, EntryType: entry.TypeDirectoryNode, After: data}}
			if v := checkValidBookDirectory(entries, stubView{}, on); v == nil {
				t.Fatal("expected violation for an MPT book root without a matching exchange rate")
			}
		})
	}
}

// Before fixCleanup3_1_3 these checks assigned their result per entry, so a
// violating entry followed (in key order) by a clean one passed.
func TestLastEntryWinsBeforeFixCleanup3_1_3(t *testing.T) {
	pre := amendment.EmptyRules()
	post := amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_1_3})

	goodLine := encodeTrustLine(t)
	idx := bytes.Index(goodLine, []byte("ABC"))
	if idx < 0 {
		t.Fatal("ABC currency marker not found in encoded RippleState")
	}
	xrpLine := bytes.Clone(goodLine)
	copy(xrpLine[idx:idx+3], []byte{0, 0, 0})

	deepFrozen := func(flags uint32) []byte {
		data, err := state.SerializeRippleState(&state.RippleState{
			Balance:   state.NewIssuedAmountFromValue(0, 0, "USD", addrHolderA),
			LowLimit:  state.NewIssuedAmountFromValue(0, 0, "USD", addrHolderA),
			HighLimit: state.NewIssuedAmountFromValue(0, 0, "USD", addrIssuer),
			Flags:     flags,
		})
		if err != nil {
			t.Fatalf("SerializeRippleState: %v", err)
		}
		return data
	}
	issuance := func(outstanding, locked uint64) []byte {
		data, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{OutstandingAmount: outstanding, LockedAmount: &locked})
		if err != nil {
			t.Fatalf("SerializeMPTokenIssuance: %v", err)
		}
		return data
	}

	line := func(after []byte) InvariantEntry {
		return InvariantEntry{EntryType: entry.TypeRippleState, After: after}
	}
	mpt := func(after []byte) InvariantEntry {
		return InvariantEntry{EntryType: entry.TypeMPTokenIssuance, After: after}
	}

	tests := []struct {
		name  string
		check func([]InvariantEntry, *amendment.Rules) *InvariantViolation
		bad   InvariantEntry
		good  InvariantEntry
	}{
		{name: "NoXRPTrustLines", check: checkNoXRPTrustLines, bad: line(xrpLine), good: line(goodLine)},
		{
			name:  "NoDeepFreezeTrustLinesWithoutFreeze",
			check: checkNoDeepFreezeTrustLinesWithoutFreeze,
			bad:   line(deepFrozen(state.LsfLowDeepFreeze)),
			good:  line(deepFrozen(0)),
		},
		{name: "NoZeroEscrow", check: checkNoZeroEscrow, bad: mpt(issuance(1, 2)), good: mpt(issuance(2, 1))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if v := tt.check([]InvariantEntry{tt.bad, tt.good}, pre); v != nil {
				t.Fatalf("pre-fix: a later clean entry must overwrite the violation, got %v", v)
			}
			if v := tt.check([]InvariantEntry{tt.good, tt.bad}, pre); v == nil {
				t.Fatal("pre-fix: a violating last entry must fail")
			}
			if v := tt.check([]InvariantEntry{tt.bad, tt.good}, post); v == nil {
				t.Fatal("post-fix: any violating entry must fail")
			}
		})
	}
}

func TestNoZeroEscrow_PreFixLockedCheckReplacesBoundViolation(t *testing.T) {
	locked := uint64(1)
	data, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{OutstandingAmount: MaxMPTokenAmount + 1, LockedAmount: &locked})
	if err != nil {
		t.Fatalf("SerializeMPTokenIssuance: %v", err)
	}
	entries := []InvariantEntry{{EntryType: entry.TypeMPTokenIssuance, After: data}}
	if v := checkNoZeroEscrow(entries, amendment.EmptyRules()); v != nil {
		t.Fatalf("pre-fix: the locked-amount assignment must replace the bound violation, got %v", v)
	}
	if v := checkNoZeroEscrow(entries, amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_1_3})); v == nil {
		t.Fatal("post-fix: an out-of-range OutstandingAmount must fail")
	}
}

func TestNoDeepFreeze_ChecksDeletedLineFinalImage(t *testing.T) {
	final, err := state.SerializeRippleState(&state.RippleState{
		Balance:   state.NewIssuedAmountFromValue(0, 0, "USD", addrHolderA),
		LowLimit:  state.NewIssuedAmountFromValue(0, 0, "USD", addrHolderA),
		HighLimit: state.NewIssuedAmountFromValue(0, 0, "USD", addrIssuer),
		Flags:     state.LsfHighDeepFreeze,
	})
	if err != nil {
		t.Fatalf("SerializeRippleState: %v", err)
	}
	entries := []InvariantEntry{{EntryType: entry.TypeRippleState, IsDelete: true, Before: final, DeleteFinal: final}}
	if v := checkNoDeepFreezeTrustLinesWithoutFreeze(entries, nil); v == nil {
		t.Fatal("expected violation for a deleted line deep-frozen without freeze")
	}
}
