// Package lending_test contains integration tests for the XLS-66 Lending
// Protocol transactors, ported from rippled's LoanBroker_test.cpp / Loan_test.cpp.
package lending_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	mpttest "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	txsign "github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
)

const reserveIncrement = "50000000" // one owner reserve increment in drops

// newLendingEnv returns an env with the lending amendment stack enabled.
func newLendingEnv(t *testing.T) *jtx.TestEnv {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.EnableFeature("SingleAssetVault")
	env.EnableFeature("MPTokensV1")
	env.EnableFeature("LendingProtocol")
	env.Close()
	return env
}

func vaultID(acc *jtx.Account, seq uint32) string {
	k := keylet.Vault(acc.AccountID(), seq)
	return strings.ToUpper(hex.EncodeToString(k.Key[:]))
}

func brokerID(acc *jtx.Account, seq uint32) string {
	k := keylet.LoanBroker(acc.AccountID(), seq)
	return strings.ToUpper(hex.EncodeToString(k.Key[:]))
}

// setupXRPVault creates an XRP vault owned by owner and deposits `deposit` drops
// from owner. Returns the vault ID.
func setupXRPVault(t *testing.T, env *jtx.TestEnv, owner *jtx.Account, deposit uint64) string {
	t.Helper()
	seq := env.Seq(owner)
	create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "XRP"})
	create.Common.Fee = reserveIncrement
	jtx.RequireTxSuccess(t, env.Submit(create))
	id := vaultID(owner, seq)
	dep := vault.NewVaultDeposit(owner.Address, id, tx.NewXRPAmount(int64(deposit)))
	jtx.RequireTxSuccess(t, env.Submit(dep))
	return id
}

// TestLoanBroker_Lifecycle exercises create → cover deposit → cover withdraw →
// delete for an XRP-vault loan broker.
func TestLoanBroker_Lifecycle(t *testing.T) {
	env := newLendingEnv(t)
	owner := jtx.NewAccount("owner")
	env.FundAmount(owner, 10_000_000_000) // 10k XRP for reserves
	vid := setupXRPVault(t, env, owner, 2_000_000_000)

	// Create the loan broker.
	brokerSeq := env.Seq(owner)
	set := lending.NewLoanBrokerSet(owner.Address, vid)
	rate := uint16(1000) // 1% management fee
	set.ManagementFeeRate = &rate
	jtx.RequireTxSuccess(t, env.Submit(set))
	bid := brokerID(owner, brokerSeq)
	bidBytes, _ := hex.DecodeString(bid)
	var bk [32]byte
	copy(bk[:], bidBytes)
	if !env.LedgerEntryExists(keylet.LoanBrokerByID(bk)) {
		t.Fatalf("LoanBroker entry not created")
	}

	// Deposit first-loss cover.
	dep := lending.NewLoanBrokerCoverDeposit(owner.Address, bid, tx.NewXRPAmount(500_000_000))
	jtx.RequireTxSuccess(t, env.Submit(dep))

	// Withdraw part of the cover back to the owner. An XRP pseudo-account keeps
	// its base reserve, so only balance-minus-reserve is spendable via withdraw;
	// the remainder is returned on delete.
	wd := lending.NewLoanBrokerCoverWithdraw(owner.Address, bid, tx.NewXRPAmount(200_000_000))
	jtx.RequireTxSuccess(t, env.Submit(wd))

	// Delete the broker; the remaining cover returns to the owner.
	del := lending.NewLoanBrokerDelete(owner.Address, bid)
	jtx.RequireTxSuccess(t, env.Submit(del))
	if env.LedgerEntryExists(keylet.LoanBrokerByID(bk)) {
		t.Fatalf("LoanBroker entry still exists after delete")
	}
}

