package invariants

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

const sponsorshipTestAccount = "rrrrrrrrrrrrrrrrrrrrrhoLvTp"

func sponsorshipTestEncode(t *testing.T, object map[string]any) []byte {
	t.Helper()
	hexData, err := binarycodec.Encode(object)
	if err != nil {
		t.Fatalf("binarycodec.Encode: %v", err)
	}
	data, err := hex.DecodeString(hexData)
	if err != nil {
		t.Fatalf("hex.DecodeString: %v", err)
	}
	return data
}

func sponsorshipTestAccountRoot(t *testing.T, sponsored, sponsoring, owner, sponsoringAccounts uint32) []byte {
	t.Helper()
	return mustSerializeAccount(t, &state.AccountRoot{
		Account:                sponsorshipTestAccount,
		Sequence:               1,
		OwnerCount:             owner,
		SponsoredOwnerCount:    sponsored,
		SponsoringOwnerCount:   sponsoring,
		SponsoringAccountCount: sponsoringAccounts,
	})
}

func sponsorshipTestAddSponsor(t *testing.T, typ entry.Type, data []byte) []byte {
	t.Helper()
	decoded := entry.New(typ)
	if decoded == nil {
		t.Fatalf("no decoder for %s", typ)
	}
	if err := decoded.Decode(data); err != nil {
		t.Fatalf("decode %s: %v", typ, err)
	}
	setter, ok := decoded.(interface{ SetSponsor(string) })
	if !ok {
		t.Fatalf("%s has no Sponsor setter", typ)
	}
	setter.SetSponsor(sponsorshipTestAccount)
	encoder, ok := decoded.(interface{ Encode() ([]byte, error) })
	if !ok {
		t.Fatalf("%s has no encoder", typ)
	}
	encoded, err := encoder.Encode()
	if err != nil {
		t.Fatalf("encode %s: %v", typ, err)
	}
	return encoded
}

