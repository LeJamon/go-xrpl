package lending

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

type coverClawbackView struct {
	data  map[[32]byte][]byte
	rules *amendment.Rules
}

func (v *coverClawbackView) Read(k keylet.Keylet) ([]byte, error) { return v.data[k.Key], nil }
func (v *coverClawbackView) Exists(k keylet.Keylet) (bool, error) {
	_, ok := v.data[k.Key]
	return ok, nil
}
func (v *coverClawbackView) Insert(k keylet.Keylet, data []byte) error {
	v.data[k.Key] = data
	return nil
}
func (v *coverClawbackView) Update(k keylet.Keylet, data []byte) error {
	v.data[k.Key] = data
	return nil
}
func (v *coverClawbackView) Erase(k keylet.Keylet) error {
	delete(v.data, k.Key)
	return nil
}
func (v *coverClawbackView) AdjustDropsDestroyed(drops.XRPAmount) {}
func (v *coverClawbackView) ForEach(fn func(key [32]byte, data []byte) bool) error {
	for key, data := range v.data {
		if !fn(key, data) {
			break
		}
	}
	return nil
}
func (v *coverClawbackView) Succ(key [32]byte) ([32]byte, []byte, bool, error) {
	return [32]byte{}, nil, false, nil
}
func (v *coverClawbackView) TxExists([32]byte) bool  { return false }
func (v *coverClawbackView) Rules() *amendment.Rules { return v.rules }
func (v *coverClawbackView) LedgerSeq() uint32       { return 1 }

type coverClawbackFixture struct {
	view       *coverClawbackView
	txn        *LoanBrokerCoverClawback
	config     tx.EngineConfig
	issuerID   [20]byte
	brokerID   [32]byte
	brokerAcct [20]byte
	mptID      [24]byte
}

func newCoverClawbackFixture(t *testing.T, flags uint32) *coverClawbackFixture {
	t.Helper()
	issuerID := repeatedAccountID(0x31)
	brokerAcct := repeatedAccountID(0x32)
	mptID := keylet.MakeMPTID(7, issuerID)
	var brokerID, vaultID, previousTxnID [32]byte
	for i := range brokerID {
		brokerID[i] = 0x41
		vaultID[i] = 0x42
		previousTxnID[i] = 0x43
	}
	rules := amendment.NewRules([][32]byte{
		amendment.FeatureMPTokensV1,
		amendment.FeatureSingleAssetVault,
		amendment.FeatureLendingProtocol,
		amendment.FeatureFixCleanup3_2_0,
	})
	view := &coverClawbackView{data: make(map[[32]byte][]byte), rules: rules}

	brokerBytes, err := serializeLoanBrokerForRules(&loanBrokerData{
		VaultID: vaultID, Account: brokerAcct, Owner: issuerID,
		CoverAvailable: "10000", DebtTotal: "30000",
		PreviousTxnID: previousTxnID, PreviousTxnLgrSeq: 1,
	}, rules)
	if err != nil {
		t.Fatalf("serialize broker: %v", err)
	}
	view.data[keylet.LoanBrokerByID(brokerID).Key] = brokerBytes

	issuerAddress := encodeTestAccount(t, issuerID)
	brokerAddress := encodeTestAccount(t, brokerAcct)
	vaultBlob, err := binarycodec.Encode(map[string]any{
		"LedgerEntryType":   "Vault",
		"Flags":             0,
		"Sequence":          1,
		"OwnerNode":         "0",
		"Owner":             issuerAddress,
		"Account":           brokerAddress,
		"Asset":             map[string]any{"mpt_issuance_id": strings.ToUpper(hex.EncodeToString(mptID[:]))},
		"ShareMPTID":        strings.Repeat("0", 48),
		"WithdrawalPolicy":  1,
		"AssetsTotal":       "30000",
		"AssetsAvailable":   "30000",
		"PreviousTxnID":     strings.ToUpper(hex.EncodeToString(previousTxnID[:])),
		"PreviousTxnLgrSeq": 1,
	})
	if err != nil {
		t.Fatalf("encode vault: %v", err)
	}
	vaultBytes, err := hex.DecodeString(vaultBlob)
	if err != nil {
		t.Fatalf("decode vault: %v", err)
	}
	view.data[keylet.VaultByID(vaultID).Key] = vaultBytes

	issuanceBytes, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{
		Issuer: issuerID, Sequence: 7, OutstandingAmount: 30_000, Flags: flags,
	})
	if err != nil {
		t.Fatalf("serialize issuance: %v", err)
	}
	view.data[keylet.MPTIssuance(mptID).Key] = issuanceBytes
	tokenBytes, err := state.SerializeMPToken(&state.MPTokenData{
		Account: brokerAcct, MPTokenIssuanceID: mptID, MPTAmount: 10_000,
	})
	if err != nil {
		t.Fatalf("serialize broker holding: %v", err)
	}
	view.data[keylet.MPTokenByID(mptID, brokerAcct).Key] = tokenBytes

	amount := state.NewMPTAmountWithIssuanceID(10_000, issuerAddress, hex.EncodeToString(mptID[:]))
	txn := NewLoanBrokerCoverClawback(issuerAddress)
	txn.LoanBrokerID = stringPtr(strings.ToUpper(hex.EncodeToString(brokerID[:])))
	txn.Amount = &amount
	return &coverClawbackFixture{
		view: view, txn: txn, config: tx.EngineConfig{Rules: rules},
		issuerID: issuerID, brokerID: brokerID, brokerAcct: brokerAcct, mptID: mptID,
	}
}