func TestLoanBrokerSet_MPTVaultCreatesEmptyHolding(t *testing.T) {
	env := newLendingEnv(t)
	issuer := jtx.NewAccount("issuer")
	owner := jtx.NewAccount("owner")
	token := mpttest.NewMPTTester(t, env, issuer, mpttest.MPTInit{
		Holders: []*jtx.Account{owner},
	})
	token.Create(mpttest.CreateOpts{Flags: mpttest.TfMPTCanTransfer})

	vaultSeq := env.Seq(owner)
	create := vault.NewVaultCreate(owner.Address, tx.Asset{MPTIssuanceID: token.IssuanceID()})
	create.Common.Fee = reserveIncrement
	jtx.RequireTxSuccess(t, env.Submit(create))

	brokerSeq := env.Seq(owner)
	result := env.Submit(lending.NewLoanBrokerSet(owner.Address, vaultID(owner, vaultSeq)))
	jtx.RequireTxSuccess(t, result)
	createdMPTokens := 0
	for _, node := range result.Metadata.AffectedNodes {
		if node.NodeType == "CreatedNode" && node.LedgerEntryType == "MPToken" {
			createdMPTokens++
		}
	}
	if createdMPTokens != 1 {
		t.Fatalf("created MPToken count = %d, want 1", createdMPTokens)
	}

	brokerKey := keylet.LoanBroker(owner.AccountID(), brokerSeq)
	brokerData, err := env.LedgerEntry(brokerKey)
	if err != nil {
		t.Fatalf("read LoanBroker: %v", err)
	}
	brokerFields, err := binarycodec.DecodeBytes(brokerData)
	if err != nil {
		t.Fatalf("decode LoanBroker: %v", err)
	}
	pseudoAddress, ok := brokerFields["Account"].(string)
	if !ok {
		t.Fatalf("LoanBroker Account = %#v, want address", brokerFields["Account"])
	}
	pseudoID, err := state.DecodeAccountID(pseudoAddress)
	if err != nil {
		t.Fatalf("decode broker pseudo-account: %v", err)
	}

	issuanceBytes, err := hex.DecodeString(token.IssuanceID())
	if err != nil {
		t.Fatalf("decode MPTokenIssuanceID: %v", err)
	}
	var issuanceID [24]byte
	copy(issuanceID[:], issuanceBytes)
	holdingData, err := env.LedgerEntry(keylet.MPTokenByID(issuanceID, pseudoID))
	if err != nil {
		t.Fatalf("read broker MPToken: %v", err)
	}
	holdingFields, err := binarycodec.DecodeBytes(holdingData)
	if err != nil {
		t.Fatalf("decode broker MPToken: %v", err)
	}
	if got := holdingFields["Account"]; got != pseudoAddress {
		t.Fatalf("MPToken Account = %v, want %s", got, pseudoAddress)
	}
	gotIssuanceID, ok := holdingFields["MPTokenIssuanceID"].(string)
	if !ok || !strings.EqualFold(gotIssuanceID, token.IssuanceID()) {
		t.Fatalf("MPTokenIssuanceID = %v, want %s", holdingFields["MPTokenIssuanceID"], token.IssuanceID())
	}
	if _, present := holdingFields["MPTAmount"]; present {
		t.Fatalf("empty broker MPToken unexpectedly contains MPTAmount")
	}
}

// TestLoanBroker_AmendmentDisabled asserts LendingProtocol gating.
func TestLoanBroker_AmendmentDisabled(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("SingleAssetVault")
	env.EnableFeature("MPTokensV1")
	env.DisableFeature("LendingProtocol")
	env.Close()
	owner := jtx.NewAccount("owner")
	env.FundAmount(owner, 10_000_000_000)

	set := lending.NewLoanBrokerSet(owner.Address, strings.Repeat("A", 64))
	jtx.RequireTxFail(t, env.Submit(set), jtx.TemDISABLED)
}

// TestLoanBroker_SetNoVault asserts a broker on a missing vault is rejected.
func TestLoanBroker_SetNoVault(t *testing.T) {
	env := newLendingEnv(t)
	owner := jtx.NewAccount("owner")
	env.FundAmount(owner, 10_000_000_000)

	set := lending.NewLoanBrokerSet(owner.Address, strings.Repeat("A", 64))
	jtx.RequireTxClaimed(t, env.Submit(set), jtx.TecNO_ENTRY)
}

// TestLoanBroker_SetWrongOwner asserts only the vault owner may open a broker.
func TestLoanBroker_SetWrongOwner(t *testing.T) {
	env := newLendingEnv(t)
	owner := jtx.NewAccount("owner")
	other := jtx.NewAccount("other")
	env.FundAmount(owner, 10_000_000_000)
	env.FundAmount(other, 10_000_000_000)
	vid := setupXRPVault(t, env, owner, 1_000_000_000)

	set := lending.NewLoanBrokerSet(other.Address, vid)
	jtx.RequireTxClaimed(t, env.Submit(set), jtx.TecNO_PERMISSION)
}

