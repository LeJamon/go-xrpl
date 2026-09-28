package lending_test

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/metadata"
	mpttest "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	paytest "github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	txsign "github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/keylet"
)

const feeDrainPayment = int64(266_690_420)

type xrpLoanTransferFixture struct {
	env        *jtx.TestEnv
	borrower   *jtx.Account
	loanKey    keylet.Keylet
	ticketSeq  uint32
	before     map[[32]byte][]byte
	balance    uint64
	sequence   uint32
	ownerCount uint32
	fee        uint64
}

func newXRPLoanTransferFixture(t *testing.T, cleanup, ticket bool) *xrpLoanTransferFixture {
	t.Helper()
	env := newLendingEnv(t)
	if cleanup {
		env.EnableFeature("fixCleanup3_1_3")
	} else {
		env.DisableFeature("fixCleanup3_1_3")
	}
	env.Close()
	if got := env.FeatureEnabled("fixCleanup3_1_3"); got != cleanup {
		t.Fatalf("fixCleanup3_1_3 after close = %t, want %t", got, cleanup)
	}

	owner := jtx.NewAccount(fmt.Sprintf("xrp-transfer-owner-%t-%t", cleanup, ticket))
	borrower := jtx.NewAccount(fmt.Sprintf("xrp-transfer-borrower-%t-%t", cleanup, ticket))
	env.FundAmount(owner, 10_000_000_000)
	env.FundAmount(borrower, 10_000_000_000)

	vaultID := setupXRPVault(t, env, owner, 1_000_000_000)

	brokerSequence := env.Seq(owner)
	jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerSet(owner.Address, vaultID)))
	brokerKey := keylet.LoanBroker(owner.AccountID(), brokerSequence)

	loanSet := lending.NewLoanSet(borrower.Address, brokerID(owner, brokerSequence), "800000000")
	interestRate := uint32(12_000)
	interval, payments := uint32(3_600), uint32(12)
	loanSet.InterestRate = &interestRate
	loanSet.PaymentInterval = &interval
	loanSet.PaymentTotal = &payments
	serviceFee := "2"
	loanSet.LoanServiceFee = &serviceFee
	loanSet.Counterparty = owner.Address
	loanSet.GetCommon().Fee = "20"
	loanSet.GetCommon().SigningPubKey = strings.ToUpper(borrower.PublicKeyHex())
	signature, err := txsign.SignCounterpartyWithRules(
		loanSet,
		strings.ToUpper(owner.PublicKeyHex()),
		"00"+strings.ToUpper(owner.PrivateKeyHex()),
		env.Rules(),
	)
	if err != nil {
		t.Fatalf("sign LoanSet counterparty: %v", err)
	}
	loanSet.GetCommon().CounterpartySignature = signature
	jtx.RequireTxSuccess(t, env.Submit(loanSet))

	loanKey := keylet.Loan(brokerKey.Key, 1)
	ticketSequence := uint32(0)
	if ticket {
		ticketSequence = env.CreateTickets(borrower, 1)
	}

	// Leave exactly enough liquid XRP for the payment before the oversized fee.
	// The account's current owner count includes the Loan and, for the ticket
	// profile, the Ticket. This is a valid accountHoldsFull preclaim balance.
	reserve := env.ReserveBase() + uint64(env.OwnerCount(borrower))*env.ReserveIncrement()
	fee := reserve + 1
	target := reserve + uint64(feeDrainPayment)
	balance := env.Balance(borrower)
	if balance <= target+env.BaseFee() {
		t.Fatalf("borrower balance %d cannot be drained to target %d", balance, target)
	}
	drain := balance - target - env.BaseFee()
	jtx.RequireTxSuccess(t, env.Submit(paytest.Pay(borrower, owner, drain).Build()))
	if got := env.Balance(borrower); got != target {
		t.Fatalf("borrower balance before fee-drain = %d, want %d", got, target)
	}

	fixture := &xrpLoanTransferFixture{
		env:       env,
		borrower:  borrower,
		loanKey:   loanKey,
		ticketSeq: ticketSequence,
		fee:       fee,
	}
	fixture.balance = env.Balance(borrower)
	fixture.sequence = env.Seq(borrower)
	fixture.ownerCount = env.OwnerCount(borrower)
	fixture.before = loanSetLedgerState(t, env)
	return fixture
}

func submitFeeDrainLoanPay(t *testing.T, f *xrpLoanTransferFixture, ticket bool) jtx.TxResult {
	t.Helper()
	loanID := strings.ToUpper(hex.EncodeToString(f.loanKey.Key[:]))
	payment := lending.NewLoanPay(f.borrower.Address, loanID, tx.NewXRPAmount(feeDrainPayment))
	payment.GetCommon().Fee = fmt.Sprint(f.fee)
	if ticket {
		payment = jtx.WithTicketSeq(payment, f.ticketSeq).(*lending.LoanPay)
	}
	return f.env.Submit(payment)
}

