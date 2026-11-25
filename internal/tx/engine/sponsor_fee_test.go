package engine

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/applystate"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

func sponsorFeeTestFixture(t *testing.T, balance uint64, reserve drops.XRPAmount) (*Engine, *mockBaseView, feePayer, [20]byte) {
	t.Helper()

	var sponsorID [20]byte
	sponsorID[0] = 1
	sponsorAddress := state.EncodeAccountIDSafe(sponsorID)
	sponsorData, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account: sponsorAddress,
		Balance: balance,
	})
	if err != nil {
		t.Fatalf("SerializeAccountRoot: %v", err)
	}
	view := newMockBaseView()
	if err := view.Insert(keylet.Account(sponsorID), sponsorData); err != nil {
		t.Fatalf("Insert sponsor account: %v", err)
	}
	engine := &Engine{
		view: view,
		config: txcore.EngineConfig{
			ReserveBase: uint64(reserve),
			Rules:       amendment.EmptyRules(),
		},
	}
	payer := feePayer{
		keylet:    keylet.Account(sponsorID),
		payerTy:   feePayerSponsorCoSigned,
		accountID: sponsorID,
		known:     true,
	}
	return engine, view, payer, sponsorID
}

func readSponsorFeeTestAccount(t *testing.T, view txcore.LedgerView, sponsorID [20]byte) *state.AccountRoot {
	t.Helper()
	data, err := view.Read(keylet.Account(sponsorID))
	if err != nil {
		t.Fatalf("Read sponsor account: %v", err)
	}
	account, err := state.ParseAccountRoot(data)
	if err != nil {
		t.Fatalf("ParseAccountRoot: %v", err)
	}
	return account
}

func TestFeePayerBalanceAndSpendableHonorsNegativeReserve(t *testing.T) {
	reserve := drops.XRPAmount(-10)
	engine, _, payer, _ := sponsorFeeTestFixture(t, 5, reserve)

	balance, spendable, result := engine.feePayerBalanceAndSpendable(payer, nil)
	if result != ter.TesSUCCESS {
		t.Fatalf("feePayerBalanceAndSpendable result = %s, want tesSUCCESS", result)
	}
	if balance != 5 {
		t.Fatalf("stored balance = %d, want 5", balance)
	}
	if spendable != 15 {
		t.Fatalf("spendable allowance = %d, want 15", spendable)
	}
}

func TestPayExternalFeeClampsStoredBalanceWithoutAllowanceWrap(t *testing.T) {
	reserve := drops.XRPAmount(-10)
	engine, view, payer, sponsorID := sponsorFeeTestFixture(t, 5, reserve)
	table := applystate.NewApplyStateTable(view, [32]byte{1}, 1, amendment.EmptyRules())

	st := &applyState{fee: 6, feePayer: payer}
	if result := engine.payExternalFeeOnTable(st, table, false); result != ter.TesSUCCESS {
		t.Fatalf("payExternalFeeOnTable result = %s, want tesSUCCESS", result)
	}
	if st.chargedFee != 6 {
		t.Fatalf("charged fee = %d, want 6 (allowance, not stored balance)", st.chargedFee)
	}
	if st.externalFeeDebt != 1 {
		t.Fatalf("external fee debt = %d, want 1", st.externalFeeDebt)
	}
	updated, err := table.Read(keylet.Account(sponsorID))
	if err != nil {
		t.Fatalf("Read staged sponsor account: %v", err)
	}
	account, err := state.ParseAccountRoot(updated)
	if err != nil {
		t.Fatalf("Parse staged sponsor account: %v", err)
	}
	if account.Balance != 0 {
		t.Fatalf("staged balance = %d, want 0 (clamped, not uint64-wrapped)", account.Balance)
	}
}

func TestPayExternalFeeRecoveryCapsToSpendableAllowance(t *testing.T) {
	reserve := drops.XRPAmount(-10)
	engine, view, payer, sponsorID := sponsorFeeTestFixture(t, 5, reserve)
	table := applystate.NewApplyStateTable(view, [32]byte{2}, 1, amendment.EmptyRules())

	st := &applyState{fee: 20, feePayer: payer}
	if result := engine.payExternalFeeOnTable(st, table, true); result != ter.TesSUCCESS {
		t.Fatalf("recovery payExternalFeeOnTable result = %s, want tesSUCCESS", result)
	}
	if st.chargedFee != 15 {
		t.Fatalf("recovery charged fee = %d, want 15 (signed spendable cap)", st.chargedFee)
	}
	if st.externalFeeDebt != 10 {
		t.Fatalf("recovery external fee debt = %d, want 10", st.externalFeeDebt)
	}
	account := readSponsorFeeTestAccount(t, table, sponsorID)
	if account.Balance != 0 {
		t.Fatalf("recovery balance = %d, want 0 (clamped, not uint64-wrapped)", account.Balance)
	}
}

func TestExternalFeeDebtSettlesAgainstIncomingSponsorCredit(t *testing.T) {
	reserve := drops.XRPAmount(-10)
	engine, view, payer, sponsorID := sponsorFeeTestFixture(t, 5, reserve)
	table := applystate.NewApplyStateTable(view, [32]byte{3}, 1, amendment.EmptyRules())

	st := &applyState{fee: 6, feePayer: payer}
	if result := engine.payExternalFeeOnTable(st, table, false); result != ter.TesSUCCESS {
		t.Fatalf("payExternalFeeOnTable result = %s, want tesSUCCESS", result)
	}
	data, err := table.Read(keylet.Account(sponsorID))
	if err != nil {
		t.Fatalf("Read staged sponsor account: %v", err)
	}
	account, err := state.ParseAccountRoot(data)
	if err != nil {
		t.Fatalf("Parse staged sponsor account: %v", err)
	}
	account.Balance = 10
	credited, err := state.SerializeAccountRoot(account)
	if err != nil {
		t.Fatalf("Serialize credited sponsor account: %v", err)
	}
	if err := table.Update(keylet.Account(sponsorID), credited); err != nil {
		t.Fatalf("Update credited sponsor account: %v", err)
	}

	if result := engine.settleExternalFeeDebt(st, table); result != ter.TesSUCCESS {
		t.Fatalf("settleExternalFeeDebt result = %s, want tesSUCCESS", result)
	}
	if st.externalFeeDebt != 0 {
		t.Fatalf("external fee debt = %d after settlement, want 0", st.externalFeeDebt)
	}
	account = readSponsorFeeTestAccount(t, table, sponsorID)
	if account.Balance != 9 {
		t.Fatalf("settled sponsor balance = %d, want 9 (credit minus signed deficit)", account.Balance)
	}
}