// TestLoanBroker_CoverDepositInsufficientFunds asserts the funds check.
func TestLoanBroker_CoverDepositInsufficientFunds(t *testing.T) {
	env := newLendingEnv(t)
	owner := jtx.NewAccount("owner")
	env.FundAmount(owner, 10_000_000_000)
	vid := setupXRPVault(t, env, owner, 1_000_000_000)

	brokerSeq := env.Seq(owner)
	set := lending.NewLoanBrokerSet(owner.Address, vid)
	jtx.RequireTxSuccess(t, env.Submit(set))
	bid := brokerID(owner, brokerSeq)

	// Deposit more than the owner can spend.
	dep := lending.NewLoanBrokerCoverDeposit(owner.Address, bid, tx.NewXRPAmount(1_000_000_000_000))
	jtx.RequireTxClaimed(t, env.Submit(dep), jtx.TecINSUFFICIENT_FUNDS)
}

func TestLoanSet_UpwardPaymentCountXRP(t *testing.T) {
	env := newLendingEnv(t)
	owner := jtx.NewAccount("owner")
	borrower := jtx.NewAccount("borrower")
	env.FundAmount(owner, 10_000_000_000)
	env.FundAmount(borrower, 10_000_000_000)

	vaultSeq := env.Seq(owner)
	vid := setupXRPVault(t, env, owner, 10_000_000)
	vaultKey := keylet.Vault(owner.AccountID(), vaultSeq)

	brokerSeq := env.Seq(owner)
	jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerSet(owner.Address, vid)))
	bid := brokerID(owner, brokerSeq)
	brokerKey := keylet.LoanBroker(owner.AccountID(), brokerSeq)

	interestRate := uint32(500)
	paymentInterval := uint32(60)
	paymentTotal := uint32(12)
	loanSet := lending.NewLoanSet(borrower.Address, bid, "1000000")
	loanSet.InterestRate = &interestRate
	loanSet.PaymentInterval = &paymentInterval
	loanSet.PaymentTotal = &paymentTotal
	loanSet.Counterparty = owner.Address
	loanSet.GetCommon().Fee = "20"
	loanSet.GetCommon().SigningPubKey = strings.ToUpper(borrower.PublicKeyHex())
	counterpartySignature, err := txsign.SignCounterparty(
		loanSet,
		strings.ToUpper(owner.PublicKeyHex()),
		"00"+strings.ToUpper(owner.PrivateKeyHex()),
	)
	if err != nil {
		t.Fatalf("sign counterparty: %v", err)
	}
	loanSet.GetCommon().CounterpartySignature = counterpartySignature

	borrowerBalance := env.Balance(borrower)
	jtx.RequireTxSuccess(t, env.Submit(loanSet))

	decode := func(name string, k keylet.Keylet) map[string]any {
		t.Helper()
		data, err := env.LedgerEntry(k)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		fields, err := binarycodec.DecodeBytes(data)
		if err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		return fields
	}
	assertField := func(name string, fields map[string]any, field string, want any) {
		t.Helper()
		if got := fields[field]; got != want {
			t.Errorf("%s %s = %v, want %v", name, field, got, want)
		}
	}

	loanKey := keylet.Loan(brokerKey.Key, 1)
	loan := decode("Loan", loanKey)
	assertField("Loan", loan, "PeriodicPayment", "83333.33848930448955")
	assertField("Loan", loan, "PrincipalOutstanding", "1000000")
	assertField("Loan", loan, "TotalValueOutstanding", "1000001")
	assertField("Loan", loan, "LoanScale", 0)
	assertField("Loan", loan, "PaymentRemaining", uint32(12))

	vaultFields := decode("Vault", vaultKey)
	assertField("Vault", vaultFields, "AssetsAvailable", "9000000")
	assertField("Vault", vaultFields, "AssetsTotal", "10000001")

	broker := decode("LoanBroker", brokerKey)
	assertField("LoanBroker", broker, "DebtTotal", "1000001")
	assertField("LoanBroker", broker, "LoanSequence", uint32(2))
	assertField("LoanBroker", broker, "OwnerCount", uint32(1))

	accountID := func(name string, fields map[string]any) [20]byte {
		t.Helper()
		address, ok := fields["Account"].(string)
		if !ok {
			t.Fatalf("%s Account = %v, want address", name, fields["Account"])
		}
		id, err := state.DecodeAccountID(address)
		if err != nil {
			t.Fatalf("decode %s Account: %v", name, err)
		}
		return id
	}
	inOwnerDir := func(name string, owner [20]byte) bool {
		t.Helper()
		found := false
		if err := state.DirForEach(env.Ledger(), keylet.OwnerDir(owner), func(item [32]byte) error {
			if item == loanKey.Key {
				found = true
			}
			return nil
		}); err != nil {
			t.Fatalf("read %s owner directory: %v", name, err)
		}
		return found
	}
	if !inOwnerDir("LoanBroker", accountID("LoanBroker", broker)) {
		t.Error("Loan missing from LoanBroker pseudo-account owner directory")
	}
	if !inOwnerDir("borrower", borrower.AccountID()) {
		t.Error("Loan missing from borrower owner directory")
	}
	if inOwnerDir("Vault", accountID("Vault", vaultFields)) {
		t.Error("Loan present in Vault pseudo-account owner directory")
	}

	if got := env.OwnerCount(borrower); got != 1 {
		t.Errorf("borrower OwnerCount = %d, want 1", got)
	}
	wantBorrowerBalance := borrowerBalance + 1_000_000 - 2*env.BaseFee()
	if got := env.Balance(borrower); got != wantBorrowerBalance {
		t.Errorf("borrower balance = %d, want %d", got, wantBorrowerBalance)
	}
}

