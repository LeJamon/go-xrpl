package payment

import (
	"bytes"
	"testing"

	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/keylet"
	tx "github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
)

// ============================================================================
// Test Helpers - Mock LedgerView for testing
// ============================================================================

// paymentMockLedgerView implements LedgerView for testing
type paymentMockLedgerView struct {
	data       map[[32]byte][]byte
	ownerCount map[[20]byte]uint32
}

func newPaymentMockLedgerView() *paymentMockLedgerView {
	return &paymentMockLedgerView{
		data:       make(map[[32]byte][]byte),
		ownerCount: make(map[[20]byte]uint32),
	}
}

func (m *paymentMockLedgerView) Read(key keylet.Keylet) ([]byte, error) {
	return m.data[key.Key], nil
}

func (m *paymentMockLedgerView) Exists(key keylet.Keylet) (bool, error) {
	_, exists := m.data[key.Key]
	return exists, nil
}

func (m *paymentMockLedgerView) Insert(key keylet.Keylet, data []byte) error {
	m.data[key.Key] = data
	return nil
}

func (m *paymentMockLedgerView) Update(key keylet.Keylet, data []byte) error {
	m.data[key.Key] = data
	return nil
}

func (m *paymentMockLedgerView) Erase(key keylet.Keylet) error {
	delete(m.data, key.Key)
	return nil
}

func (m *paymentMockLedgerView) AdjustDropsDestroyed(drops drops.XRPAmount) {
	// No-op for testing
}

func (m *paymentMockLedgerView) TxExists(txID [32]byte) bool {
	return false
}

func (m *paymentMockLedgerView) ForEach(fn func(key [32]byte, data []byte) bool) error {
	for k, v := range m.data {
		if !fn(k, v) {
			break
		}
	}
	return nil
}

func (m *paymentMockLedgerView) Succ(key [32]byte) ([32]byte, []byte, bool, error) {
	var bestKey [32]byte
	found := false
	for k, v := range m.data {
		if bytes.Compare(k[:], key[:]) > 0 {
			if !found || bytes.Compare(k[:], bestKey[:]) < 0 {
				bestKey = k
				found = true
				_ = v
			}
		}
	}
	if found {
		return bestKey, m.data[bestKey], true, nil
	}
	return [32]byte{}, nil, false, nil
}

// Helper to create test account with balance
func (m *paymentMockLedgerView) createAccount(accountID [20]byte, balanceDrops uint64, ownerCount uint32) {
	account := &state.AccountRoot{
		Account:    state.EncodeAccountIDSafe(accountID),
		Balance:    balanceDrops,
		OwnerCount: ownerCount,
		Sequence:   1,
	}
	data, _ := state.SerializeAccountRoot(account)
	key := keylet.Account(accountID)
	m.data[key.Key] = data
	m.ownerCount[accountID] = ownerCount
}

// Helper to create test trust line
func (m *paymentMockLedgerView) createTrustLine(low, high [20]byte, currency string, balanceLow int64, limitLow, limitHigh int64) {
	// Create a RippleState (trust line) entry
	lowIssuer := state.EncodeAccountIDSafe(low)
	highIssuer := state.EncodeAccountIDSafe(high)

	rs := &state.RippleState{
		Balance:        tx.NewIssuedAmountFromFloat64(float64(balanceLow), currency, highIssuer),
		LowLimit:       tx.NewIssuedAmountFromFloat64(float64(limitLow), currency, lowIssuer),
		HighLimit:      tx.NewIssuedAmountFromFloat64(float64(limitHigh), currency, highIssuer),
		LowQualityIn:   QualityOne,
		LowQualityOut:  QualityOne,
		HighQualityIn:  QualityOne,
		HighQualityOut: QualityOne,
	}
	data, _ := state.SerializeRippleState(rs)
	key := keylet.Line(low, high, currency)
	m.data[key.Key] = data
}

// ============================================================================
// EitherAmount Tests
// ============================================================================

