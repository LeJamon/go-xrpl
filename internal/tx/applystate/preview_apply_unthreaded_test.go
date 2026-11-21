package applystate

import (
	"bytes"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestPreviewAndApplyUnthreadedMatchesPreviewThenApplyUnthreaded(t *testing.T) {
	const owner = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	ownerID, err := state.DecodeAccountID(owner)
	if err != nil {
		t.Fatal(err)
	}
	accountKey := keylet.Account(ownerID)
	account := func(balance string) []byte {
		fields := applyTestAccountRootFields()
		fields["Balance"] = balance
		fields["PreviousTxnID"] = strings.Repeat("AB", 32)
		fields["PreviousTxnLgrSeq"] = uint32(1)
		return encodeApplyTestEntry(t, fields)
	}
	ticket := func(seq uint32) []byte {
		return encodeApplyTestEntry(t, map[string]any{
			"LedgerEntryType":   "Ticket",
			"Account":           owner,
			"Flags":             uint32(0),
			"OwnerNode":         "0",
			"TicketSequence":    seq,
			"PreviousTxnID":     strings.Repeat("CD", 32),
			"PreviousTxnLgrSeq": uint32(2),
		})
	}

	type outcome struct {
		metadata *tx.Metadata
		err      error
		base     *recordingBaseView
	}
	type scenario struct {
		name          string
		updateAccount bool
		insertInvalid bool
	}
	run := func(sc scenario, apply func(*ApplyStateTable) (*tx.Metadata, error)) outcome {
		base := newRecordingBaseView()
		base.data[accountKey.Key] = account("1000000")
		base.data[key(9)] = ticket(9)
		table := NewApplyStateTable(base, [32]byte{7}, 3, amendment.AllSupportedRules())
		if sc.updateAccount {
			if err := table.Update(accountKey, account("999990")); err != nil {
				t.Fatal(err)
			}
		}
		if err := table.Erase(kl(9)); err != nil {
			t.Fatal(err)
		}
		if err := table.Insert(kl(10), ticket(10)); err != nil {
			t.Fatal(err)
		}
		if sc.insertInvalid {
			invalid := applyTestAccountRootFields()
			invalid["DestinationTag"] = uint32(1)
			if err := table.Insert(kl(11), encodeApplyTestEntry(t, invalid)); err != nil {
				t.Fatal(err)
			}
		}
		metadata, err := apply(table)
		return outcome{metadata: metadata, err: err, base: base}
	}
	previewThenApply := func(table *ApplyStateTable) (*tx.Metadata, error) {
		metadata, err := table.Preview()
		if err != nil {
			return nil, err
		}
		return metadata, table.ApplyUnthreaded()
	}

	for _, sc := range []scenario{
		{name: "thread-only owner"},
		{name: "modified owner", updateAccount: true},
		{name: "metadata build error", insertInvalid: true},
	} {
		t.Run(sc.name, func(t *testing.T) {
			want := run(sc, previewThenApply)
			got := run(sc, (*ApplyStateTable).PreviewAndApplyUnthreaded)
			if (got.err == nil) != (want.err == nil) || (got.err != nil && got.err.Error() != want.err.Error()) {
				t.Fatalf("error %v, want %v", got.err, want.err)
			}
			if !sc.insertInvalid && len(want.metadata.AffectedNodes) != 3 {
				t.Fatalf("expected 3 affected nodes, got %d", len(want.metadata.AffectedNodes))
			}
			if !reflect.DeepEqual(got.metadata, want.metadata) {
				t.Fatalf("metadata differs:\n got %+v\nwant %+v", got.metadata, want.metadata)
			}
			if !maps.EqualFunc(got.base.data, want.base.data, bytes.Equal) {
				t.Fatal("committed state differs")
			}
			if !slices.Equal(got.base.mutations, want.base.mutations) {
				t.Fatalf("mutations %v, want %v", got.base.mutations, want.base.mutations)
			}
		})
	}
}