func TestLoanSet_ReplayUsesApplicationViewCloseTime(t *testing.T) {
	env := newLendingEnv(t)
	owner := jtx.NewAccount("owner")
	borrower := jtx.NewAccount("borrower")
	env.FundAmount(owner, 10_000_000_000)
	env.FundAmount(borrower, 10_000_000_000)

	vid := setupXRPVault(t, env, owner, 2_000_000_000)
	brokerSeq := env.Seq(owner)
	jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerSet(owner.Address, vid)))
	bid := brokerID(owner, brokerSeq)

	for env.Ledger().CloseTimeResolution() != 10 {
		env.Close()
	}
	env.CloseToParentCloseTime(835360951)
	if got := env.Ledger().CloseTimeResolution(); got != 10 {
		t.Fatalf("close-time resolution: got %d want 10", got)
	}

	env.EnableOpenLedgerReplay()
	interval := uint32(400)
	loanSet := lending.NewLoanSet(borrower.Address, bid, "1000000")
	loanSet.PaymentInterval = &interval
	loanSet.Counterparty = owner.Address
	loanSet.GetCommon().Fee = "20"
	loanSet.GetCommon().SigningPubKey = strings.ToUpper(borrower.PublicKeyHex())
	counterpartySignature, err := txsign.SignCounterparty(
		loanSet,
		strings.ToUpper(owner.PublicKeyHex()),
		"00"+strings.ToUpper(owner.PrivateKeyHex()),
	)
	if err != nil {
		t.Fatalf("sign counterparty: %v", err)
	}
	loanSet.GetCommon().CounterpartySignature = counterpartySignature
	jtx.RequireTxSuccess(t, env.Submit(loanSet))

	// Close() advances by one resolution, so seed it to store 835360952 while
	// the build view remains at parent plus resolution (835360961).
	env.SetTime(protocol.FromRippleTime(835360942))
	env.Close()
	closed := env.LastClosedLedger()
	if got := protocol.ToRippleTime(closed.ParentCloseTime()); got != 835360951 {
		t.Fatalf("stored parent close time: got %d want 835360951", got)
	}
	if got := protocol.ToRippleTime(closed.CloseTime()); got != 835360952 {
		t.Fatalf("stored close time: got %d want 835360952", got)
	}

	bidBytes, err := hex.DecodeString(bid)
	if err != nil {
		t.Fatalf("decode broker ID: %v", err)
	}
	var brokerKey [32]byte
	copy(brokerKey[:], bidBytes)
	data, err := env.LedgerEntry(keylet.Loan(brokerKey, 1))
	if err != nil {
		t.Fatalf("read Loan: %v", err)
	}
	loan, err := binarycodec.Decode(hex.EncodeToString(data))
	if err != nil {
		t.Fatalf("decode Loan: %v", err)
	}
	if got := loan["StartDate"]; got != uint32(835360961) {
		t.Fatalf("StartDate: got %v want 835360961", got)
	}
	if got := loan["NextPaymentDueDate"]; got != uint32(835361361) {
		t.Fatalf("NextPaymentDueDate: got %v want 835361361", got)
	}
}