func requireFeeDrainRollback(t *testing.T, f *xrpLoanTransferFixture, result jtx.TxResult, ticket bool) {
	t.Helper()
	if result.Code != "tecFAILED_PROCESSING" {
		t.Fatalf("LoanPay result = %s, want tecFAILED_PROCESSING", result.Code)
	}
	if !result.Applied || result.Fee != f.fee {
		t.Fatalf("LoanPay applied/fee = %v/%d, want true/%d", result.Applied, result.Fee, f.fee)
	}
	if got, want := f.env.Balance(f.borrower), f.balance-result.Fee; got != want {
		t.Fatalf("borrower balance after fee-drain = %d, want %d", got, want)
	}
	if got, want := f.env.Seq(f.borrower), f.sequence; !ticket && got != want+1 {
		t.Fatalf("sequence after sequence LoanPay = %d, want %d", got, want+1)
	} else if ticket && got != want {
		t.Fatalf("sequence after ticket LoanPay = %d, want unchanged %d", got, want)
	}
	if result.Metadata == nil || result.Metadata.TransactionResult.String() != "tecFAILED_PROCESSING" {
		t.Fatalf("LoanPay metadata result = %#v, want tecFAILED_PROCESSING", result.Metadata)
	}
	metadata.CheckXRPConservation(t, result, result.Fee)

	account := metadata.FindNode(result.Metadata, "ModifiedNode", "AccountRoot")
	if account == nil {
		t.Fatal("fee-drain metadata has no payer AccountRoot")
	}
	if got := account.PreviousFields["Balance"]; got != fmt.Sprint(f.env.Balance(f.borrower)+result.Fee) {
		t.Fatalf("metadata previous payer balance = %v, want %d", got, f.env.Balance(f.borrower)+result.Fee)
	}
	if got := account.FinalFields["Balance"]; got != fmt.Sprint(f.env.Balance(f.borrower)) {
		t.Fatalf("metadata final payer balance = %v, want %d", got, f.env.Balance(f.borrower))
	}
	if ticket {
		if got := f.env.TicketCount(f.borrower); got != 0 {
			t.Fatalf("ticket count after claimed ticket payment = %d, want 0", got)
		}
		if got := f.env.OwnerCount(f.borrower); got != f.ownerCount-1 {
			t.Fatalf("owner count after claimed ticket payment = %d, want %d", got, f.ownerCount-1)
		}
		deleted := metadata.FindNodes(result.Metadata, "DeletedNode", "Ticket")
		if len(deleted) != 1 {
			t.Fatalf("deleted ticket metadata nodes = %d, want 1", len(deleted))
		}
		if got := deleted[0].FinalFields["TicketSequence"]; got != f.ticketSeq {
			t.Fatalf("deleted ticket sequence metadata = %v, want %d", got, f.ticketSeq)
		}
	} else {
		if got := f.env.OwnerCount(f.borrower); got != f.ownerCount {
			t.Fatalf("owner count after sequence LoanPay = %d, want unchanged %d", got, f.ownerCount)
		}
		if got := account.PreviousFields["Sequence"]; got != f.sequence {
			t.Fatalf("metadata previous payer sequence = %v, want %d", got, f.sequence)
		}
		if got := account.FinalFields["Sequence"]; got != f.sequence+1 {
			t.Fatalf("metadata final payer sequence = %v, want %d", got, f.sequence+1)
		}
	}

	if ticket {
		if got := account.PreviousFields["OwnerCount"]; got != f.ownerCount {
			t.Fatalf("metadata previous payer owner count = %v, want %d", got, f.ownerCount)
		}
		if got := account.FinalFields["OwnerCount"]; got != f.ownerCount-1 {
			t.Fatalf("metadata final payer owner count = %v, want %d", got, f.ownerCount-1)
		}
	}

	allowed := map[[32]byte]bool{keylet.Account(f.borrower.AccountID()).Key: true}
	if ticket {
		allowed[keylet.Ticket(f.borrower.AccountID(), f.ticketSeq).Key] = true
		allowed[keylet.OwnerDir(f.borrower.AccountID()).Key] = true
	}
	after := loanSetLedgerState(t, f.env)

	for _, node := range result.Metadata.AffectedNodes {
		index, err := hex.DecodeString(node.LedgerIndex)
		if err != nil || len(index) != 32 {
			t.Fatalf("invalid metadata ledger index %q", node.LedgerIndex)
		}
		var key [32]byte
		copy(key[:], index)
		if !allowed[key] {
			t.Fatalf("unexpected failed LoanPay metadata node: %s:%s %s", node.NodeType, node.LedgerEntryType, node.LedgerIndex)
		}
		switch node.LedgerEntryType {
		case "AccountRoot":
			if node.NodeType != "ModifiedNode" {
				t.Fatalf("payer metadata node type = %s, want ModifiedNode", node.NodeType)
			}
		case "Ticket":
			if !ticket || node.NodeType != "DeletedNode" {
				t.Fatalf("unexpected ticket metadata node %s with ticket=%t", node.NodeType, ticket)
			}
		case "DirectoryNode":
			if !ticket || node.NodeType != "ModifiedNode" {
				t.Fatalf("unexpected owner-directory metadata node %s with ticket=%t", node.NodeType, ticket)
			}
		default:
			t.Fatalf("unexpected failed LoanPay metadata type %s:%s", node.NodeType, node.LedgerEntryType)
		}
	}

	keys := make(map[[32]byte]struct{}, len(f.before)+len(after))
	for key := range f.before {
		keys[key] = struct{}{}
	}
	for key := range after {
		keys[key] = struct{}{}
	}
	for key := range keys {
		before, beforeOK := f.before[key]
		after, afterOK := after[key]
		if string(before) == string(after) && beforeOK == afterOK {
			continue
		}
		if !allowed[key] {
			t.Fatalf("non-fee ledger entry %x changed after failed LoanPay", key)
		}
	}
}