func sponsorshipTestRippleState(t *testing.T, high, low bool) []byte {
	t.Helper()
	lowAccount, err := state.EncodeAccountID([20]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	highAccount, err := state.EncodeAccountID([20]byte{2})
	if err != nil {
		t.Fatal(err)
	}
	balance, err := state.NewIssuedAmountFromDecimalString("1", "USD", state.AccountOneAddress)
	if err != nil {
		t.Fatal(err)
	}
	lowLimit, err := state.NewIssuedAmountFromDecimalString("100", "USD", lowAccount)
	if err != nil {
		t.Fatal(err)
	}
	highLimit, err := state.NewIssuedAmountFromDecimalString("100", "USD", highAccount)
	if err != nil {
		t.Fatal(err)
	}
	line := &state.RippleState{Balance: balance, LowLimit: lowLimit, HighLimit: highLimit}
	if high {
		line.HighSponsor = sponsorshipTestAccount
	}
	if low {
		line.LowSponsor = sponsorshipTestAccount
	}
	data, err := state.SerializeRippleState(line)
	if err != nil {
		t.Fatalf("SerializeRippleState: %v", err)
	}
	return data
}

func sponsorshipTestOracle(t *testing.T, seriesCount int) []byte {
	t.Helper()
	series := make([]state.OraclePriceData, seriesCount)
	for i := range series {
		series[i] = state.OraclePriceData{
			BaseAsset: "XRP", QuoteAsset: "USD", AssetPrice: 1,
			HasPrice: true, Scale: 1, HasScale: true,
		}
	}
	data, err := state.SerializeOracle(&state.OracleData{
		Owner: [20]byte{1}, Provider: "AB", AssetClass: "CD", LastUpdateTime: 1,
		PriceDataSeries: series,
	})
	if err != nil {
		t.Fatalf("SerializeOracle: %v", err)
	}
	return sponsorshipTestAddSponsor(t, entry.TypeOracle, data)
}

func sponsorshipTestVault(t *testing.T) []byte {
	t.Helper()
	owner, err := state.EncodeAccountID([20]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	account, err := state.EncodeAccountID([20]byte{2})
	if err != nil {
		t.Fatal(err)
	}
	return sponsorshipTestAddSponsor(t, entry.TypeVault, sponsorshipTestEncode(t, map[string]any{
		"LedgerEntryType":   "Vault",
		"Flags":             uint32(0),
		"Sequence":          uint32(1),
		"OwnerNode":         "0",
		"Owner":             owner,
		"Account":           account,
		"Asset":             map[string]any{"currency": "XRP"},
		"ShareMPTID":        strings.Repeat("0", 48),
		"WithdrawalPolicy":  uint32(0),
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
	}))
}

func sponsorshipTestSignerList(t *testing.T, flags uint32, signerCount int) []byte {
	t.Helper()
	owner, err := state.EncodeAccountID([20]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	signers := make([]state.SignerEntry, signerCount)
	for i := range signers {
		signers[i] = state.SignerEntry{Account: owner, SignerWeight: 1}
	}
	data, err := state.SerializeSignerList(1, signers, flags, false, 0, nil)
	if err != nil {
		t.Fatalf("SerializeSignerList: %v", err)
	}
	return sponsorshipTestAddSponsor(t, entry.TypeSignerList, data)
}

func sponsorshipTestLegacySignerList(t *testing.T, signerCount int) []byte {
	t.Helper()
	owner, err := state.EncodeAccountID([20]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	entries := make([]map[string]any, signerCount)
	for i := range entries {
		entries[i] = map[string]any{
			"SignerEntry": map[string]any{"Account": owner, "SignerWeight": uint16(1)},
		}
	}
	return sponsorshipTestEncode(t, map[string]any{
		"LedgerEntryType":   "SignerList",
		"Account":           owner,
		"Flags":             uint32(0),
		"SignerQuorum":      uint32(1),
		"OwnerNode":         "0",
		"SignerEntries":     entries,
		"Sponsor":           sponsorshipTestAccount,
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
	})
}

func sponsorshipTestEntries(t *testing.T, objectType entry.Type, object []byte, magnitude, ownerCount uint32) []InvariantEntry {
	t.Helper()
	return []InvariantEntry{
		{EntryType: entry.TypeAccountRoot, After: sponsorshipTestAccountRoot(t, magnitude, 0, ownerCount, 0)},
		{EntryType: entry.TypeAccountRoot, After: sponsorshipTestAccountRoot(t, 0, magnitude, 0, 0)},
		{EntryType: objectType, After: object},
	}
}

func TestSponsorshipOwnerCountsBoundaries(t *testing.T) {
	object := sponsorshipTestRippleState(t, true, false)
	valid := sponsorshipTestEntries(t, entry.TypeRippleState, object, 1, 1)
	if violation := checkSponsorshipOwnerCounts(valid); violation != nil {
		t.Fatalf("owner count at sponsored boundary: %v", violation)
	}
	invalid := sponsorshipTestEntries(t, entry.TypeRippleState, object, 1, 0)
	if violation := checkSponsorshipOwnerCounts(invalid); violation == nil || !strings.Contains(violation.Message, "OwnerCount") {
		t.Fatalf("owner count below sponsored count: %v", violation)
	}
}

func TestSponsorshipObjectOwnerCountMagnitudes(t *testing.T) {
	tests := []struct {
		name      string
		typ       entry.Type
		object    func(*testing.T) []byte
		magnitude uint32
	}{
		{name: "RippleState high and low", typ: entry.TypeRippleState, object: func(t *testing.T) []byte {
			return sponsorshipTestRippleState(t, true, true)
		}, magnitude: 2},
		{name: "Oracle five series", typ: entry.TypeOracle, object: func(t *testing.T) []byte {
			return sponsorshipTestOracle(t, 5)
		}, magnitude: 1},
		{name: "Oracle six series", typ: entry.TypeOracle, object: func(t *testing.T) []byte {
			return sponsorshipTestOracle(t, 6)
		}, magnitude: 2},
		{name: "Vault", typ: entry.TypeVault, object: sponsorshipTestVault, magnitude: 2},
		{name: "SignerList one owner", typ: entry.TypeSignerList, object: func(t *testing.T) []byte {
			return sponsorshipTestSignerList(t, entry.LsfOneOwnerCount, 2)
		}, magnitude: 1},
		{name: "SignerList modern reserve", typ: entry.TypeSignerList, object: func(t *testing.T) []byte {
			return sponsorshipTestSignerList(t, 0, 2)
		}, magnitude: 4},
		{name: "SignerList legacy reserve", typ: entry.TypeSignerList, object: func(t *testing.T) []byte {
			return sponsorshipTestLegacySignerList(t, 2)
		}, magnitude: 4},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entries := sponsorshipTestEntries(t, test.typ, test.object(t), test.magnitude, test.magnitude)
			if violation := checkSponsorshipOwnerCounts(entries); violation != nil {
				t.Fatalf("unexpected violation: %v", violation)
			}
		})
	}
}

func TestSponsorshipOwnerCountMismatch(t *testing.T) {
	entries := []InvariantEntry{
		{EntryType: entry.TypeAccountRoot, After: sponsorshipTestAccountRoot(t, 1, 0, 1, 0)},
	}
	violation := checkSponsorshipOwnerCounts(entries)
	if violation == nil || !strings.Contains(violation.Message, "SponsoredOwnerCount") {
		t.Fatalf("expected aggregate sponsorship mismatch, got %v", violation)
	}
}

func TestSponsorshipAccountCountMatchesSponsorField(t *testing.T) {
	before := mustSerializeAccount(t, &state.AccountRoot{Account: sponsorshipTestAccount, Sequence: 1})
	after := mustSerializeAccount(t, &state.AccountRoot{
		Account: sponsorshipTestAccount, Sequence: 1, SponsoringAccountCount: 1,
		Sponsor: sponsorshipTestAccount, HasSponsor: true,
	})
	entries := []InvariantEntry{{EntryType: entry.TypeAccountRoot, Before: before, After: after}}
	if violation := checkSponsorship(entries); violation != nil {
		t.Fatalf("matching Sponsor field and count: %v", violation)
	}

	bad := mustSerializeAccount(t, &state.AccountRoot{
		Account: sponsorshipTestAccount, Sequence: 1, SponsoringAccountCount: 1,
	})
	if violation := checkSponsorship([]InvariantEntry{{EntryType: entry.TypeAccountRoot, Before: before, After: bad}}); violation == nil || !strings.Contains(violation.Message, "SponsoringAccountCount") {
		t.Fatalf("missing Sponsor field: %v", violation)
	}

	badFinal := mustSerializeAccount(t, &state.AccountRoot{
		Account: sponsorshipTestAccount, Sequence: 1, SponsoringAccountCount: 1,
	})
	deleted := InvariantEntry{EntryType: entry.TypeAccountRoot, Before: before, DeleteFinal: badFinal, IsDelete: true}
	if violation := checkSponsorshipAccountCount([]InvariantEntry{deleted}); violation == nil || !strings.Contains(violation.Message, "SponsoringAccountCount") {
		t.Fatalf("delete final image missing Sponsor field: %v", violation)
	}
}

func TestDeletedAccountCleanupUsesFinalImage(t *testing.T) {
	rules := amendment.NewRules([][32]byte{amendment.FeatureSponsor})
	before := mustSerializeAccount(t, &state.AccountRoot{Account: sponsorshipTestAccount, Balance: 100, Sequence: 1})
	final := mustSerializeAccount(t, &state.AccountRoot{Account: sponsorshipTestAccount, Sequence: 1})
	deleted := InvariantEntry{EntryType: entry.TypeAccountRoot, Before: before, DeleteFinal: final, IsDelete: true}
	if violation := checkAccountRootsDeletedClean([]InvariantEntry{deleted}, stubView{}, rules); violation != nil {
		t.Fatalf("zero final balance should permit deletion: %v", violation)
	}

	nonzeroFinal := mustSerializeAccount(t, &state.AccountRoot{Account: sponsorshipTestAccount, Balance: 1, Sequence: 1})
	deleted.DeleteFinal = nonzeroFinal
	if violation := checkAccountRootsDeletedClean([]InvariantEntry{deleted}, stubView{}, rules); violation == nil || !strings.Contains(violation.Message, "non-zero balance") {
		t.Fatalf("non-zero final balance: %v", violation)
	}

	sponsoredFinal := mustSerializeAccount(t, &state.AccountRoot{
		Account: sponsorshipTestAccount, Sequence: 1, SponsoredOwnerCount: 1,
	})
	deleted.DeleteFinal = sponsoredFinal
	if violation := checkAccountRootsDeletedClean([]InvariantEntry{deleted}, stubView{}, rules); violation == nil || !strings.Contains(violation.Message, "sponsorship field") {
		t.Fatalf("final sponsorship field: %v", violation)
	}
}

func TestPseudoAccountRejectsSponsorshipFields(t *testing.T) {
	rules := amendment.NewRules([][32]byte{amendment.FeatureSingleAssetVault})
	account := &state.AccountRoot{
		Account:             sponsorshipTestAccount,
		Sequence:            0,
		Flags:               LsfDisableMaster | LsfDefaultRipple | LsfDepositAuth,
		AMMID:               [32]byte{1},
		SponsoredOwnerCount: 1,
	}
	data := mustSerializeAccount(t, account)
	entries := []InvariantEntry{{EntryType: entry.TypeAccountRoot, After: data}}
	if violation := checkValidPseudoAccounts(entries, rules); violation == nil || !strings.Contains(violation.Message, "sponsorship field") {
		t.Fatalf("pseudo-account sponsorship field: %v", violation)
	}
}
