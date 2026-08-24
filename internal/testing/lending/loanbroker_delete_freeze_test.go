package lending_test

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	mpttest "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/LeJamon/go-xrpl/internal/tx/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
)

type loanBrokerDeleteFreezeFixture struct {
	env        *jtx.TestEnv
	issuer     *jtx.Account
	owner      *jtx.Account
	pseudo     *jtx.Account
	brokerID   string
	brokerKey  keylet.Keylet
	holdingKey keylet.Keylet
	token      *mpttest.MPTTester
}

func newLoanBrokerDeleteFreezeFixture(t *testing.T, kind string, fixCleanup bool) *loanBrokerDeleteFreezeFixture {
	t.Helper()
	env := newLendingEnv(t)
	if !fixCleanup {
		env.DisableFeature("fixCleanup3_2_0")
		env.Close()
	}
	issuer := jtx.NewAccount(kind + "-delete-issuer")
	owner := jtx.NewAccount(kind + "-delete-owner")
	env.FundAmount(issuer, 10_000_000_000)
	env.FundAmount(owner, 10_000_000_000)

	var asset tx.Asset
	var cover tx.Amount
	var token *mpttest.MPTTester
	switch kind {
	case "IOU":
		asset = tx.Asset{Currency: "USD", Issuer: issuer.Address}
		env.Trust(owner, tx.NewIssuedAmountFromFloat64(1_000, "USD", issuer.Address))
		env.PayIOU(issuer, owner, issuer, "USD", 200)
		cover = tx.NewIssuedAmountFromFloat64(100, "USD", issuer.Address)
	case "MPT":
		token = mpttest.NewMPTTester(t, env, issuer, mpttest.MPTInit{Holders: []*jtx.Account{owner}})
		token.Create(mpttest.CreateOpts{Flags: mpttest.TfMPTCanTransfer | mpttest.TfMPTCanLock})
		token.Authorize(mpttest.AuthorizeOpts{Account: owner})
		token.Pay(issuer, owner, 200)
		asset = tx.Asset{MPTIssuanceID: token.IssuanceID()}
		cover = token.MPTAmount(100)
	default:
		t.Fatalf("unsupported asset kind %q", kind)
	}

	vaultSeq := env.Seq(owner)
	create := vault.NewVaultCreate(owner.Address, asset)
	create.Common.Fee = reserveIncrement
	jtx.RequireTxSuccess(t, env.Submit(create))
	brokerSeq := env.Seq(owner)
	jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerSet(owner.Address, vaultID(owner, vaultSeq))))
	brokerKey := keylet.LoanBroker(owner.AccountID(), brokerSeq)
	pseudoID := loanBrokerPseudoID(t, env, brokerKey)
	pseudoAddress, err := state.EncodeAccountID(pseudoID)
	if err != nil {
		t.Fatalf("encode broker pseudo-account: %v", err)
	}
	jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerCoverDeposit(owner.Address, brokerID(owner, brokerSeq), cover)))

	brokerData, err := env.LedgerEntry(brokerKey)
	if err != nil {
		t.Fatalf("read LoanBroker: %v", err)
	}
	brokerFields, err := binarycodec.DecodeBytes(brokerData)
	if err != nil {
		t.Fatalf("decode LoanBroker: %v", err)
	}
	if got := brokerFields["CoverAvailable"]; got != "100" {
		t.Fatalf("LoanBroker CoverAvailable = %v, want 100", got)
	}

	var holdingKey keylet.Keylet
	if kind == "IOU" {
		holdingKey = keylet.Line(pseudoID, issuer.AccountID(), "USD")
	} else {
		issuanceBytes, err := hex.DecodeString(token.IssuanceID())
		if err != nil {
			t.Fatalf("decode MPTokenIssuanceID: %v", err)
		}
		var issuanceID [24]byte
		copy(issuanceID[:], issuanceBytes)
		holdingKey = keylet.MPTokenByID(issuanceID, pseudoID)
	}

	return &loanBrokerDeleteFreezeFixture{
		env:        env,
		issuer:     issuer,
		owner:      owner,
		pseudo:     jtx.NewAccountWithAddress("broker-pseudo", pseudoAddress),
		brokerID:   brokerID(owner, brokerSeq),
		brokerKey:  brokerKey,
		holdingKey: holdingKey,
		token:      token,
	}
}

func (f *loanBrokerDeleteFreezeFixture) delete(t *testing.T) jtx.TxResult {
	t.Helper()
	return f.env.Submit(lending.NewLoanBrokerDelete(f.owner.Address, f.brokerID))
}

