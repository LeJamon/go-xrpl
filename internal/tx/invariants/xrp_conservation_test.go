package invariants

import (
	"encoding/json"
	"math/big"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

func xrpConservationImage(t *testing.T, kind entry.Type, amount, claimed uint64) []byte {
	t.Helper()
	fields := map[string]any{
		"LedgerEntryType":   kind.String(),
		"Flags":             uint32(0),
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
	}
	const owner = "rrrrrrrrrrrrrrrrrrrrrhoLvTp"
	const destination = "rrrrrrrrrrrrrrrrrrrrBZbvji"
	switch kind {
	case entry.TypeAccountRoot:
		fields["Account"] = owner
		fields["Balance"] = strconv.FormatUint(amount, 10)
		fields["Sequence"] = uint32(1)
		fields["OwnerCount"] = uint32(0)
	case entry.TypeEscrow:
		fields["Account"] = owner
		fields["Destination"] = destination
		fields["Amount"] = strconv.FormatUint(amount, 10)
		fields["OwnerNode"] = "0"
	case entry.TypePayChannel:
		fields["Account"] = owner
		fields["Destination"] = destination
		fields["Amount"] = strconv.FormatUint(amount, 10)
		fields["Balance"] = strconv.FormatUint(claimed, 10)
		fields["OwnerNode"] = "0"
		fields["SettleDelay"] = uint32(1)
		fields["PublicKey"] = "ED" + strings.Repeat("01", 32)
	case entry.TypeSponsorship:
		fields["Owner"] = owner
		fields["Sponsee"] = destination
		fields["OwnerNode"] = "0"
		fields["SponseeNode"] = "0"
		fields["FeeAmount"] = strconv.FormatUint(amount, 10)
	default:
		t.Fatalf("unsupported XRP-bearing type %v", kind)
	}
	return mustEncode(t, fields)
}

func TestXRPNotCreatedBoundaries(t *testing.T) {
	// These are synthetic invariant images, not transactions reachable from a
	// valid parent ledger: their aggregate holdings can exceed the XRP supply.
	data, err := os.ReadFile("testdata/xrp_conservation.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Positive, Negative, Fee, Net string
		Accepted                           bool
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []entry.Type{entry.TypeAccountRoot, entry.TypeEscrow, entry.TypePayChannel, entry.TypeSponsorship} {
		t.Run(kind.String(), func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.Name, func(t *testing.T) {
					positive, ok := new(big.Int).SetString(tc.Positive, 10)
					if !ok {
						t.Fatal("invalid positive total")
					}
					negative, ok := new(big.Int).SetString(tc.Negative, 10)
					if !ok {
						t.Fatal("invalid negative total")
					}
					net := new(big.Int).Sub(positive, negative)
					if net.String() != tc.Net {
						t.Fatalf("mathematical total %s, want %s", net, tc.Net)
					}
					fee, err := strconv.ParseUint(tc.Fee, 10, 64)
					if err != nil {
						t.Fatal(err)
					}
					accepted := net.Sign() <= 0 && new(big.Int).Neg(net).Cmp(new(big.Int).SetUint64(fee)) == 0
					if accepted != tc.Accepted {
						t.Fatal("fixture predicate disagrees with mathematical total")
					}
					var entries []InvariantEntry
					for _, total := range []struct {
						amount   *big.Int
						subtract bool
					}{{positive, false}, {negative, true}} {
						remaining := new(big.Int).Set(total.amount)
						limit := new(big.Int).SetUint64(InitialXRP)
						for remaining.Sign() > 0 {
							part := new(big.Int).Set(remaining)
							if part.Cmp(limit) > 0 {
								part.Set(limit)
							}
							image := xrpConservationImage(t, kind, part.Uint64(), 0)
							change := InvariantEntry{EntryType: kind, After: image}
							if total.subtract {
								change.Before, change.After, change.IsDelete = image, nil, true
								if kind == entry.TypeAccountRoot {
									change.DeleteFinal = xrpConservationImage(t, kind, 0, 0)
								} else {
									change.DeleteFinal = image
								}
							}
							entries = append(entries, change)
							remaining.Sub(remaining, part)
						}
					}
					for _, order := range []string{"positive_first", "negative_first"} {
						t.Run(order, func(t *testing.T) {
							violation := checkXRPNotCreated(TesSUCCESS, fee, entries)
							if (violation == nil) != tc.Accepted {
								t.Fatalf("net %s fee %d: %v (accepted=%t)", tc.Net, fee, violation, tc.Accepted)
							}
							if violation != nil && (violation.Name != "XRPNotCreated" || !strings.Contains(violation.Message, tc.Net+" drops")) {
								t.Fatalf("inexact violation: %v; want total %s", violation, tc.Net)
							}
						})
						slices.Reverse(entries)
					}
				})
			}
		})
	}
}

