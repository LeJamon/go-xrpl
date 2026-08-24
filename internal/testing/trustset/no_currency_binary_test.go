package trustset

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
	sponsortx "github.com/LeJamon/go-xrpl/internal/tx/sponsor"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	trustsettx "github.com/LeJamon/go-xrpl/internal/tx/trustset"
	"github.com/LeJamon/go-xrpl/keylet"
)

const noCurrencyTrustSetBlob = "120014240045CF2A201B0045D0C5204A0000000363D4838D7EA4C680000000000000000000000000000000000000000001F2D3998AF7133840529595D2D80FFA90B670AD0D68400000000000000C7321ED5067639DE601D7043EA9EDAC37CF7CB9A5DC8E51268F3B4ABC2C61009B1F34017440673215C69AB49B2C1305A5D87E589E1DE61F0FCAE16EE1AC05E9A900E171F3489688E6739866F29919FF8F219B01EA375FAFDA1D5B6E37005D0DC019A007910B811426E613343B8F39EAD2216786FBBE891A9DB6609A801B1491CBEE1263AF5B8A4B253F68561611150D27064D"

func TestTrustSetNoCurrencyBinaryReplay(t *testing.T) {
	all.RegisterAll()
	raw, err := hex.DecodeString(noCurrencyTrustSetBlob)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := txcore.ParseFromBinary(raw)
	if err != nil {
		t.Fatalf("ParseFromBinary: %v", err)
	}
	parsed, ok := transaction.(*trustsettx.TrustSet)
	if !ok {
		t.Fatalf("parsed transaction = %T, want *trustset.TrustSet", transaction)
	}
	if parsed.LimitAmount.Currency != "1" {
		t.Fatalf("LimitAmount currency = %q, want canonical noCurrency string", parsed.LimitAmount.Currency)
	}
	if got := keylet.CurrencyBytes(parsed.LimitAmount.Currency); got != keylet.NoCurrency() {
		t.Fatalf("LimitAmount currency bytes = %X, want %X", got, keylet.NoCurrency())
	}
	reencoded, err := txcore.SerializeTransaction(transaction)
	if err != nil {
		t.Fatalf("SerializeTransaction: %v", err)
	}
	if !bytes.Equal(reencoded, raw) {
		t.Fatalf("canonical re-encoding changed transaction\nwant %X\n got %X", raw, reencoded)
	}
	flattened, err := transaction.Flatten()
	if err != nil {
		t.Fatalf("Flatten: %v", err)
	}
	flattenedBinary, err := binarycodec.EncodeBytes(flattened)
	if err != nil {
		t.Fatalf("encode flattened transaction: %v", err)
	}
	if !bytes.Equal(flattenedBinary, raw) {
		t.Fatalf("field re-encoding changed transaction\nwant %X\n got %X", raw, flattenedBinary)
	}
	if !bytes.Equal(transaction.GetRawBytes(), raw) {
		t.Fatal("parsed transaction did not retain the canonical binary")
	}
	if matches, err := txcore.CurrentFieldsMatchRaw(transaction); err != nil || !matches {
		t.Fatalf("CurrentFieldsMatchRaw = %v, %v", matches, err)
	}

	jsonTransaction := []byte(`{"TransactionType":"TrustSet","Account":"rhYgJBYvKE7ZoxMsWGK1RDeVYgLY9BfuSG","Fee":"12","Sequence":4575018,"LimitAmount":{"currency":"1","issuer":"rP3xrdjhZUneSig4yaswidJZV2FCEqV98K","value":"1"}}`)
	if _, err := txcore.ParseJSON(jsonTransaction); err == nil {
		t.Fatal(`ParseJSON accepted user-supplied currency "1"`)
	}

	env := jtx.NewTestEnv(t)
	env.EnableFeature("Sponsor")
	account := jtx.NewAccountWithAddress("account", parsed.Account)
	issuer := jtx.NewAccountWithAddress("issuer", parsed.LimitAmount.Issuer)
	sponsor := jtx.NewAccountWithAddress("sponsor", parsed.Sponsor)
	env.FundAmountNoRipple(account, uint64(jtx.XRP(1_000)))
	env.FundAmountNoRipple(issuer, uint64(jtx.XRP(1_000)))
	env.FundAmountNoRipple(sponsor, uint64(jtx.XRP(1_000)))
	env.Close()

	feeBudget := txcore.NewXRPAmount(100)
	ownerBudget := int32(1)
	sponsorship := sponsortx.NewSponsorshipSet(sponsor.Address)
	sponsorship.Sponsee = account.Address
	sponsorship.FeeAmountDelta = &feeBudget
	sponsorship.RemainingOwnerCountDelta = &ownerBudget
	created := env.SubmitWithOptions(sponsorship, jtx.SubmitOptions{SkipSignature: true})
	if created.Code != "tesSUCCESS" {
		t.Fatalf("create Sponsorship = %s, want tesSUCCESS", created.Code)
	}

	setAccountSequence(t, env, account, *parsed.Sequence)
	result := env.SubmitWithOptions(transaction, jtx.SubmitOptions{SkipSignature: true})
	if result.Code != "tesSUCCESS" || result.Result != ter.TesSUCCESS || !result.Applied {
		t.Fatalf("captured TrustSet = %s, result %s, applied %v", result.Code, result.Result, result.Applied)
	}
	if result.Metadata == nil || result.Metadata.TransactionResult != ter.TesSUCCESS {
		t.Fatalf("metadata result = %#v, want tesSUCCESS", result.Metadata)
	}

	lineKey := keylet.Line(account.ID, issuer.ID, parsed.LimitAmount.Currency)
	lineIndex := strings.ToUpper(hex.EncodeToString(lineKey.Key[:]))
	if lineIndex != "5CFD7BB1F51D068978B8DF15D0A9A276643169A72781AF081CA5A45CD0A44688" {
		t.Fatalf("RippleState index = %s", lineIndex)
	}
	var createdNode map[string]any
	for _, node := range result.Metadata.AffectedNodes {
		if node.NodeType == "CreatedNode" && node.LedgerEntryType == "RippleState" && node.LedgerIndex == lineIndex {
			createdNode = node.NewFields
			break
		}
	}
	if createdNode == nil {
		t.Fatal("metadata does not contain the created noCurrency RippleState")
	}
	assertNoCurrencyAmounts(t, createdNode)

	lineData, err := env.LedgerEntry(lineKey)
	if err != nil {
		t.Fatalf("read RippleState: %v", err)
	}
	if lineData == nil {
		t.Fatal("captured TrustSet did not create the noCurrency RippleState")
	}
	decoded, err := binarycodec.DecodeBytes(lineData)
	if err != nil {
		t.Fatalf("decode RippleState: %v", err)
	}
	assertNoCurrencyAmounts(t, decoded)
	line, err := state.ParseRippleState(lineData)
	if err != nil {
		t.Fatalf("ParseRippleState: %v", err)
	}
	for name, amount := range map[string]txcore.Amount{
		"Balance": line.Balance, "LowLimit": line.LowLimit, "HighLimit": line.HighLimit,
	} {
		if amount.Currency != "1" || keylet.CurrencyBytes(amount.Currency) != keylet.NoCurrency() {
			t.Fatalf("%s currency = %q, want noCurrency", name, amount.Currency)
		}
	}
}

func assertNoCurrencyAmounts(t *testing.T, fields map[string]any) {
	t.Helper()
	for _, name := range []string{"Balance", "LowLimit", "HighLimit"} {
		amount, ok := fields[name].(map[string]any)
		if !ok || amount["currency"] != "1" {
			t.Fatalf("%s = %#v, want currency 1", name, fields[name])
		}
	}
}

func setAccountSequence(t *testing.T, env *jtx.TestEnv, account *jtx.Account, sequence uint32) {
	t.Helper()
	accountKey := keylet.Account(account.ID)
	data, err := env.LedgerEntry(accountKey)
	if err != nil {
		t.Fatalf("read AccountRoot: %v", err)
	}
	root, err := state.ParseAccountRoot(data)
	if err != nil {
		t.Fatalf("parse AccountRoot: %v", err)
	}
	root.Sequence = sequence
	encoded, err := state.SerializeAccountRoot(root)
	if err != nil {
		t.Fatalf("serialize AccountRoot: %v", err)
	}
	if err := env.Ledger().Update(accountKey, encoded); err != nil {
		t.Fatalf("update AccountRoot: %v", err)
	}
}