func assertLoanBrokerDeleteFeeOnly(t *testing.T, f *loanBrokerDeleteFreezeFixture, want string) {
	t.Helper()
	beforeState := loanSetLedgerState(t, f.env)
	beforeBalance := f.env.Balance(f.owner)
	beforeSequence := f.env.Seq(f.owner)
	beforeOwnerCount := f.env.OwnerCount(f.owner)

	result := f.delete(t)
	jtx.RequireTxClaimed(t, result, want)
	if !result.Applied || result.Fee != f.env.BaseFee() {
		t.Fatalf("applied/fee = %v/%d, want true/%d", result.Applied, result.Fee, f.env.BaseFee())
	}
	if result.Metadata == nil || result.Metadata.TransactionResult.String() != want {
		t.Fatalf("metadata result = %v, want %s", result.Metadata, want)
	}
	if len(result.Metadata.AffectedNodes) != 1 {
		t.Fatalf("affected nodes = %d, want 1", len(result.Metadata.AffectedNodes))
	}
	accountKey := keylet.Account(f.owner.AccountID())
	wantIndex := strings.ToUpper(hex.EncodeToString(accountKey.Key[:]))
	node := result.Metadata.AffectedNodes[0]
	if node.NodeType != "ModifiedNode" || node.LedgerEntryType != "AccountRoot" || node.LedgerIndex != wantIndex {
		t.Fatalf("affected node = %+v, want broker owner AccountRoot modification", node)
	}
	if got := f.env.Balance(f.owner); got != beforeBalance-f.env.BaseFee() {
		t.Errorf("broker owner balance = %d, want %d", got, beforeBalance-f.env.BaseFee())
	}
	if got := f.env.Seq(f.owner); got != beforeSequence+1 {
		t.Errorf("broker owner sequence = %d, want %d", got, beforeSequence+1)
	}
	if got := f.env.OwnerCount(f.owner); got != beforeOwnerCount {
		t.Errorf("broker owner OwnerCount = %d, want %d", got, beforeOwnerCount)
	}
	for name, entryKey := range map[string]keylet.Keylet{
		"LoanBroker":     f.brokerKey,
		"pseudo-account": keylet.Account(f.pseudo.AccountID()),
		"asset holding":  f.holdingKey,
	} {
		if !f.env.LedgerEntryExists(entryKey) {
			t.Errorf("%s missing after claimed deletion", name)
		}
	}

	afterState := loanSetLedgerState(t, f.env)
	delete(beforeState, accountKey.Key)
	delete(afterState, accountKey.Key)
	if len(afterState) != len(beforeState) {
		t.Fatalf("non-submitter state entry count = %d, want %d", len(afterState), len(beforeState))
	}
	for key, wantData := range beforeState {
		gotData, ok := afterState[key]
		if !ok || !bytes.Equal(gotData, wantData) {
			t.Fatalf("non-submitter ledger entry %X changed after claimed deletion", key)
		}
	}
}

func assertLoanBrokerDeleted(t *testing.T, f *loanBrokerDeleteFreezeFixture) {
	t.Helper()
	jtx.RequireTxSuccess(t, f.delete(t))
	for name, entryKey := range map[string]keylet.Keylet{
		"LoanBroker":     f.brokerKey,
		"pseudo-account": keylet.Account(f.pseudo.AccountID()),
		"asset holding":  f.holdingKey,
	} {
		if f.env.LedgerEntryExists(entryKey) {
			t.Errorf("%s remains after successful deletion", name)
		}
	}
	if f.token != nil {
		f.token.RequireMPTokenAmount(f.owner, 200)
	} else {
		jtx.RequireIOUBalance(t, f.env, f.owner, f.issuer, "USD", 200)
	}
}