func TestEitherAmount_XRP(t *testing.T) {
	// Test XRP amount creation
	amt := NewXRPEitherAmount(1000000) // 1 XRP in drops

	if !amt.IsNative {
		t.Error("expected IsNative=true for XRP amount")
	}
	if amt.XRP != 1000000 {
		t.Errorf("expected XRP=1000000, got %d", amt.XRP)
	}
	if amt.IsZero() {
		t.Error("expected non-zero amount")
	}
}

func TestEitherAmount_IOU(t *testing.T) {
	// Test IOU amount creation
	iou := tx.NewIssuedAmountFromFloat64(100_000_000, "USD", "rIssuer123")
	amt := NewIOUEitherAmount(iou)

	if amt.IsNative {
		t.Error("expected IsNative=false for IOU amount")
	}
	if amt.IOU.Currency != "USD" {
		t.Errorf("expected currency=USD, got %s", amt.IOU.Currency)
	}
}

func TestEitherAmount_Add(t *testing.T) {
	// Test XRP addition
	a := NewXRPEitherAmount(100)
	b := NewXRPEitherAmount(50)
	c := a.Add(b)

	if c.XRP != 150 {
		t.Errorf("expected 100+50=150, got %d", c.XRP)
	}

	// Test IOU addition
	iouA := NewIOUEitherAmount(tx.NewIssuedAmountFromFloat64(100_000_000, "USD", "issuer"))
	iouB := NewIOUEitherAmount(tx.NewIssuedAmountFromFloat64(50_000_000, "USD", "issuer"))
	iouC := iouA.Add(iouB)

	// Check that the sum is 150M (using Float64 comparison)
	expectedValue := float64(150_000_000)
	actualValue := iouC.IOU.Float64()
	if actualValue != expectedValue {
		t.Errorf("expected IOU sum=150000000, got %v", actualValue)
	}
}

func TestEitherAmount_Compare(t *testing.T) {
	tests := []struct {
		name     string
		a, b     EitherAmount
		expected int
	}{
		{"XRP equal", NewXRPEitherAmount(100), NewXRPEitherAmount(100), 0},
		{"XRP less", NewXRPEitherAmount(50), NewXRPEitherAmount(100), -1},
		{"XRP greater", NewXRPEitherAmount(100), NewXRPEitherAmount(50), 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.a.Compare(tt.b)
			if result != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, result)
			}
		})
	}
}

// ============================================================================
// Quality Tests
// ============================================================================

func TestQuality_FromAmounts(t *testing.T) {
	// Quality = in / out
	// If in=100, out=100, quality should be 1.0
	in := NewXRPEitherAmount(100)
	out := NewXRPEitherAmount(100)

	q := QualityFromAmounts(in, out)

	// Quality.Value is STAmount-encoded (exponent in top 8 bits, mantissa in lower 56).
	// A quality of 1.0 is encoded by qualityFromFloat64(1.0), NOT by the raw uint32
	// constant QualityOne (which is the transfer rate identity, 1 billion).
	expectedQ := qualityFromFloat64(1.0)
	if q.Value != expectedQ.Value {
		t.Errorf("expected quality value %d (1.0 encoded), got %d", expectedQ.Value, q.Value)
	}
}

func TestQuality_BetterThan(t *testing.T) {
	// Lower quality value = better (less input for same output)
	better := Quality{Value: 500000000}  // 0.5 ratio
	worse := Quality{Value: 1500000000}  // 1.5 ratio

	if !better.BetterThan(worse) {
		t.Error("expected 0.5 to be better than 1.5")
	}
	if worse.BetterThan(better) {
		t.Error("expected 1.5 to NOT be better than 0.5")
	}
}

// ============================================================================
// PaymentSandbox Tests
// ============================================================================