func TestLoanSet_ReplayRechecksScheduleAgainstApplicationViewCloseTime(t *testing.T) {
	env := newLendingEnv(t)
	owner := jtx.NewAccount("owner")
	borrower := jtx.NewAccount("borrower")
	env.FundAmount(owner, 10_000_000_000)
	env.FundAmount(borrower, 10_000_000_000)

	vid := setupXRPVault(t, env, owner, 2_000_000_000)
	brokerSeq := env.Seq(owner)
	jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerSet(owner.Address, vid)))
	bid := brokerID(owner, brokerSeq)

	for env.Ledger().CloseTimeResolution() != 10 {
		env.Close()
	}
	const parentCloseTime = uint32(835360951)
	env.CloseToParentCloseTime(parentCloseTime)
	env.EnableOpenLedgerReplay()

	grace := uint32(5000)
	interval := ^uint32(0) - parentCloseTime - grace
	loanSet := lending.NewLoanSet(borrower.Address, bid, "1000000")
	loanSet.PaymentInterval = &interval
	loanSet.GracePeriod = &grace
	loanSet.Counterparty = owner.Address
	loanSet.GetCommon().Fee = "20"
	loanSet.GetCommon().SigningPubKey = strings.ToUpper(borrower.PublicKeyHex())
	counterpartySignature, err := txsign.SignCounterparty(
		loanSet,
		strings.ToUpper(owner.PublicKeyHex()),
		"00"+strings.ToUpper(owner.PrivateKeyHex()),
	)
	if err != nil {
		t.Fatalf("sign counterparty: %v", err)
	}
	loanSet.GetCommon().CounterpartySignature = counterpartySignature
	loanSeq := env.Seq(borrower)
	jtx.RequireTxSuccess(t, env.Submit(loanSet))

	env.SetTime(protocol.FromRippleTime(835360942))
	env.Close()
	if got := env.Seq(borrower); got != loanSeq+1 {
		t.Fatalf("borrower sequence after claimed replay: got %d want %d", got, loanSeq+1)
	}

	bidBytes, err := hex.DecodeString(bid)
	if err != nil {
		t.Fatalf("decode broker ID: %v", err)
	}
	var brokerKey [32]byte
	copy(brokerKey[:], bidBytes)
	if env.LedgerEntryExists(keylet.Loan(brokerKey, 1)) {
		t.Fatal("LoanSet accepted a schedule that overflows from application view close time")
	}

	const applicationCloseTime = uint32(835360962)
	interval = ^uint32(0) - applicationCloseTime - grace
	loanSet = lending.NewLoanSet(borrower.Address, bid, "1000000")
	loanSet.PaymentInterval = &interval
	loanSet.GracePeriod = &grace
	loanSet.Counterparty = owner.Address
	loanSet.GetCommon().Fee = "20"
	loanSet.GetCommon().SigningPubKey = strings.ToUpper(borrower.PublicKeyHex())
	counterpartySignature, err = txsign.SignCounterparty(
		loanSet,
		strings.ToUpper(owner.PublicKeyHex()),
		"00"+strings.ToUpper(owner.PrivateKeyHex()),
	)
	if err != nil {
		t.Fatalf("sign boundary counterparty: %v", err)
	}
	loanSet.GetCommon().CounterpartySignature = counterpartySignature
	jtx.RequireTxSuccess(t, env.Submit(loanSet))

	env.SetTime(protocol.FromRippleTime(835360943))
	env.Close()
	data, err := env.LedgerEntry(keylet.Loan(brokerKey, 1))
	if err != nil {
		t.Fatalf("read boundary Loan: %v", err)
	}
	loan, err := binarycodec.Decode(hex.EncodeToString(data))
	if err != nil {
		t.Fatalf("decode boundary Loan: %v", err)
	}
	if got := loan["StartDate"]; got != applicationCloseTime {
		t.Fatalf("boundary StartDate: got %v want %d", got, applicationCloseTime)
	}
	if got := loan["NextPaymentDueDate"]; got != ^uint32(0)-grace {
		t.Fatalf("boundary NextPaymentDueDate: got %v want %d", got, ^uint32(0)-grace)
	}
}
