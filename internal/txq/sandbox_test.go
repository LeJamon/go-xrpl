package txq

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx"
)

// mockTx is a minimal tx.Transaction used to drive tryClearAccountQueue in
// isolation. Behavior keyed on identity, not contents.
type mockTx struct{ id byte }

func (m *mockTx) TxType() tx.Type                  { return tx.TypeAccountSet }
func (m *mockTx) GetCommon() *tx.Common            { return &tx.Common{Fee: "10"} }
func (m *mockTx) Validate() error                  { return nil }
func (m *mockTx) Flatten() (map[string]any, error) { return map[string]any{}, nil }
func (m *mockTx) GetRawBytes() []byte              { return []byte{m.id} }
func (m *mockTx) SetRawBytes([]byte)               {}
func (m *mockTx) RequiredAmendments() [][32]byte   { return nil }

// mockSandbox records what was applied and whether the batch was committed.
type mockSandbox struct {
	results map[*mockTx]struct {
		res     tx.Result
		applied bool
	}
	appliedTo []*mockTx
	committed bool
}

func (s *mockSandbox) ApplyTransaction(txn tx.Transaction) (tx.Result, bool) {
	mt := txn.(*mockTx)
	s.appliedTo = append(s.appliedTo, mt)
	r := s.results[mt]
	return r.res, r.applied
}

func (s *mockSandbox) Commit() error {
	s.committed = true
	return nil
}

// mockClearCtx is an ApplyContext whose only meaningful method is NewSandbox.
// tryClearAccountQueue applies the whole batch through the sandbox, so the
// remaining methods are never reached and return zero values.
type mockClearCtx struct {
	sandbox *mockSandbox
	newErr  error
}

func (c *mockClearCtx) GetAccountSequence([20]byte) uint32 { return 0 }
func (c *mockClearCtx) AccountExists([20]byte) bool        { return true }
func (c *mockClearCtx) TicketExists([20]byte, uint32) bool { return true }
func (c *mockClearCtx) GetAccountBalance([20]byte) uint64  { return 0 }
func (c *mockClearCtx) GetAccountReserve(uint32) uint64    { return 0 }
func (c *mockClearCtx) GetBaseFee(tx.Transaction) uint64   { return 10 }
func (c *mockClearCtx) GetTxInLedger() uint32              { return 0 }
func (c *mockClearCtx) GetLedgerSequence() uint32          { return 0 }
func (c *mockClearCtx) ApplyTransaction(tx.Transaction) (tx.Result, bool) {
	return tx.TefINTERNAL, false
}
func (c *mockClearCtx) PreclaimTransaction(tx.Transaction, [20]byte, uint64, uint32) tx.Result {
	return 0
}
func (c *mockClearCtx) GetApplyFlags() tx.ApplyFlags { return 0 }
func (c *mockClearCtx) NewSandbox() (SandboxContext, error) {
	return c.sandbox, c.newErr
}

// setupClearQueue builds a TxQ with one preceding queued tx (seq 1) for the
// account and returns the queue, account queue, the preceding candidate, and
// the new tx submitted at seq 2. Fee levels are set far above the escalated
// series requirement so tryClearAccountQueue always advances to the apply
// stage.
func setupClearQueue() (*TxQ, *AccountQueue, *Candidate, *mockTx, [20]byte, SeqProxy) {
	q := New(DefaultConfig())
	account := [20]byte{1}
	aq := NewAccountQueue(account)

	precedingTx := &mockTx{id: 1}
	preceding := &Candidate{
		Txn:              precedingTx,
		Account:          account,
		FeeLevel:         FeeLevel(1_000_000),
		SeqProxy:         NewSeqProxySequence(1),
		RetriesRemaining: RetriesAllowed,
	}
	aq.Add(preceding)
	q.byAccount[account] = aq
	q.byFee = append(q.byFee, preceding)

	newTx := &mockTx{id: 2}
	return q, aq, preceding, newTx, account, NewSeqProxySequence(2)
}

func mkResults(entries ...struct {
	tx      *mockTx
	res     tx.Result
	applied bool
}) map[*mockTx]struct {
	res     tx.Result
	applied bool
} {
	m := make(map[*mockTx]struct {
		res     tx.Result
		applied bool
	})
	for _, e := range entries {
		m[e.tx] = struct {
			res     tx.Result
			applied bool
		}{e.res, e.applied}
	}
	return m
}