func TestLoanPayXRPFeeDrainReturnsFailedProcessing(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		for _, ticket := range []bool{false, true} {
			t.Run(fmt.Sprintf("fixCleanup3_1_3=%t/ticket=%t", cleanup, ticket), func(t *testing.T) {
				f := newXRPLoanTransferFixture(t, cleanup, ticket)
				result := submitFeeDrainLoanPay(t, f, ticket)
				requireFeeDrainRollback(t, f, result, ticket)
			})
		}
	}
}

func TestLoanSetTwoRecipientDisbursementWaivesTransferRate(t *testing.T) {
	for _, kind := range []string{"IOU", "MPT"} {
		t.Run(kind, func(t *testing.T) {
			f := newLoanSetAssetFixture(t, kind)
			if kind == "IOU" {
				f.env.SetTransferRate(f.issuer, 1_250_000_000)
			} else {
				setMPTTransferFee(t, f)
			}
			f.createHolding(f.owner)

			jtx.RequireTxSuccess(t, submitLoanSet(t, f, f.borrower, f.owner, true))
			vaultFields := decodeLendingEntry(t, f.env, f.vaultKey)
			vaultAccount := jtx.NewAccountWithAddress("disbursement-vault", vaultFields["Account"].(string))
			if kind == "IOU" {
				jtx.RequireIOUBalance(t, f.env, f.borrower, f.issuer, "USD", 999)
				jtx.RequireIOUBalance(t, f.env, f.owner, f.issuer, "USD", 1)
				jtx.RequireIOUBalance(t, f.env, vaultAccount, f.issuer, "USD", 9_000)
			} else {
				f.token.RequireMPTokenAmount(f.borrower, 999)
				f.token.RequireMPTokenAmount(f.owner, 1)
				f.token.RequireMPTokenAmount(vaultAccount, 9_000)
			}
		})
	}
}

func TestLoanSetIssuerBorrowerUsesIssuerAlias(t *testing.T) {
	for _, kind := range []string{"IOU", "MPT"} {
		t.Run(kind, func(t *testing.T) {
			f := newLoanSetAssetFixture(t, kind)
			if kind == "IOU" {
				f.env.SetTransferRate(f.issuer, 1_250_000_000)
			} else {
				setMPTTransferFee(t, f)
			}
			f.createHolding(f.owner)
			f.borrower = f.issuer

			jtx.RequireTxSuccess(t, submitLoanSet(t, f, f.borrower, f.owner, true))
			if !f.env.LedgerEntryExists(keylet.Loan(f.brokerKey, 1)) {
				t.Fatal("issuer-borrower LoanSet did not create Loan")
			}
			vaultFields := decodeLendingEntry(t, f.env, f.vaultKey)
			vaultAccount := jtx.NewAccountWithAddress("issuer-alias-vault", vaultFields["Account"].(string))
			if kind == "IOU" {
				jtx.RequireIOUBalance(t, f.env, f.owner, f.issuer, "USD", 1)
				jtx.RequireIOUBalance(t, f.env, vaultAccount, f.issuer, "USD", 9_000)
			} else {
				f.token.RequireMPTokenAmount(f.owner, 1)
				f.token.RequireMPTokenAmount(vaultAccount, 9_000)
			}
		})
	}
}