func TestXRPNotCreatedEntryLifecycle(t *testing.T) {
	for _, kind := range []entry.Type{entry.TypeAccountRoot, entry.TypeEscrow, entry.TypePayChannel, entry.TypeSponsorship} {
		t.Run(kind.String(), func(t *testing.T) {
			before := xrpConservationImage(t, kind, 100, 0)
			after := xrpConservationImage(t, kind, 90, 0)
			zero := xrpConservationImage(t, kind, 0, 0)
			deleted := before
			if kind == entry.TypeAccountRoot {
				deleted = zero
			}
			for _, tc := range []struct {
				name   string
				change InvariantEntry
				fee    uint64
				valid  bool
			}{
				{"create_zero", InvariantEntry{After: zero}, 0, true},
				{"create_xrp", InvariantEntry{After: before}, 0, false},
				{"unchanged", InvariantEntry{Before: before, After: before}, 0, true},
				{"fee", InvariantEntry{Before: before, After: after}, 10, true},
				{"increase", InvariantEntry{Before: after, After: before}, 0, false},
				{"delete", InvariantEntry{Before: before, DeleteFinal: deleted, IsDelete: true}, 100, true},
				{"delete_excess_burn", InvariantEntry{Before: before, DeleteFinal: deleted, IsDelete: true}, 10, false},
				{"bad_before", InvariantEntry{Before: []byte{0xff}, After: after}, 0, false},
				{"bad_after", InvariantEntry{Before: before, After: []byte{0xff}}, 0, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					tc.change.EntryType = kind
					for _, result := range []Result{TesSUCCESS, Result(ter.TecINVARIANT_FAILED)} {
						violation := checkXRPNotCreated(result, tc.fee, []InvariantEntry{tc.change})
						if (violation == nil) != tc.valid {
							t.Fatalf("result %v: %v (valid=%t)", result, violation, tc.valid)
						}
					}
				})
			}
		})
	}
}

func TestXRPNotCreatedPayChannelDifference(t *testing.T) {
	// A synthetic overclaimed channel has a negative holding. Preserve that
	// signed difference until finalization, including cancellation with an account.
	channel := xrpConservationImage(t, entry.TypePayChannel, 10, 30)
	account := xrpConservationImage(t, entry.TypeAccountRoot, 10, 0)
	for _, tc := range []struct {
		name    string
		changes []InvariantEntry
		fee     uint64
	}{
		{"negative_holding", []InvariantEntry{{EntryType: entry.TypePayChannel, After: channel}}, 20},
		{"cancel_with_account", []InvariantEntry{{EntryType: entry.TypePayChannel, After: channel}, {EntryType: entry.TypeAccountRoot, After: account}}, 10},
		{"remove_negative_holding", []InvariantEntry{{EntryType: entry.TypePayChannel, Before: channel, IsDelete: true}, {EntryType: entry.TypeAccountRoot, Before: xrpConservationImage(t, entry.TypeAccountRoot, 30, 0), IsDelete: true}}, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if violation := checkXRPNotCreated(TesSUCCESS, tc.fee, tc.changes); violation != nil {
				t.Fatal(violation)
			}
		})
	}
}