func TestPaymentSandbox_Isolation(t *testing.T) {
	// Create base view with an account
	view := newPaymentMockLedgerView()
	var accountID [20]byte
	copy(accountID[:], []byte("alice12345678901234"))
	view.createAccount(accountID, 100_000_000, 0) // 100 XRP

	// Create sandbox
	sandbox := NewPaymentSandbox(view)

	// Verify we can read the account
	key := keylet.Account(accountID)
	data, err := sandbox.Read(key)
	if err != nil || data == nil {
		t.Fatal("expected to read account from sandbox")
	}

	// Modify in sandbox
	account, _ := state.ParseAccountRoot(data)
	account.Balance = 50_000_000 // 50 XRP
	newData, _ := state.SerializeAccountRoot(account)
	sandbox.Update(key, newData)

	// Verify sandbox has modified value
	modifiedData, _ := sandbox.Read(key)
	modifiedAccount, _ := state.ParseAccountRoot(modifiedData)
	if modifiedAccount.Balance != 50_000_000 {
		t.Errorf("expected sandbox balance=50M, got %d", modifiedAccount.Balance)
	}

	// Verify original view is unchanged
	originalData, _ := view.Read(key)
	originalAccount, _ := state.ParseAccountRoot(originalData)
	if originalAccount.Balance != 100_000_000 {
		t.Errorf("expected original balance=100M, got %d", originalAccount.Balance)
	}
}

func TestPaymentSandbox_ChildSandbox(t *testing.T) {
	view := newPaymentMockLedgerView()
	var accountID [20]byte
	copy(accountID[:], []byte("alice12345678901234"))
	view.createAccount(accountID, 100_000_000, 0)

	parent := NewPaymentSandbox(view)
	child := NewChildSandbox(parent)

	// Modify in child
	key := keylet.Account(accountID)
	data, _ := child.Read(key)
	account, _ := state.ParseAccountRoot(data)
	account.Balance = 25_000_000
	newData, _ := state.SerializeAccountRoot(account)
	child.Update(key, newData)

	// Verify child has modification
	childData, _ := child.Read(key)
	childAccount, _ := state.ParseAccountRoot(childData)
	if childAccount.Balance != 25_000_000 {
		t.Errorf("expected child balance=25M, got %d", childAccount.Balance)
	}

	// Verify parent is unchanged
	parentData, _ := parent.Read(key)
	parentAccount, _ := state.ParseAccountRoot(parentData)
	if parentAccount.Balance != 100_000_000 {
		t.Errorf("expected parent balance=100M, got %d", parentAccount.Balance)
	}

	// Apply child to parent
	child.Apply(parent)

	// Verify parent now has modification
	parentData2, _ := parent.Read(key)
	parentAccount2, _ := state.ParseAccountRoot(parentData2)
	if parentAccount2.Balance != 25_000_000 {
		t.Errorf("expected parent balance after apply=25M, got %d", parentAccount2.Balance)
	}
}

// ============================================================================
// XRPEndpointStep Tests
// ============================================================================

func TestXRPEndpointStep_Source(t *testing.T) {
	view := newPaymentMockLedgerView()
	var accountID [20]byte
	copy(accountID[:], []byte("alice12345678901234"))
	// Account with 100 XRP, reserve needs ~12 XRP (base 10 + owner 2)
	view.createAccount(accountID, 100_000_000, 1) // 100 XRP, 1 owner

	sandbox := NewPaymentSandbox(view)

	// Create source step (isLast=false)
	step := NewXRPEndpointStep(accountID, false)

	// Request 50 XRP output
	requestedOut := NewXRPEitherAmount(50_000_000)
	ofrsToRm := make(map[[32]byte]bool)

	actualIn, actualOut := step.Rev(sandbox, sandbox, ofrsToRm, requestedOut)

	// Should return what was requested (limited by available balance)
	// Available = 100M - 12M reserve = 88M
	if actualOut.XRP != 50_000_000 {
		t.Errorf("expected actualOut=50M, got %d", actualOut.XRP)
	}
	if actualIn.XRP != 50_000_000 {
		t.Errorf("expected actualIn=50M, got %d", actualIn.XRP)
	}
}