func setMPTTransferFee(t *testing.T, f *loanSetAssetFixture) {
	t.Helper()
	const transferFee = uint16(2_500)
	f.token.Set(mpttest.SetOpts{
		Account:     f.issuer,
		TransferFee: mpttest.PtrUint16(transferFee),
	})
	f.env.Close()
	f.token.CheckTransferFee(transferFee)
	if !f.token.IsTransferFeePresent() {
		t.Fatal("MPT issuance transfer fee is not present")
	}
}

func TestLoanPayTwoRecipientDisbursementWaivesTransferFee(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		for _, kind := range []string{"IOU", "MPT"} {
			t.Run(fmt.Sprintf("fixCleanup3_1_3=%t/%s", cleanup, kind), func(t *testing.T) {
				env := newLendingEnv(t)
				if cleanup {
					env.EnableFeature("fixCleanup3_1_3")
				} else {
					env.DisableFeature("fixCleanup3_1_3")
				}
				env.Close()
				if got := env.FeatureEnabled("fixCleanup3_1_3"); got != cleanup {
					t.Fatalf("fixCleanup3_1_3 after close = %t, want %t", got, cleanup)
				}

				f := newLoanSetAssetFixtureWithEnv(t, env, kind, false)
				if kind == "IOU" {
					f.env.SetTransferRate(f.issuer, 1_250_000_000)
				} else {
					setMPTTransferFee(t, f)
				}
				f, loanID, pseudo := setupLoanPayFeeRoutingWithFixture(t, f)
				submitLoanPayFee(t, f, loanID)
				requireLoanPayFeeBalances(t, f, pseudo, 100, 0)
			})
		}
	}
}

func TestLoanSetXRPDisbursementHasTwoRecipients(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		t.Run(fmt.Sprintf("fixCleanup3_1_3=%t", cleanup), func(t *testing.T) {
			env := newLendingEnv(t)
			if cleanup {
				env.EnableFeature("fixCleanup3_1_3")
			} else {
				env.DisableFeature("fixCleanup3_1_3")
			}
			env.Close()
			if got := env.FeatureEnabled("fixCleanup3_1_3"); got != cleanup {
				t.Fatalf("fixCleanup3_1_3 after close = %t, want %t", got, cleanup)
			}
			owner := jtx.NewAccount(fmt.Sprintf("xrp-disbursement-owner-%t", cleanup))
			borrower := jtx.NewAccount(fmt.Sprintf("xrp-disbursement-borrower-%t", cleanup))
			env.FundAmount(owner, 10_000_000_000)
			env.FundAmount(borrower, 10_000_000_000)
			vaultSequence := env.Seq(owner)
			vaultID := setupXRPVault(t, env, owner, 10_000_000)
			vaultKey := keylet.Vault(owner.AccountID(), vaultSequence)
			vaultFields := decodeLendingEntry(t, env, vaultKey)
			vaultAccount := jtx.NewAccountWithAddress("xrp-disbursement-vault", vaultFields["Account"].(string))
			brokerSequence := env.Seq(owner)
			jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerSet(owner.Address, vaultID)))

			loanSet := lending.NewLoanSet(borrower.Address, brokerID(owner, brokerSequence), "1000")
			interestRate := uint32(12_000)
			interval, payments := uint32(3_600), uint32(12)
			originationFee := "1"
			loanSet.InterestRate = &interestRate
			loanSet.PaymentInterval = &interval
			loanSet.PaymentTotal = &payments
			loanSet.LoanOriginationFee = &originationFee
			loanSet.Counterparty = owner.Address
			loanSet.GetCommon().Fee = "20"
			loanSet.GetCommon().SigningPubKey = strings.ToUpper(borrower.PublicKeyHex())
			signature, err := txsign.SignCounterpartyWithRules(
				loanSet,
				strings.ToUpper(owner.PublicKeyHex()),
				"00"+strings.ToUpper(owner.PrivateKeyHex()),
				env.Rules(),
			)
			if err != nil {
				t.Fatalf("sign XRP LoanSet counterparty: %v", err)
			}
			loanSet.GetCommon().CounterpartySignature = signature
			borrowerBefore, ownerBefore := env.Balance(borrower), env.Balance(owner)
			vaultBefore := env.Balance(vaultAccount)
			result := env.Submit(loanSet)
			jtx.RequireTxSuccess(t, result)
			if got, want := env.Balance(borrower), borrowerBefore-result.Fee+999; got != want {
				t.Fatalf("borrower XRP balance = %d, want %d", got, want)
			}
			if got, want := env.Balance(owner), ownerBefore+1; got != want {
				t.Fatalf("owner origination-fee balance = %d, want %d", got, want)
			}
			if got, want := env.Balance(vaultAccount), vaultBefore-1000; got != want {
				t.Fatalf("vault XRP balance = %d, want %d", got, want)
			}
		})
	}
}