func TestLoanBrokerDeleteRoundsResidualDebt(t *testing.T) {
	tests := []struct {
		name string
		debt string
		want string
	}{
		{name: "sub-scale debt", debt: "0.0000000005"},
		{name: "representable debt", debt: "0.000000001", want: jtx.TecHAS_OBLIGATIONS},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newLendingEnv(t)
			issuer := jtx.NewAccount("debt-delete-issuer")
			owner := jtx.NewAccount("debt-delete-owner")
			env.FundAmount(issuer, 10_000_000_000)
			env.FundAmount(owner, 10_000_000_000)
			env.Trust(owner, tx.NewIssuedAmountFromFloat64(2_000_000, "USD", issuer.Address))
			env.PayIOU(issuer, owner, issuer, "USD", 1_000_200)

			vaultSeq := env.Seq(owner)
			create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "USD", Issuer: issuer.Address})
			create.Common.Fee = reserveIncrement
			jtx.RequireTxSuccess(t, env.Submit(create))
			vaultID := vaultID(owner, vaultSeq)
			jtx.RequireTxSuccess(t, env.Submit(vault.NewVaultDeposit(
				owner.Address,
				vaultID,
				tx.NewIssuedAmountFromFloat64(1_000_000, "USD", issuer.Address),
			)))

			brokerSeq := env.Seq(owner)
			jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerSet(owner.Address, vaultID)))
			brokerKey := keylet.LoanBroker(owner.AccountID(), brokerSeq)
			pseudoID := loanBrokerPseudoID(t, env, brokerKey)
			pseudoAddress, err := state.EncodeAccountID(pseudoID)
			if err != nil {
				t.Fatalf("encode broker pseudo-account: %v", err)
			}

			brokerData, err := env.LedgerEntry(brokerKey)
			if err != nil {
				t.Fatalf("read LoanBroker: %v", err)
			}
			brokerFields, err := binarycodec.DecodeBytes(brokerData)
			if err != nil {
				t.Fatalf("decode LoanBroker: %v", err)
			}
			brokerFields["DebtTotal"] = test.debt
			brokerData, err = binarycodec.EncodeBytes(brokerFields)
			if err != nil {
				t.Fatalf("encode LoanBroker: %v", err)
			}
			if err := env.Ledger().Update(brokerKey, brokerData); err != nil {
				t.Fatalf("update LoanBroker: %v", err)
			}

			fixture := &loanBrokerDeleteFreezeFixture{
				env:        env,
				issuer:     issuer,
				owner:      owner,
				pseudo:     jtx.NewAccountWithAddress("broker-pseudo", pseudoAddress),
				brokerID:   brokerID(owner, brokerSeq),
				brokerKey:  brokerKey,
				holdingKey: keylet.Line(pseudoID, issuer.AccountID(), "USD"),
			}
			if test.want == "" {
				assertLoanBrokerDeleted(t, fixture)
				return
			}
			assertLoanBrokerDeleteFeeOnly(t, fixture, test.want)
		})
	}
}

func TestLoanBrokerDeleteOwnerDeepFreeze(t *testing.T) {
	t.Run("ordinary freeze permits returning cover", func(t *testing.T) {
		f := newLoanBrokerDeleteFreezeFixture(t, "IOU", true)
		f.env.FreezeTrustLine(f.issuer, f.owner, "USD")
		assertLoanBrokerDeleted(t, f)
	})

	for _, fixCleanup := range []bool{false, true} {
		name := "without fixCleanup3_2_0"
		if fixCleanup {
			name = "with fixCleanup3_2_0"
		}
		t.Run(name, func(t *testing.T) {
			f := newLoanBrokerDeleteFreezeFixture(t, "IOU", fixCleanup)
			freeze := trustset.NewTrustSet(f.issuer.Address, tx.NewIssuedAmountFromFloat64(0, "USD", f.owner.Address))
			freeze.SetFlags(trustset.TrustSetFlagSetFreeze | trustset.TrustSetFlagSetDeepFreeze)
			jtx.RequireTxSuccess(t, f.env.Submit(freeze))
			assertLoanBrokerDeleteFeeOnly(t, f, jtx.TecFROZEN)
		})
	}
}

func TestLoanBrokerDeletePseudoFreezeGate(t *testing.T) {
	for _, kind := range []string{"IOU", "MPT"} {
		for _, fixCleanup := range []bool{false, true} {
			name := kind + "/without fixCleanup3_2_0"
			if fixCleanup {
				name = kind + "/with fixCleanup3_2_0"
			}
			t.Run(name, func(t *testing.T) {
				f := newLoanBrokerDeleteFreezeFixture(t, kind, fixCleanup)
				if kind == "IOU" {
					f.env.FreezeTrustLine(f.issuer, f.pseudo, "USD")
				} else {
					f.token.Set(mpttest.SetOpts{Account: f.issuer, Holder: f.pseudo, Flags: mpttest.TfMPTLock})
				}

				if fixCleanup {
					want := jtx.TecFROZEN
					if kind == "MPT" {
						want = jtx.TecLOCKED
					}
					assertLoanBrokerDeleteFeeOnly(t, f, want)
					return
				}
				if kind == "IOU" {
					assertLoanBrokerDeleteFeeOnly(t, f, jtx.TecINVARIANT_FAILED)
					return
				}
				assertLoanBrokerDeleted(t, f)
			})
		}
	}
}