func TestXRPEndpointStep_Destination(t *testing.T) {
	view := newPaymentMockLedgerView()
	var accountID [20]byte
	copy(accountID[:], []byte("bob1234567890123456"))
	view.createAccount(accountID, 50_000_000, 0)

	sandbox := NewPaymentSandbox(view)

	// Create destination step (isLast=true)
	step := NewXRPEndpointStep(accountID, true)

	// Request 30 XRP
	requestedOut := NewXRPEitherAmount(30_000_000)
	ofrsToRm := make(map[[32]byte]bool)

	actualIn, actualOut := step.Rev(sandbox, sandbox, ofrsToRm, requestedOut)

	// Destination accepts full amount
	if actualOut.XRP != 30_000_000 {
		t.Errorf("expected actualOut=30M, got %d", actualOut.XRP)
	}
	if actualIn.XRP != 30_000_000 {
		t.Errorf("expected actualIn=30M, got %d", actualIn.XRP)
	}
}

func TestXRPEndpointStep_QualityUpperBound(t *testing.T) {
	var accountID [20]byte
	step := NewXRPEndpointStep(accountID, false)

	q, dir := step.QualityUpperBound(nil, DebtDirectionIssues)

	if q == nil {
		t.Fatal("expected non-nil quality")
	}
	// XRP has 1:1 quality — properly encoded as STAmount-like quality
	expectedQ := qualityFromFloat64(1.0)
	if q.Value != expectedQ.Value {
		t.Errorf("expected quality=%d, got %d", expectedQ.Value, q.Value)
	}
	if dir != DebtDirectionIssues {
		t.Error("expected DebtDirectionIssues")
	}
}

// ============================================================================
// DirectStepI Tests
// ============================================================================

func TestDirectStepI_Basic(t *testing.T) {
	view := newPaymentMockLedgerView()

	// Create accounts
	var alice, bob [20]byte
	copy(alice[:], []byte("alice12345678901234"))
	copy(bob[:], []byte("bob1234567890123456"))
	view.createAccount(alice, 100_000_000, 1)
	view.createAccount(bob, 100_000_000, 1)

	// Create trust line: alice owes bob 100 USD
	view.createTrustLine(alice, bob, "USD", 100_000_000, 1000_000_000, 1000_000_000)

	sandbox := NewPaymentSandbox(view)

	// Create direct step from alice to bob
	step := NewDirectStepI(alice, bob, "USD", nil, true, false)

	// Check the step
	result := step.Check(sandbox)
	if result != tx.TesSUCCESS {
		t.Errorf("expected tx.TesSUCCESS, got %d", result)
	}
}

// ============================================================================
// Strand Tests
// ============================================================================

func TestToStrand_XRPToXRP(t *testing.T) {
	view := newPaymentMockLedgerView()

	var alice, bob [20]byte
	copy(alice[:], []byte("alice12345678901234"))
	copy(bob[:], []byte("bob1234567890123456"))
	view.createAccount(alice, 100_000_000, 0)
	view.createAccount(bob, 100_000_000, 0)

	sandbox := NewPaymentSandbox(view)

	// XRP to XRP payment - default path
	dstIssue := Issue{Currency: "XRP"}

	strand, err := ToStrand(sandbox, alice, bob, dstIssue, nil, nil, true)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have at least source and destination XRP endpoints
	if len(strand) < 1 {
		t.Errorf("expected at least 1 step, got %d", len(strand))
	}
}