func TestXRPNotCreatedNonXRPAndUnfundedEntries(t *testing.T) {
	const owner = "rrrrrrrrrrrrrrrrrrrrrhoLvTp"
	const destination = "rrrrrrrrrrrrrrrrrrrrBZbvji"
	for name, amount := range map[string]any{
		"iou": map[string]any{"currency": "USD", "issuer": owner, "value": "123"},
		"mpt": map[string]any{"mpt_issuance_id": strings.Repeat("01", 24), "value": "123"},
	} {
		t.Run(name, func(t *testing.T) {
			image := mustEncode(t, map[string]any{
				"LedgerEntryType":   "Escrow",
				"Flags":             uint32(0),
				"PreviousTxnID":     strings.Repeat("0", 64),
				"PreviousTxnLgrSeq": uint32(0),
				"Account":           owner,
				"Destination":       destination,
				"Amount":            amount,
				"OwnerNode":         "0",
			})
			for _, change := range []InvariantEntry{
				{EntryType: entry.TypeEscrow, After: image},
				{EntryType: entry.TypeEscrow, Before: image, After: image},
				{EntryType: entry.TypeEscrow, Before: image, DeleteFinal: image, IsDelete: true},
			} {
				if violation := checkXRPNotCreated(TesSUCCESS, 0, []InvariantEntry{change}); violation != nil {
					t.Fatal(violation)
				}
			}
		})
	}
	sponsorship := mustEncode(t, map[string]any{
		"Flags": uint32(0), "LedgerEntryType": "Sponsorship", "Owner": owner, "Sponsee": destination,
		"OwnerNode": "0", "SponseeNode": "0", "MaxFee": "1000",
		"PreviousTxnID": strings.Repeat("0", 64), "PreviousTxnLgrSeq": uint32(0),
	})
	for _, change := range []InvariantEntry{
		{EntryType: entry.TypeSponsorship, After: sponsorship},
		{EntryType: entry.TypeSponsorship, Before: sponsorship, After: sponsorship},
		{EntryType: entry.TypeSponsorship, Before: sponsorship, DeleteFinal: sponsorship, IsDelete: true},
		{EntryType: entry.TypeSponsorship, Before: xrpConservationImage(t, entry.TypeSponsorship, 0, 0), After: sponsorship},
		{EntryType: entry.TypeOffer, Before: []byte{0xff}, After: []byte{0xff}},
	} {
		if violation := checkXRPNotCreated(TesSUCCESS, 0, []InvariantEntry{change}); violation != nil {
			t.Fatal(violation)
		}
	}
}

func TestXRPNotCreatedMixedTransfer(t *testing.T) {
	entries := []InvariantEntry{
		{EntryType: entry.TypeAccountRoot, Before: xrpConservationImage(t, entry.TypeAccountRoot, 1000, 0), After: xrpConservationImage(t, entry.TypeAccountRoot, 690, 0)},
		{EntryType: entry.TypeEscrow, After: xrpConservationImage(t, entry.TypeEscrow, 100, 0)},
		{EntryType: entry.TypePayChannel, After: xrpConservationImage(t, entry.TypePayChannel, 150, 50)},
		{EntryType: entry.TypeSponsorship, After: xrpConservationImage(t, entry.TypeSponsorship, 100, 0)},
	}
	if violation := checkXRPNotCreated(TesSUCCESS, 10, entries); violation != nil {
		t.Fatal(violation)
	}
}

func TestXRPNotCreatedDeletedAccountFinalBalance(t *testing.T) {
	for _, tc := range []struct {
		name  string
		final []byte
		valid bool
	}{
		{"retained_balance", xrpConservationImage(t, entry.TypeAccountRoot, 50, 0), true},
		{"extra_burn", xrpConservationImage(t, entry.TypeAccountRoot, 0, 0), false},
		{"malformed_final", []byte{0xff}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changes := []InvariantEntry{
				{EntryType: entry.TypeAccountRoot, Before: xrpConservationImage(t, entry.TypeAccountRoot, 100, 0), DeleteFinal: tc.final, IsDelete: true},
				{EntryType: entry.TypeAccountRoot, Before: xrpConservationImage(t, entry.TypeAccountRoot, 0, 0), After: xrpConservationImage(t, entry.TypeAccountRoot, 50, 0)},
			}
			violation := checkXRPNotCreated(TesSUCCESS, 0, changes)
			if (violation == nil) != tc.valid {
				t.Fatalf("%v (valid=%t)", violation, tc.valid)
			}
		})
	}
}