// TestTryClearAccountQueue_RollbackOnPrecedingFailure verifies that when a
// preceding queued tx fails to apply, the sandbox is discarded (never
// committed) and the queue is left intact — mirroring rippled TxQ.cpp:592-596.
func TestTryClearAccountQueue_RollbackOnPrecedingFailure(t *testing.T) {
	q, aq, preceding, newTx, account, seqProxy := setupClearQueue()
	sb := &mockSandbox{results: mkResults(
		struct {
			tx      *mockTx
			res     tx.Result
			applied bool
		}{preceding.Txn.(*mockTx), tx.TelCAN_NOT_QUEUE, false},
	)}
	ctx := &mockClearCtx{sandbox: sb}

	result := q.tryClearAccountQueue(ctx, aq, newTx, seqProxy, FeeLevel(1_000_000), 4, account)

	if result != nil {
		t.Fatalf("expected nil (fall through to queuing), got %+v", *result)
	}
	if sb.committed {
		t.Errorf("sandbox must NOT be committed when a preceding tx fails")
	}
	if len(sb.appliedTo) != 1 {
		t.Errorf("only the failing preceding tx should be applied, got %d applies", len(sb.appliedTo))
	}
	if _, stillQueued := aq.Transactions[NewSeqProxySequence(1)]; !stillQueued {
		t.Errorf("preceding tx must stay queued after a failed clear attempt")
	}
}

// TestTryClearAccountQueue_RollbackOnNewTxFailure verifies that when all
// preceding txs apply but the new tx fails, the sandbox is discarded and the
// queue is left intact (rippled commits only on result.applied, TxQ.cpp:1216).
func TestTryClearAccountQueue_RollbackOnNewTxFailure(t *testing.T) {
	q, aq, preceding, newTx, account, seqProxy := setupClearQueue()
	sb := &mockSandbox{results: mkResults(
		struct {
			tx      *mockTx
			res     tx.Result
			applied bool
		}{preceding.Txn.(*mockTx), tx.TesSUCCESS, true},
		struct {
			tx      *mockTx
			res     tx.Result
			applied bool
		}{newTx, tx.TelCAN_NOT_QUEUE, false},
	)}
	ctx := &mockClearCtx{sandbox: sb}

	result := q.tryClearAccountQueue(ctx, aq, newTx, seqProxy, FeeLevel(1_000_000), 4, account)

	if result == nil || result.Applied {
		t.Fatalf("expected a non-applied result, got %v", result)
	}
	if sb.committed {
		t.Errorf("sandbox must NOT be committed when the new tx fails")
	}
	if _, stillQueued := aq.Transactions[NewSeqProxySequence(1)]; !stillQueued {
		t.Errorf("preceding tx must stay queued after a failed clear attempt")
	}
}

// TestTryClearAccountQueue_CommitOnFullSuccess verifies the happy path: every
// tx applies, the sandbox is committed exactly once, and the cleared preceding
// txs are removed from the queue (rippled TxQ.cpp:602-611, 1218).
func TestTryClearAccountQueue_CommitOnFullSuccess(t *testing.T) {
	q, aq, preceding, newTx, account, seqProxy := setupClearQueue()
	sb := &mockSandbox{results: mkResults(
		struct {
			tx      *mockTx
			res     tx.Result
			applied bool
		}{preceding.Txn.(*mockTx), tx.TesSUCCESS, true},
		struct {
			tx      *mockTx
			res     tx.Result
			applied bool
		}{newTx, tx.TesSUCCESS, true},
	)}
	ctx := &mockClearCtx{sandbox: sb}

	result := q.tryClearAccountQueue(ctx, aq, newTx, seqProxy, FeeLevel(1_000_000), 4, account)

	if result == nil || !result.Applied {
		t.Fatalf("expected an applied result, got %v", result)
	}
	if !sb.committed {
		t.Errorf("sandbox MUST be committed on full success")
	}
	if _, stillQueued := aq.Transactions[NewSeqProxySequence(1)]; stillQueued {
		t.Errorf("preceding tx must be erased from the queue after a successful clear")
	}
}