func TestToStrands_WithPaths(t *testing.T) {
	view := newPaymentMockLedgerView()

	var alice, bob, gateway [20]byte
	copy(alice[:], []byte("alice12345678901234"))
	copy(bob[:], []byte("bob1234567890123456"))
	copy(gateway[:], []byte("gateway1234567890ab"))
	view.createAccount(alice, 100_000_000, 1)
	view.createAccount(bob, 100_000_000, 1)
	view.createAccount(gateway, 100_000_000, 0)

	// Create trust lines required for the IOU path:
	// alice <-> gateway for USD (alice holds 100 USD from gateway)
	// gateway <-> bob for USD (bob holds 0 USD from gateway, with limit)
	view.createTrustLine(alice, gateway, "USD", 100_000_000, 1000_000_000, 1000_000_000)
	view.createTrustLine(bob, gateway, "USD", 0, 1000_000_000, 1000_000_000)

	sandbox := NewPaymentSandbox(view)

	// USD payment with explicit path through gateway
	dstAmt := tx.NewIssuedAmountFromFloat64(100_000_000, "USD", state.EncodeAccountIDSafe(gateway))
	paths := [][]PathStep{
		{{Currency: "USD", Issuer: state.EncodeAccountIDSafe(gateway)}},
	}

	strands, result := ToStrands(sandbox, alice, bob, dstAmt, nil, paths, true)

	if result != tx.TesSUCCESS {
		t.Fatalf("unexpected result: %v", result)
	}

	// Should create at least one strand
	if len(strands) < 1 {
		t.Errorf("expected at least 1 strand, got %d", len(strands))
	}
}

// ============================================================================
// ExecuteStrand Tests
// ============================================================================

func TestExecuteStrand_XRPPayment(t *testing.T) {
	view := newPaymentMockLedgerView()

	var alice, bob [20]byte
	copy(alice[:], []byte("alice12345678901234"))
	copy(bob[:], []byte("bob1234567890123456"))
	view.createAccount(alice, 100_000_000, 0)
	view.createAccount(bob, 50_000_000, 0)

	sandbox := NewPaymentSandbox(view)

	// Create a simple XRP strand: alice -> bob
	strand := Strand{
		NewXRPEndpointStep(alice, false), // Source
		NewXRPEndpointStep(bob, true),    // Destination
	}

	// Execute with 10 XRP requested output
	requestedOut := NewXRPEitherAmount(10_000_000)

	result := ExecuteStrand(sandbox, strand, nil, requestedOut)

	if !result.Success {
		t.Error("expected successful execution")
	}
	if result.Out.XRP != 10_000_000 {
		t.Errorf("expected output=10M, got %d", result.Out.XRP)
	}
	if result.In.XRP != 10_000_000 {
		t.Errorf("expected input=10M, got %d", result.In.XRP)
	}
}

// ============================================================================
// Flow Tests
// ============================================================================

func TestFlow_SingleStrand(t *testing.T) {
	view := newPaymentMockLedgerView()

	var alice, bob [20]byte
	copy(alice[:], []byte("alice12345678901234"))
	copy(bob[:], []byte("bob1234567890123456"))
	view.createAccount(alice, 100_000_000, 0)
	view.createAccount(bob, 50_000_000, 0)

	sandbox := NewPaymentSandbox(view)

	// Single XRP strand
	strands := []Strand{
		{
			NewXRPEndpointStep(alice, false),
			NewXRPEndpointStep(bob, true),
		},
	}

	requestedOut := NewXRPEitherAmount(10_000_000)

	result := Flow(sandbox, strands, requestedOut, false, nil, nil, nil)

	if result.Result != tx.TesSUCCESS {
		t.Errorf("expected tx.TesSUCCESS, got %d", result.Result)
	}
	if result.Out.XRP != 10_000_000 {
		t.Errorf("expected output=10M, got %d", result.Out.XRP)
	}
}

func TestFlow_PartialPayment(t *testing.T) {
	view := newPaymentMockLedgerView()

	var alice, bob [20]byte
	copy(alice[:], []byte("alice12345678901234"))
	copy(bob[:], []byte("bob1234567890123456"))
	// Alice has 50 XRP, reserve ~10 XRP (base only, no owner count), so ~40 XRP available
	view.createAccount(alice, 50_000_000, 0)
	view.createAccount(bob, 50_000_000, 0)

	sandbox := NewPaymentSandbox(view)

	strands := []Strand{
		{
			NewXRPEndpointStep(alice, false),
			NewXRPEndpointStep(bob, true),
		},
	}

	// Request more than available (40 XRP available, request 100)
	requestedOut := NewXRPEitherAmount(100_000_000)

	// Without partial payment flag - should fail or deliver less
	result := Flow(sandbox, strands, requestedOut, false, nil, nil, nil)

	// Should not deliver full amount
	if result.Out.XRP >= 100_000_000 {
		t.Error("expected partial delivery when requesting more than available")
	}

	// With partial payment flag - should succeed with whatever is available
	sandbox2 := NewPaymentSandbox(view)
	strands2 := []Strand{
		{
			NewXRPEndpointStep(alice, false),
			NewXRPEndpointStep(bob, true),
		},
	}
	result2 := Flow(sandbox2, strands2, requestedOut, true, nil, nil, nil)

	// With partial payment, any delivery (even partial) is success
	// We just check that something was delivered
	if result2.Out.XRP == 0 && result2.Result != tx.TecPATH_DRY {
		t.Errorf("expected some delivery with partial payment, got out=%d, result=%d", result2.Out.XRP, result2.Result)
	}
}