func TestLoanBrokerCoverClawbackMPTAuthorization(t *testing.T) {
	t.Run("issuer with clawback permission", func(t *testing.T) {
		f := newCoverClawbackFixture(t, entry.LsfMPTCanClawback)
		if got := f.txn.Preclaim(f.view, f.config); got != ter.TesSUCCESS {
			t.Fatalf("Preclaim() = %v, want tesSUCCESS", got)
		}
		ctx := &tx.ApplyContext{
			View: f.view, AccountID: f.issuerID, Config: f.config,
			Metadata: &tx.Metadata{}, Log: xrpllog.Discard(), Ctx: context.Background(),
		}
		if got := f.txn.Apply(ctx); got != ter.TesSUCCESS {
			t.Fatalf("Apply() = %v, want tesSUCCESS", got)
		}
		broker, err := readLoanBroker(f.view, keylet.LoanBrokerByID(f.brokerID))
		if err != nil || broker == nil {
			t.Fatalf("read broker: broker=%v err=%v", broker, err)
		}
		if got := lendNumForRules(broker.CoverAvailable, f.view.rules); !got.IsZero() {
			t.Fatalf("CoverAvailable = %s, want 0", broker.CoverAvailable)
		}
		issuance, err := state.ParseMPTokenIssuance(f.view.data[keylet.MPTIssuance(f.mptID).Key])
		if err != nil {
			t.Fatalf("parse issuance: %v", err)
		}
		if issuance.OutstandingAmount != 20_000 {
			t.Fatalf("OutstandingAmount = %d, want 20000", issuance.OutstandingAmount)
		}
		holding, err := state.ParseMPToken(f.view.data[keylet.MPTokenByID(f.mptID, f.brokerAcct).Key])
		if err != nil {
			t.Fatalf("parse holding: %v", err)
		}
		if holding.MPTAmount != 0 {
			t.Fatalf("broker holding = %d, want 0", holding.MPTAmount)
		}
	})

	t.Run("non-issuer", func(t *testing.T) {
		f := newCoverClawbackFixture(t, entry.LsfMPTCanClawback)
		f.txn.Account = encodeTestAccount(t, repeatedAccountID(0x33))
		if got := f.txn.Preclaim(f.view, f.config); got != ter.TecNO_PERMISSION {
			t.Fatalf("Preclaim() = %v, want tecNO_PERMISSION", got)
		}
	})

	t.Run("clawback disabled", func(t *testing.T) {
		f := newCoverClawbackFixture(t, 0)
		if got := f.txn.Preclaim(f.view, f.config); got != ter.TecNO_PERMISSION {
			t.Fatalf("Preclaim() = %v, want tecNO_PERMISSION", got)
		}
	})

	t.Run("broker holding below cover", func(t *testing.T) {
		f := newCoverClawbackFixture(t, entry.LsfMPTCanClawback)
		holding, err := state.ParseMPToken(f.view.data[keylet.MPTokenByID(f.mptID, f.brokerAcct).Key])
		if err != nil {
			t.Fatalf("parse holding: %v", err)
		}
		holding.MPTAmount = 9_999
		data, err := state.SerializeMPToken(holding)
		if err != nil {
			t.Fatalf("serialize holding: %v", err)
		}
		f.view.data[keylet.MPTokenByID(f.mptID, f.brokerAcct).Key] = data
		if got := f.txn.Preclaim(f.view, f.config); got != ter.TecINTERNAL {
			t.Fatalf("Preclaim() = %v, want tecINTERNAL", got)
		}
	})
}

func repeatedAccountID(value byte) [20]byte {
	var id [20]byte
	for i := range id {
		id[i] = value
	}
	return id
}

func encodeTestAccount(t *testing.T, id [20]byte) string {
	t.Helper()
	address, err := state.EncodeAccountID(id)
	if err != nil {
		t.Fatalf("encode account: %v", err)
	}
	return address
}

func stringPtr(value string) *string { return &value }
