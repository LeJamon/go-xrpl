package signerlist

import (
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

const walletLocatorHex = "00000000000000000000000000000000000000000000000000000000DEADBEEF"

func entriesOfWeightOne(n int) []SignerEntry {
	entries := make([]SignerEntry, n)
	for i := range entries {
		entries[i] = SignerEntry{SignerEntry: SignerEntryData{
			Account:      fmt.Sprintf("rSigner%d", i),
			SignerWeight: 1,
		}}
	}
	return entries
}

func newSLS(account string, quorum uint32, entries []SignerEntry) *SignerListSet {
	s := NewSignerListSet(account, quorum)
	s.SignerEntries = entries
	return s
}

func TestValidateQuorumAndSignerEntries(t *testing.T) {
	tests := []struct {
		name     string
		account  string
		quorum   uint32
		entries  []SignerEntry
		expanded bool
		want     ter.Result
	}{
		{
			name: "valid two signers", account: "rAlice", quorum: 2, expanded: false,
			entries: []SignerEntry{
				{SignerEntry: SignerEntryData{Account: "rBob", SignerWeight: 1}},
				{SignerEntry: SignerEntryData{Account: "rCarol", SignerWeight: 1}},
			},
			want: ter.TesSUCCESS,
		},
		{name: "no entries", account: "rAlice", quorum: 1, entries: nil, expanded: false, want: ter.TemMALFORMED},
		{name: "8 entries without amendment ok", account: "rAlice", quorum: 1, entries: entriesOfWeightOne(8), expanded: false, want: ter.TesSUCCESS},
		{name: "9 entries without amendment", account: "rAlice", quorum: 1, entries: entriesOfWeightOne(9), expanded: false, want: ter.TemMALFORMED},
		{name: "32 entries with amendment ok", account: "rAlice", quorum: 1, entries: entriesOfWeightOne(32), expanded: true, want: ter.TesSUCCESS},
		{name: "33 entries with amendment", account: "rAlice", quorum: 1, entries: entriesOfWeightOne(33), expanded: true, want: ter.TemMALFORMED},
		{
			name: "duplicate signer", account: "rAlice", quorum: 2, expanded: false,
			entries: []SignerEntry{
				{SignerEntry: SignerEntryData{Account: "rBob", SignerWeight: 1}},
				{SignerEntry: SignerEntryData{Account: "rBob", SignerWeight: 1}},
			},
			want: ter.TemBAD_SIGNER,
		},
		{
			name: "self reference", account: "rAlice", quorum: 2, expanded: false,
			entries: []SignerEntry{
				{SignerEntry: SignerEntryData{Account: "rAlice", SignerWeight: 1}},
				{SignerEntry: SignerEntryData{Account: "rBob", SignerWeight: 1}},
			},
			want: ter.TemBAD_SIGNER,
		},
		{
			name: "zero weight", account: "rAlice", quorum: 2, expanded: false,
			entries: []SignerEntry{
				{SignerEntry: SignerEntryData{Account: "rBob", SignerWeight: 0}},
				{SignerEntry: SignerEntryData{Account: "rCarol", SignerWeight: 2}},
			},
			want: ter.TemBAD_WEIGHT,
		},
		{
			name: "quorum unreachable", account: "rAlice", quorum: 5, expanded: false,
			entries: []SignerEntry{
				{SignerEntry: SignerEntryData{Account: "rBob", SignerWeight: 1}},
				{SignerEntry: SignerEntryData{Account: "rCarol", SignerWeight: 1}},
			},
			want: ter.TemBAD_QUORUM,
		},
		{
			name: "walletlocator without amendment", account: "rAlice", quorum: 1, expanded: false,
			entries: []SignerEntry{
				{SignerEntry: SignerEntryData{Account: "rBob", SignerWeight: 1, WalletLocator: walletLocatorHex}},
			},
			want: ter.TemMALFORMED,
		},
		{
			name: "walletlocator with amendment", account: "rAlice", quorum: 1, expanded: true,
			entries: []SignerEntry{
				{SignerEntry: SignerEntryData{Account: "rBob", SignerWeight: 1, WalletLocator: walletLocatorHex}},
			},
			want: ter.TesSUCCESS,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newSLS(tt.account, tt.quorum, tt.entries).validateQuorumAndSignerEntries(tt.expanded)
			if got != tt.want {
				t.Errorf("validateQuorumAndSignerEntries(%v) = %v, want %v", tt.expanded, got, tt.want)
			}
		})
	}
}

// TestValidateQuorumAndSignerEntries_CheckOrder pins the check order to rippled's:
// the entry-count cap and the duplicate check both precede the per-signer weight
// check, so a list malformed in more than one way reports the same TER rippled
// would. Reference: rippled SetSignerList.cpp:271-303.
func TestValidateQuorumAndSignerEntries_CheckOrder(t *testing.T) {
	// Over the pre-amendment cap of 8 AND carrying a zero weight: the cap check
	// wins, so the result is temMALFORMED rather than temBAD_WEIGHT.
	overCap := entriesOfWeightOne(9)
	overCap[0].SignerEntry.SignerWeight = 0
	if got := newSLS("rAlice", 1, overCap).validateQuorumAndSignerEntries(false); got != ter.TemMALFORMED {
		t.Errorf("cap precedes weight: got %v, want temMALFORMED", got)
	}

	// A duplicate AND a zero weight: the duplicate check wins, so the result is
	// temBAD_SIGNER rather than temBAD_WEIGHT.
	dupZero := []SignerEntry{
		{SignerEntry: SignerEntryData{Account: "rBob", SignerWeight: 0}},
		{SignerEntry: SignerEntryData{Account: "rBob", SignerWeight: 1}},
	}
	if got := newSLS("rAlice", 1, dupZero).validateQuorumAndSignerEntries(false); got != ter.TemBAD_SIGNER {
		t.Errorf("duplicate precedes weight: got %v, want temBAD_SIGNER", got)
	}
}