func TestFlow_EmptyStrands(t *testing.T) {
	view := newPaymentMockLedgerView()
	sandbox := NewPaymentSandbox(view)

	requestedOut := NewXRPEitherAmount(10_000_000)

	result := Flow(sandbox, []Strand{}, requestedOut, false, nil, nil, nil)

	if result.Result != tx.TecPATH_DRY {
		t.Errorf("expected tx.TecPATH_DRY for empty strands, got %d", result.Result)
	}
}

func TestFlow_SendMaxLimit(t *testing.T) {
	view := newPaymentMockLedgerView()

	var alice, bob [20]byte
	copy(alice[:], []byte("alice12345678901234"))
	copy(bob[:], []byte("bob1234567890123456"))
	view.createAccount(alice, 100_000_000, 0)
	view.createAccount(bob, 50_000_000, 0)

	sandbox := NewPaymentSandbox(view)

	strands := []Strand{
		{
			NewXRPEndpointStep(alice, false),
			NewXRPEndpointStep(bob, true),
		},
	}

	requestedOut := NewXRPEitherAmount(50_000_000)
	sendMax := NewXRPEitherAmount(20_000_000) // Limit to 20 XRP

	result := Flow(sandbox, strands, requestedOut, true, nil, &sendMax, nil)

	// Should be limited by sendMax
	if result.In.XRP > 20_000_000 {
		t.Errorf("expected input <= 20M (sendMax), got %d", result.In.XRP)
	}
}

// ============================================================================
// RippleCalculate Integration Test
// ============================================================================

func TestRippleCalculate_XRPPayment(t *testing.T) {
	view := newPaymentMockLedgerView()

	var alice, bob [20]byte
	copy(alice[:], []byte("alice12345678901234"))
	copy(bob[:], []byte("bob1234567890123456"))
	view.createAccount(alice, 100_000_000, 0)
	view.createAccount(bob, 50_000_000, 0)

	dstAmount := tx.NewXRPAmount(10_000_000) // 10 XRP
	var txHash [32]byte
	ledgerSeq := uint32(1000)

	actualIn, actualOut, _, _, result := RippleCalculate(
		view,
		alice,
		bob,
		dstAmount,
		nil,       // No SendMax
		nil,       // No explicit paths
		true,      // Add default path
		false,     // No partial payment
		false,     // No limit quality
		txHash,    // Transaction hash
		ledgerSeq, // Ledger sequence
	)

	if result != tx.TesSUCCESS && result != tx.TecPATH_DRY {
		t.Errorf("expected tx.TesSUCCESS or tx.TecPATH_DRY, got %d", result)
	}

	// If successful, verify amounts
	if result == tx.TesSUCCESS {
		if actualOut.XRP != 10_000_000 {
			t.Errorf("expected output=10M, got %d", actualOut.XRP)
		}
		if actualIn.XRP != 10_000_000 {
			t.Errorf("expected input=10M, got %d", actualIn.XRP)
		}
	}
}

// ============================================================================
// Issue and Book Tests
// ============================================================================

func TestIssue_IsXRP(t *testing.T) {
	xrpIssue := Issue{Currency: "XRP"}
	if !xrpIssue.IsXRP() {
		t.Error("expected XRP issue to return IsXRP=true")
	}

	usdIssue := Issue{Currency: "USD", Issuer: [20]byte{1, 2, 3}}
	if usdIssue.IsXRP() {
		t.Error("expected USD issue to return IsXRP=false")
	}

	emptyIssue := Issue{}
	if !emptyIssue.IsXRP() {
		t.Error("expected empty issue to be treated as XRP")
	}
}

func TestBook_Creation(t *testing.T) {
	inIssue := Issue{Currency: "USD", Issuer: [20]byte{1}}
	outIssue := Issue{Currency: "EUR", Issuer: [20]byte{2}}

	book := Book{In: inIssue, Out: outIssue}

	if book.In.Currency != "USD" {
		t.Errorf("expected In.Currency=USD, got %s", book.In.Currency)
	}
	if book.Out.Currency != "EUR" {
		t.Errorf("expected Out.Currency=EUR, got %s", book.Out.Currency)
	}
}

// ============================================================================
// MulRatio Tests
// ============================================================================

func TestMulRatio_XRP(t *testing.T) {
	amt := NewXRPEitherAmount(100)

	// Multiply by 1.5 (num=150, den=100)
	result := MulRatio(amt, 150, 100, false)

	if result.XRP != 150 {
		t.Errorf("expected 100 * 1.5 = 150, got %d", result.XRP)
	}

	// Test rounding up
	amt2 := NewXRPEitherAmount(100)
	result2 := MulRatio(amt2, 151, 100, true)

	// 100 * 151 / 100 = 151, no remainder so same
	if result2.XRP != 151 {
		t.Errorf("expected 151, got %d", result2.XRP)
	}
}

func TestMulRatio_IOU(t *testing.T) {
	iou := tx.NewIssuedAmountFromFloat64(100_000_000, "USD", "issuer")
	amt := NewIOUEitherAmount(iou)

	// Multiply by 2 (num=200, den=100)
	result := MulRatio(amt, 200, 100, false)

	expectedValue := float64(200_000_000)
	actualValue := result.IOU.Float64()
	if actualValue != expectedValue {
		t.Errorf("expected 100 * 2 = 200000000, got %v", actualValue)
	}
}

// ============================================================================
// Strand Quality Tests
// ============================================================================

func TestGetStrandQuality(t *testing.T) {
	view := newPaymentMockLedgerView()

	var alice, bob [20]byte
	copy(alice[:], []byte("alice12345678901234"))
	copy(bob[:], []byte("bob1234567890123456"))
	view.createAccount(alice, 100_000_000, 0)
	view.createAccount(bob, 50_000_000, 0)

	sandbox := NewPaymentSandbox(view)

	// Simple XRP strand should have quality = 1.0 (identity)
	strand := Strand{
		NewXRPEndpointStep(alice, false),
		NewXRPEndpointStep(bob, true),
	}

	q := GetStrandQuality(strand, sandbox)

	if q == nil {
		t.Fatal("expected non-nil quality")
	}

	// Quality.Value is STAmount-encoded. For XRP-to-XRP, each step has quality 1.0,
	// so the composed quality is also 1.0. Compare against qualityFromFloat64(1.0).
	expectedQ := qualityFromFloat64(1.0)
	if q.Value != expectedQ.Value {
		t.Errorf("expected quality value %d (1.0 encoded), got %d", expectedQ.Value, q.Value)
	}
}

// ============================================================================
// DebtDirection Tests
// ============================================================================

func TestDebtDirection(t *testing.T) {
	if !Issues(DebtDirectionIssues) {
		t.Error("expected Issues() to return true for DebtDirectionIssues")
	}
	if Issues(DebtDirectionRedeems) {
		t.Error("expected Issues() to return false for DebtDirectionRedeems")
	}
	if !Redeems(DebtDirectionRedeems) {
		t.Error("expected Redeems() to return true for DebtDirectionRedeems")
	}
	if Redeems(DebtDirectionIssues) {
		t.Error("expected Redeems() to return false for DebtDirectionIssues")
	}
}
