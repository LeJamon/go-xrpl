package txq

import (
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// seqTx is a minimal sequence-based tx.Transaction for driving TxQ.Apply.
type seqTx struct {
	seq           uint32
	ticket        uint32
	fee           string
	accountTxnID  string
	previousTxnID bool
	lastLedger    *uint32
	delegate      string
	sponsor       string
	sponsorFlags  *uint32
}

func (m *seqTx) TxType() tx.Type { return tx.TypeAccountSet }
func (m *seqTx) GetCommon() *tx.Common {
	s := m.seq
	common := &tx.Common{
		Account:            "rTest",
		Sequence:           &s,
		Fee:                m.fee,
		AccountTxnID:       m.accountTxnID,
		LastLedgerSequence: m.lastLedger,
		Delegate:           m.delegate,
		Sponsor:            m.sponsor,
		SponsorFlags:       m.sponsorFlags,
	}
	if m.previousTxnID {
		common.SetPresentFields(map[string]bool{"PreviousTxnID": true})
	}
	if m.ticket != 0 {
		zero := uint32(0)
		ticket := m.ticket
		common.Sequence = &zero
		common.TicketSequence = &ticket
	}
	return common
}
func (m *seqTx) Validate() error                  { return nil }
func (m *seqTx) Flatten() (map[string]any, error) { return map[string]any{}, nil }
func (m *seqTx) GetRawBytes() []byte              { return []byte{byte(m.seq)} }
func (m *seqTx) SetRawBytes([]byte)               {}
func (m *seqTx) RequiredAmendments() [][32]byte   { return nil }
func (*seqTx) txqSynthetic()                      {}

type batchSeqTx struct{ *seqTx }

func (m *batchSeqTx) TxType() tx.Type { return tx.TypeBatch }

// stubApplyCtx is a configurable txq.ApplyContext for admission tests. The
// preflight/preclaim/apply results are dialled in per test so we can pin which
// admission path rejects (or queues) a submission.
type stubApplyCtx struct {
	seq         uint32
	sequenceErr error
	balance     uint64
	balanceErr  error
	reserve     uint64
	exists      bool
	existsErr   error
	tickets     map[uint32]bool
	ticketErr   error
	baseFee     uint64
	txInLedger  uint32
	ledgerSeq   uint32
	flags       tx.ApplyFlags

	preflight ter.Result
	preclaim  ter.Result
	applyRes  ter.Result
	applied   bool
	applyFn   func(tx.Transaction) (ter.Result, bool)
	readFn    func()
}

func (c *stubApplyCtx) observeRead() {
	if c.readFn != nil {
		c.readFn()
	}
}

func (c *stubApplyCtx) GetAccountSequence([20]byte) (uint32, error) {
	c.observeRead()
	return c.seq, c.sequenceErr
}
func (c *stubApplyCtx) AccountExists([20]byte) (bool, error) {
	c.observeRead()
	return c.exists, c.existsErr
}
func (c *stubApplyCtx) TicketExists(_ [20]byte, t uint32) (bool, error) {
	c.observeRead()
	return c.tickets[t], c.ticketErr
}
func (c *stubApplyCtx) GetAccountBalance([20]byte) (uint64, error) {
	c.observeRead()
	return c.balance, c.balanceErr
}
func (c *stubApplyCtx) GetAccountReserve(uint32) uint64 {
	c.observeRead()
	return c.reserve
}
func (c *stubApplyCtx) GetBaseFees(tx.Transaction) (uint64, uint64) {
	c.observeRead()
	return c.baseFee, c.baseFee
}
func (c *stubApplyCtx) GetReferenceFee() uint64 {
	c.observeRead()
	return c.baseFee
}
func (c *stubApplyCtx) GetTxInLedger() uint32 {
	c.observeRead()
	return c.txInLedger
}
func (c *stubApplyCtx) GetLedgerSequence() uint32 {
	c.observeRead()
	return c.ledgerSeq
}
func (c *stubApplyCtx) GetApplyFlags() tx.ApplyFlags {
	c.observeRead()
	return c.flags
}
func (c *stubApplyCtx) GetParentHash() [32]byte {
	c.observeRead()
	return [32]byte{}
}
func (c *stubApplyCtx) PreflightTransaction(tx.Transaction) ter.Result {
	c.observeRead()
	return c.preflight
}
func (c *stubApplyCtx) PreclaimTransaction(tx.Transaction, [20]byte, uint64, uint32) ter.Result {
	c.observeRead()
	return c.preclaim
}

func (c *stubApplyCtx) ApplyTransaction(txn tx.Transaction) (ter.Result, bool) {
	c.observeRead()
	if c.applyFn != nil {
		return c.applyFn(txn)
	}
	return c.applyRes, c.applied
}

func (c *stubApplyCtx) ApplyTransactionWithFlags(txn tx.Transaction, _ tx.ApplyFlags) (ter.Result, bool) {
	return c.ApplyTransaction(txn)
}

func (c *stubApplyCtx) PreflightTransactionWithFlags(txn tx.Transaction, _ tx.ApplyFlags) ter.Result {
	return c.PreflightTransaction(txn)
}

func (c *stubApplyCtx) PreclaimTransactionWithFlags(txn tx.Transaction, account [20]byte, balance uint64, seq uint32, _ tx.ApplyFlags) ter.Result {
	return c.PreclaimTransaction(txn, account, balance, seq)
}

func (c *stubApplyCtx) RulesIdentity() *amendment.Rules {
	c.observeRead()
	return nil
}
func (c *stubApplyCtx) NewSandbox() (SandboxContext, error) {
	c.observeRead()
	return nil, errors.New("stubApplyCtx: no sandbox")
}

type stubClosedLedgerCtx struct{}

func (*stubClosedLedgerCtx) GetLedgerSequence() uint32           { return 0 }
func (*stubClosedLedgerCtx) GetTransactionFeeLevels() []FeeLevel { return nil }

// addQueued appends a sequence-based candidate to the account queue with an
// explicit followingSeq, so getNextQueuableSeq can walk the chain.
func addQueued(q *TxQ, aq *AccountQueue, seq, followingSeq uint32) *Candidate {
	sp := NewSeqProxySequence(seq)
	c := &Candidate{
		Txn:              &seqTx{seq: seq, fee: "10"},
		Account:          aq.Account,
		FeeLevel:         FeeLevel(BaseLevel * 10),
		SeqProxy:         sp,
		RetriesRemaining: RetriesAllowed,
		Consequences:     TxConsequences{Fee: 10, FollowingSeq: NewSeqProxySequence(followingSeq)},
	}
	aq.Add(c)
	q.insertByFee(c)
	return c
}

func TestAcceptDropsTefCategory(t *testing.T) {
	tests := []struct {
		name     string
		result   ter.Result
		wantSize int
	}{
		{name: "category lower boundary", result: ter.TefFAILURE},
		{name: "nftoken not transferable", result: ter.TefNFTOKEN_IS_NOT_TRANSFERABLE},
		{name: "invalid ledger fix type", result: ter.TefINVALID_LEDGER_FIX_TYPE},
		{name: "partial payment to new destination", result: ter.TefNO_DST_PARTIAL},
		{name: "bad payment path count", result: ter.TefBAD_PATH_COUNT},
		{name: "category upper boundary", result: ter.Result(-100)},
		{name: "retry category", result: ter.TerRETRY, wantSize: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			q := mustNew(makeAdmissionConfig())
			acct := [20]byte{9}
			aq := NewAccountQueue(acct)
			q.byAccount[acct] = aq
			candidate := addQueued(q, aq, 1, 2)

			ctx := &stubApplyCtx{applyRes: test.result}
			require.False(t, q.Accept(ctx))
			require.Equal(t, test.wantSize, q.Size())

			retained, exists := q.byAccount[acct]
			require.True(t, exists)
			require.Equal(t, test.result.IsTef(), retained.DropPenalty)
			if test.result.IsTef() {
				require.True(t, retained.Empty())
				require.Equal(t, RetriesAllowed, candidate.RetriesRemaining)
			} else {
				require.Equal(t, RetriesAllowed-1, candidate.RetriesRemaining)
				require.Equal(t, test.result, candidate.LastResult)
			}
		})
	}
}

func TestApplyQueueConstraintValidationPrecedence(t *testing.T) {
	accountID := [20]byte{9}
	newContext := func() *stubApplyCtx {
		return &stubApplyCtx{
			seq:        1,
			balance:    1_000_000_000,
			exists:     true,
			baseFee:    10,
			txInLedger: 100,
			flags:      tx.TapFAIL_HARD,
			preflight:  ter.TesSUCCESS,
			preclaim:   ter.TesSUCCESS,
		}
	}

	t.Run("account existence before hold constraints", func(t *testing.T) {
		q := mustNew(makeAdmissionConfig())
		ctx := newContext()
		ctx.exists = false

		result := q.Apply(ctx, &seqTx{seq: 1, fee: "10"}, [32]byte{1}, accountID)
		require.Equal(t, ter.TerNO_ACCOUNT, result.Result)
	})

	t.Run("ticket existence before hold constraints", func(t *testing.T) {
		q := mustNew(makeAdmissionConfig())
		ctx := newContext()

		result := q.Apply(ctx, &seqTx{ticket: 2, fee: "10"}, [32]byte{2}, accountID)
		require.Equal(t, ter.TerPRE_TICKET, result.Result)
	})

	t.Run("previous transaction id cannot be held", func(t *testing.T) {
		q := mustNew(makeAdmissionConfig())
		ctx := newContext()
		ctx.flags = tx.TapNONE

		result := q.Apply(ctx, &seqTx{seq: 1, fee: "10", previousTxnID: true}, [32]byte{6}, accountID)
		require.Equal(t, ter.TelCAN_NOT_QUEUE, result.Result)
		require.False(t, result.Queued)
	})

	t.Run("queued blocker before hold constraints", func(t *testing.T) {
		q := mustNew(makeAdmissionConfig())
		aq := NewAccountQueue(accountID)
		q.byAccount[accountID] = aq
		blocker := addQueued(q, aq, 1, 2)
		blocker.Consequences.IsBlocker = true

		result := q.Apply(newContext(), &seqTx{seq: 2, fee: "10"}, [32]byte{3}, accountID)
		require.Equal(t, ter.TelCAN_NOT_QUEUE_BLOCKED, result.Result)
	})

	t.Run("replacement fee before hold constraints", func(t *testing.T) {
		q := mustNew(makeAdmissionConfig())
		aq := NewAccountQueue(accountID)
		q.byAccount[accountID] = aq
		addQueued(q, aq, 1, 2)

		result := q.Apply(newContext(), &seqTx{seq: 1, fee: "10"}, [32]byte{4}, accountID)
		require.Equal(t, ter.TelCAN_NOT_QUEUE_FEE, result.Result)
	})

	t.Run("explicit zero last ledger before multi-path preclaim", func(t *testing.T) {
		q := mustNew(makeAdmissionConfig())
		aq := NewAccountQueue(accountID)
		q.byAccount[accountID] = aq
		addQueued(q, aq, 1, 2)
		ctx := newContext()
		ctx.flags = 0
		ctx.ledgerSeq = 10
		ctx.preclaim = ter.TefMAX_LEDGER
		zero := uint32(0)

		result := q.Apply(ctx, &seqTx{seq: 2, fee: "10", lastLedger: &zero}, [32]byte{5}, accountID)
		require.Equal(t, ter.TelCAN_NOT_QUEUE, result.Result)
	})
}

func TestAcceptPreflightFailureMarksDropPenalty(t *testing.T) {
	q := mustNew(makeAdmissionConfig())
	accountID := [20]byte{9}
	aq := NewAccountQueue(accountID)
	q.byAccount[accountID] = aq
	candidate := addQueued(q, aq, 1, 2)
	candidate.PreflightResult = ter.TesSUCCESS
	candidate.PreflightFlags = tx.TapUNLIMITED

	applyCalled := false
	ctx := &stubApplyCtx{
		preflight: ter.TemMALFORMED,
		applyFn: func(tx.Transaction) (ter.Result, bool) {
			applyCalled = true
			return ter.TesSUCCESS, true
		},
	}

	require.False(t, q.Accept(ctx))
	require.False(t, applyCalled)
	require.True(t, aq.DropPenalty)
	require.True(t, aq.Empty())
	require.Equal(t, ter.TemMALFORMED, candidate.LastResult)
}

func TestApplyRejectsDelegatedTransactionFromQueue(t *testing.T) {
	q := mustNew(makeAdmissionConfig())
	ctx := &stubApplyCtx{
		seq:        5,
		balance:    1_000_000,
		exists:     true,
		baseFee:    10,
		txInLedger: 100,
		preclaim:   ter.TesSUCCESS,
	}
	transaction := &seqTx{seq: 5, fee: "10", delegate: "rDelegate"}

	result := q.Apply(ctx, transaction, [32]byte{1}, [20]byte{1})
	require.Equal(t, ter.TelCAN_NOT_QUEUE, result.Result)
	require.False(t, result.Applied)
	require.Zero(t, q.Size())
}

func TestAcceptRevisitsNextAccountCandidateAcrossGap(t *testing.T) {
	q := mustNew(makeAdmissionConfig())
	acct := [20]byte{9}
	aq := NewAccountQueue(acct)
	q.byAccount[acct] = aq

	front := mkCandidate(acct, NewSeqProxySequence(1), FeeLevel(BaseLevel*2))
	successor := mkCandidate(acct, NewSeqProxySequence(3), FeeLevel(BaseLevel*3))
	aq.Add(front)
	aq.Add(successor)
	q.insertByFee(front)
	q.insertByFee(successor)

	var appliedSeqs []uint32
	ctx := &stubApplyCtx{applyFn: func(txn tx.Transaction) (ter.Result, bool) {
		seq := txn.(*seqTx).seq
		appliedSeqs = append(appliedSeqs, seq)
		if seq == 1 {
			return ter.TefNFTOKEN_IS_NOT_TRANSFERABLE, false
		}
		return ter.TesSUCCESS, true
	}}

	require.True(t, q.Accept(ctx))
	require.Equal(t, []uint32{1, 3}, appliedSeqs)
	require.Zero(t, q.Size())
	require.Same(t, aq, q.byAccount[acct])
	require.True(t, aq.DropPenalty)
}

func TestAcceptRevisitsNextTicketCandidate(t *testing.T) {
	q := mustNew(makeAdmissionConfig())
	acct := [20]byte{9}
	aq := NewAccountQueue(acct)
	q.byAccount[acct] = aq

	lowerTicket := mkCandidate(acct, NewSeqProxyTicket(2), FeeLevel(BaseLevel*2))
	higherTicket := mkCandidate(acct, NewSeqProxyTicket(3), FeeLevel(BaseLevel*3))
	aq.Add(lowerTicket)
	aq.Add(higherTicket)
	q.insertByFee(lowerTicket)
	q.insertByFee(higherTicket)

	var appliedTickets []uint32
	higherAttempts := 0
	ctx := &stubApplyCtx{applyFn: func(txn tx.Transaction) (ter.Result, bool) {
		ticket := txn.(*seqTx).seq
		appliedTickets = append(appliedTickets, ticket)
		if ticket == 2 {
			return ter.TefNO_TICKET, false
		}
		higherAttempts++
		if higherAttempts == 1 {
			return ter.TerPRE_TICKET, false
		}
		return ter.TesSUCCESS, true
	}}

	require.True(t, q.Accept(ctx))
	require.Equal(t, []uint32{3, 2, 3}, appliedTickets)
	require.Zero(t, q.Size())
}

func TestAcceptDropsQueuedPastSequence(t *testing.T) {
	q := mustNew(makeAdmissionConfig())
	acct := [20]byte{9}
	aq := NewAccountQueue(acct)
	q.byAccount[acct] = aq
	addQueued(q, aq, 1, 2)

	ctx := &stubApplyCtx{seq: 2}
	ctx.applyFn = func(txn tx.Transaction) (ter.Result, bool) {
		if *txn.GetCommon().Sequence < ctx.seq {
			return ter.TefPAST_SEQ, false
		}
		return ter.TesSUCCESS, true
	}

	require.False(t, q.Accept(ctx))
	require.True(t, aq.Empty())
	require.True(t, aq.DropPenalty)
}

func TestAcceptRetainedRetryPenaltyAffectsNextCandidate(t *testing.T) {
	q := mustNew(makeAdmissionConfig())
	acct := [20]byte{9}
	aq := NewAccountQueue(acct)
	q.byAccount[acct] = aq
	addQueued(q, aq, 1, 2)

	ctx := &stubApplyCtx{applyRes: ter.TerPRE_SEQ}
	for range RetriesAllowed + 1 {
		require.False(t, q.Accept(ctx))
	}

	require.True(t, aq.Empty())
	require.True(t, aq.RetryPenalty)

	next := addQueued(q, aq, 2, 3)
	require.False(t, q.Accept(ctx))
	require.Equal(t, 1, next.RetriesRemaining)
}

func TestAcceptRetainedDropPenaltyDropsLastCandidate(t *testing.T) {
	q := mustNew(makeAdmissionConfig())
	acct := [20]byte{9}
	aq := NewAccountQueue(acct)
	q.byAccount[acct] = aq
	addQueued(q, aq, 1, 2)

	require.False(t, q.Accept(&stubApplyCtx{applyRes: ter.TefNFTOKEN_IS_NOT_TRANSFERABLE}))
	require.True(t, aq.DropPenalty)

	first := mkCandidate(acct, NewSeqProxySequence(2), FeeLevel(BaseLevel*3))
	last := mkCandidate(acct, NewSeqProxySequence(3), FeeLevel(BaseLevel*2))
	aq.Add(first)
	aq.Add(last)
	q.insertByFee(first)
	q.insertByFee(last)
	q.setMaxSize(2)

	require.False(t, q.Accept(&stubApplyCtx{applyRes: ter.TerPRE_SEQ}))
	require.Equal(t, RetriesAllowed-1, first.RetriesRemaining)
	require.Same(t, first, aq.Transactions[first.SeqProxy])
	_, exists := aq.Transactions[last.SeqProxy]
	require.False(t, exists)
}

func TestAcceptCleansRetainedPenaltyOnNextClosedLedger(t *testing.T) {
	q := mustNew(makeAdmissionConfig())
	acct := [20]byte{9}
	aq := NewAccountQueue(acct)
	q.byAccount[acct] = aq
	addQueued(q, aq, 1, 2)

	ctx := &stubApplyCtx{applyRes: ter.TefNFTOKEN_IS_NOT_TRANSFERABLE}
	require.False(t, q.Accept(ctx))
	retained, exists := q.byAccount[acct]
	require.True(t, exists)
	require.True(t, retained.DropPenalty)
	require.True(t, retained.Empty())

	q.ProcessClosedLedger(&stubClosedLedgerCtx{}, false)
	_, exists = q.byAccount[acct]
	require.False(t, exists)
}

func TestEraseRetainsAccountQueueUntilNextClosedLedger(t *testing.T) {
	q := mustNew(makeAdmissionConfig())
	acct := [20]byte{9}
	aq := NewAccountQueue(acct)
	aq.DropPenalty = true
	aq.RetryPenalty = true
	q.byAccount[acct] = aq
	candidate := addQueued(q, aq, 1, 2)

	q.erase(candidate)

	retained, exists := q.byAccount[acct]
	require.True(t, exists)
	require.Same(t, aq, retained)
	require.True(t, retained.Empty())
	require.True(t, retained.DropPenalty)
	require.True(t, retained.RetryPenalty)

	q.ProcessClosedLedger(&stubClosedLedgerCtx{}, false)
	_, exists = q.byAccount[acct]
	require.False(t, exists)
}

func TestApplyReusesRetainedEmptyAccountQueue(t *testing.T) {
	q := mustNew(makeAdmissionConfig())
	acct := [20]byte{9}
	aq := NewAccountQueue(acct)
	aq.DropPenalty = true
	aq.RetryPenalty = true
	q.byAccount[acct] = aq
	candidate := addQueued(q, aq, 1, 2)
	q.erase(candidate)

	ctx := &stubApplyCtx{
		seq:        2,
		balance:    1_000_000_000,
		exists:     true,
		baseFee:    10,
		txInLedger: 4,
	}

	past := q.Apply(ctx, &seqTx{seq: 1, fee: "10"}, [32]byte{1}, acct)
	require.Equal(t, ter.TefPAST_SEQ, past.Result)
	future := q.Apply(ctx, &seqTx{seq: 3, fee: "10"}, [32]byte{2}, acct)
	require.Equal(t, ter.TerPRE_SEQ, future.Result)
	current := q.Apply(ctx, &seqTx{seq: 2, fee: "10"}, [32]byte{3}, acct)
	require.Equal(t, ter.TerQUEUED, current.Result)
	require.True(t, current.Queued)

	require.Same(t, aq, q.byAccount[acct])
	require.True(t, aq.DropPenalty)
	require.True(t, aq.RetryPenalty)
	require.Contains(t, aq.Transactions, NewSeqProxySequence(2))
}

func TestApplyFullQueueEvictionRetainsAccountQueue(t *testing.T) {
	q := mustNew(makeAdmissionConfig())
	evictedAccount := [20]byte{8}
	evictedQueue := NewAccountQueue(evictedAccount)
	evictedQueue.DropPenalty = true
	evictedQueue.RetryPenalty = true
	q.byAccount[evictedAccount] = evictedQueue
	evicted := addQueued(q, evictedQueue, 1, 2)
	evicted.FeeLevel = FeeLevel(BaseLevel)
	q.setMaxSize(1)

	newAccount := [20]byte{9}
	ctx := &stubApplyCtx{
		seq:        1,
		balance:    1_000_000_000,
		exists:     true,
		baseFee:    10,
		txInLedger: 4,
	}
	result := q.Apply(ctx, &seqTx{seq: 1, fee: "130"}, [32]byte{4}, newAccount)

	require.Equal(t, ter.TerQUEUED, result.Result)
	require.True(t, result.Queued)
	require.Same(t, evictedQueue, q.byAccount[evictedAccount])
	require.True(t, evictedQueue.Empty())
	require.True(t, evictedQueue.DropPenalty)
	require.True(t, evictedQueue.RetryPenalty)
	require.Contains(t, q.byAccount, newAccount)
	require.Equal(t, 1, q.Size())
	require.Equal(t, newAccount, q.byFee[0].Account)
}

func TestApplyReplacementPreservesAccountPenalties(t *testing.T) {
	tests := []struct {
		name       string
		txInLedger uint32
		didApply   bool
		wantResult ter.Result
		wantQueued bool
		wantSize   int
	}{
		{
			name:       "queued while full",
			txInLedger: 4,
			wantResult: ter.TerQUEUED,
			wantQueued: true,
			wantSize:   1,
		},
		{
			name:       "applied directly",
			didApply:   true,
			wantResult: ter.TesSUCCESS,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := makeAdmissionConfig()
			cfg.RetrySequencePercent = 25
			q := mustNew(cfg)
			acct := [20]byte{9}
			aq := NewAccountQueue(acct)
			aq.DropPenalty = true
			aq.RetryPenalty = true
			q.byAccount[acct] = aq
			original := addQueued(q, aq, 1, 2)
			q.setMaxSize(1)

			replacement := &seqTx{seq: 1, fee: "130"}
			ctx := &stubApplyCtx{
				seq:        1,
				balance:    1_000_000_000,
				exists:     true,
				baseFee:    10,
				txInLedger: test.txInLedger,
				applyRes:   ter.TesSUCCESS,
				applied:    test.didApply,
			}

			result := q.Apply(ctx, replacement, [32]byte{2}, acct)

			require.Equal(t, test.wantResult, result.Result)
			require.Equal(t, test.didApply, result.Applied)
			require.Equal(t, test.wantQueued, result.Queued)
			require.Equal(t, test.wantSize, q.Size())

			retained, exists := q.byAccount[acct]
			require.True(t, exists)
			require.Same(t, aq, retained)
			require.True(t, retained.DropPenalty)
			require.True(t, retained.RetryPenalty)
			if test.wantQueued {
				candidate, exists := retained.Transactions[NewSeqProxySequence(1)]
				require.True(t, exists)
				require.NotSame(t, original, candidate)
				require.Same(t, replacement, candidate.Txn)
			} else {
				require.True(t, retained.Empty())
			}
		})
	}
}

// TestApply_H2_ExpirationGap pins rippled TxQ.cpp:1031-1040: a tx that lands
// after an expiration gap in the account's queue must fill the FIRST hole.
// Account seq 5 with queued {5,6,9} (7,8 expired out) and a new seq 10 is
// telCAN_NOT_QUEUE because the first gap is 7 — not terQUEUED.
func TestApply_H2_ExpirationGap(t *testing.T) {
	q := mustNew(makeAdmissionConfig())
	acct := [20]byte{9}
	aq := NewAccountQueue(acct)
	q.byAccount[acct] = aq
	addQueued(q, aq, 5, 6)
	addQueued(q, aq, 6, 7)
	addQueued(q, aq, 9, 10)

	ctx := &stubApplyCtx{seq: 5, balance: 1_000_000_000, exists: true, baseFee: 10}
	res := q.Apply(ctx, &seqTx{seq: 10, fee: "10"}, [32]byte{0xAA}, acct)

	require.Equal(t, ter.TelCAN_NOT_QUEUE, res.Result)
	require.False(t, res.Queued)
}

// TestApply_H2_TicketCreateHole pins the same rule for a hole left by a
// TicketCreate. A TicketCreate at seq 5 reserving 3 sequences advances the
// chain to seq 8; a new seq 6 (inside the hole) is telCAN_NOT_QUEUE, where the
// pre-fix code returned tefPAST_SEQ.
func TestApply_H2_TicketCreateHole(t *testing.T) {
	q := mustNew(makeAdmissionConfig())
	acct := [20]byte{9}
	aq := NewAccountQueue(acct)
	q.byAccount[acct] = aq
	addQueued(q, aq, 5, 8) // followingSeq 8 == seq + ticketCount

	ctx := &stubApplyCtx{seq: 5, balance: 1_000_000_000, exists: true, baseFee: 10}
	res := q.Apply(ctx, &seqTx{seq: 6, fee: "10"}, [32]byte{0xBB}, acct)

	require.Equal(t, ter.TelCAN_NOT_QUEUE, res.Result)
	require.False(t, res.Queued)
}

// TestApply_H2_StalePredecessorGap pins that a gap-landing tx whose immediate
// queued predecessor is a STALE sequence (queued seq < acctSeq) is rejected
// with telCAN_NOT_QUEUE, matching rippled's after-entries branch
// (TxQ.cpp:1019-1041), NOT terPRE_SEQ. Account seq 5 with queued {3 (stale), 8}
// and a new seq 6: the predecessor is the stale seq 3, but front-of-queue is
// keyed only on seqProxy < prevSeqProxy, so this lands in the after-entries
// branch where getNextQueuableSeq is 5 != 6.
func TestApply_H2_StalePredecessorGap(t *testing.T) {
	q := mustNew(makeAdmissionConfig())
	acct := [20]byte{9}
	aq := NewAccountQueue(acct)
	q.byAccount[acct] = aq
	addQueued(q, aq, 3, 4) // stale: seq 3 < acctSeq 5
	addQueued(q, aq, 8, 9)

	ctx := &stubApplyCtx{seq: 5, balance: 1_000_000_000, exists: true, baseFee: 10}
	res := q.Apply(ctx, &seqTx{seq: 6, fee: "10"}, [32]byte{0x1A}, acct)

	require.Equal(t, ter.TelCAN_NOT_QUEUE, res.Result)
	require.False(t, res.Queued)
}

// TestApply_H1_PreflightRejects pins rippled TxQ.cpp:743-745: a submission that
// fails preflight is rejected with the preflight TER, never held as terQUEUED.
func TestApply_H1_PreflightRejects(t *testing.T) {
	q := mustNew(makeAdmissionConfig())
	acct := [20]byte{9}
	ctx := &stubApplyCtx{seq: 5, balance: 1_000_000_000, exists: true, baseFee: 10, preflight: ter.TemMALFORMED}

	res := q.Apply(ctx, &seqTx{seq: 5, fee: "10"}, [32]byte{0xCC}, acct)

	require.Equal(t, ter.TemMALFORMED, res.Result)
	require.False(t, res.Queued)
	require.False(t, res.Applied)
}

// TestApply_H1_PreclaimRejectsFirstQueued pins rippled TxQ.cpp:1167-1170: the
// FIRST queued tx for an account now runs preclaim, so a tx whose preclaim
// fails (e.g. terINSUF_FEE_B) is rejected instead of returning terQUEUED. A
// high txInLedger forces the escalated fee level above the paid level so the
// submission takes the queue path rather than direct apply.
func TestApply_H1_PreclaimRejectsFirstQueued(t *testing.T) {
	q := mustNew(makeAdmissionConfig())
	acct := [20]byte{9}
	ctx := &stubApplyCtx{
		seq: 5, balance: 1_000_000_000, exists: true, baseFee: 10,
		txInLedger: 100, preclaim: ter.TerINSUF_FEE_B,
	}

	res := q.Apply(ctx, &seqTx{seq: 5, fee: "10"}, [32]byte{0xDD}, acct)

	require.Equal(t, ter.TerINSUF_FEE_B, res.Result)
	require.False(t, res.Queued)
}

// TestApply_H1_PreclaimPassesQueues is the positive control: with preflight and
// preclaim passing, the same first-queued submission is held (terQUEUED).
func TestApply_H1_PreclaimPassesQueues(t *testing.T) {
	q := mustNew(makeAdmissionConfig())
	acct := [20]byte{9}
	ctx := &stubApplyCtx{
		seq: 5, balance: 1_000_000_000, exists: true, baseFee: 10, txInLedger: 100,
	}

	res := q.Apply(ctx, &seqTx{seq: 5, fee: "10"}, [32]byte{0xEE}, acct)

	require.Equal(t, ter.TerQUEUED, res.Result)
	require.True(t, res.Queued)
}

func TestApplyAccountRootReadErrorsAreFatal(t *testing.T) {
	tests := []struct {
		name string
		ctx  *stubApplyCtx
	}{
		{
			name: "sequence",
			ctx: &stubApplyCtx{
				sequenceErr: errors.New("sequence read failed"),
				baseFee:     10,
			},
		},
		{
			name: "balance",
			ctx: &stubApplyCtx{
				seq:        4,
				balanceErr: errors.New("balance read failed"),
				exists:     true,
				baseFee:    10,
			},
		},
		{
			name: "existence",
			ctx: &stubApplyCtx{
				seq:       4,
				existsErr: errors.New("existence read failed"),
				baseFee:   10,
			},
		},
		{
			name: "ticket existence",
			ctx: &stubApplyCtx{
				seq:        4,
				exists:     true,
				ticketErr:  errors.New("ticket read failed"),
				baseFee:    10,
				txInLedger: 100,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := mustNew(makeAdmissionConfig())
			transaction := &seqTx{seq: 5, fee: "10"}
			if tt.name == "ticket existence" {
				transaction.ticket = 5
			}
			res := q.Apply(tt.ctx, transaction, [32]byte{0xEF}, [20]byte{9})
			require.Equal(t, ter.TefINTERNAL, res.Result)
			require.False(t, res.Applied)
			require.False(t, res.Queued)
			require.Zero(t, q.Size())
		})
	}
}

func TestApplyFeeSponsoredTransactionCannotQueueButMayApplyDirectly(t *testing.T) {
	account := [20]byte{9}
	feeFlag := tx.SpfSponsorFee
	sponsor := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	t.Run("cannot queue", func(t *testing.T) {
		q := mustNew(makeAdmissionConfig())
		ctx := &stubApplyCtx{
			seq:        5,
			balance:    1_000_000_000,
			exists:     true,
			baseFee:    10,
			txInLedger: 100,
		}
		transaction := &seqTx{
			seq:          5,
			fee:          "10",
			sponsor:      sponsor,
			sponsorFlags: &feeFlag,
		}

		result := q.Apply(ctx, transaction, [32]byte{0xEF}, account)
		require.Equal(t, ter.TelCAN_NOT_QUEUE, result.Result)
		require.False(t, result.Applied)
		require.False(t, result.Queued)
		require.Zero(t, q.Size())
	})

	t.Run("may apply directly", func(t *testing.T) {
		q := mustNew(makeAdmissionConfig())
		ctx := &stubApplyCtx{
			seq:      5,
			balance:  1_000_000_000,
			exists:   true,
			baseFee:  10,
			applyRes: ter.TesSUCCESS,
			applied:  true,
		}
		transaction := &seqTx{
			seq:          5,
			fee:          "10",
			sponsor:      sponsor,
			sponsorFlags: &feeFlag,
		}

		result := q.Apply(ctx, transaction, [32]byte{0xF0}, account)
		require.Equal(t, ter.TesSUCCESS, result.Result)
		require.True(t, result.Applied)
		require.False(t, result.Queued)
	})

	t.Run("reserve only remains queueable", func(t *testing.T) {
		q := mustNew(makeAdmissionConfig())
		ctx := &stubApplyCtx{
			seq:        5,
			balance:    1_000_000_000,
			exists:     true,
			baseFee:    10,
			txInLedger: 100,
		}
		reserveFlag := tx.SpfSponsorReserve
		transaction := &seqTx{
			seq:          5,
			fee:          "10",
			sponsor:      sponsor,
			sponsorFlags: &reserveFlag,
		}

		result := q.Apply(ctx, transaction, [32]byte{0xF1}, account)
		require.Equal(t, ter.TerQUEUED, result.Result)
		require.True(t, result.Queued)
	})
}

func TestApplyBatchCannotQueueButMayApplyDirectly(t *testing.T) {
	account := [20]byte{9}

	t.Run("cannot queue", func(t *testing.T) {
		q := mustNew(makeAdmissionConfig())
		ctx := &stubApplyCtx{
			seq:        5,
			balance:    1_000_000_000,
			exists:     true,
			baseFee:    10,
			txInLedger: 100,
		}

		result := q.Apply(ctx, &batchSeqTx{seqTx: &seqTx{seq: 5, fee: "10"}}, [32]byte{0xF2}, account)
		require.Equal(t, ter.TelCAN_NOT_QUEUE, result.Result)
		require.False(t, result.Applied)
		require.False(t, result.Queued)
		require.Zero(t, q.Size())
	})

	t.Run("may apply directly", func(t *testing.T) {
		q := mustNew(makeAdmissionConfig())
		ctx := &stubApplyCtx{
			seq:      5,
			balance:  1_000_000_000,
			exists:   true,
			baseFee:  10,
			applyRes: ter.TesSUCCESS,
			applied:  true,
		}

		result := q.Apply(ctx, &batchSeqTx{seqTx: &seqTx{seq: 5, fee: "10"}}, [32]byte{0xF3}, account)
		require.Equal(t, ter.TesSUCCESS, result.Result)
		require.True(t, result.Applied)
		require.False(t, result.Queued)
	})
}

// TestApply_BadFeeRejected pins that a malformed Fee string is rejected with
// temBAD_FEE rather than being silently treated as fee level 0.
func TestApply_BadFeeRejected(t *testing.T) {
	q := mustNew(makeAdmissionConfig())
	acct := [20]byte{9}
	ctx := &stubApplyCtx{seq: 5, balance: 1_000_000_000, exists: true, baseFee: 10}

	res := q.Apply(ctx, &seqTx{seq: 5, fee: "not-a-number"}, [32]byte{0xFF}, acct)

	require.Equal(t, ter.TemBAD_FEE, res.Result)
	require.False(t, res.Queued)
}

func mkCandidate(acct [20]byte, sp SeqProxy, feeLevel FeeLevel) *Candidate {
	return &Candidate{
		Txn:              &seqTx{seq: sp.Value, fee: "10"},
		Account:          acct,
		FeeLevel:         feeLevel,
		SeqProxy:         sp,
		RetriesRemaining: RetriesAllowed,
		Consequences:     TxConsequences{Fee: 10, FollowingSeq: NewSeqProxySequence(sp.Value + 1)},
	}
}

// TestDropLastForAccount_DropsHighestSeqProxyInclTickets pins rippled
// TxQ.cpp:1541: the drop penalty removes the account's highest-SeqProxy entry,
// which is a ticket when one is queued after the sequences (tickets sort after
// sequences). The pre-fix code only ever considered sequence-based entries.
func TestDropLastForAccount_DropsHighestSeqProxyInclTickets(t *testing.T) {
	q := mustNew(DefaultConfig())
	acct := [20]byte{1}
	aq := NewAccountQueue(acct)
	q.byAccount[acct] = aq

	seqC := mkCandidate(acct, NewSeqProxySequence(2), FeeLevel(BaseLevel*2))
	ticketC := mkCandidate(acct, NewSeqProxyTicket(5), FeeLevel(BaseLevel))
	aq.Add(seqC)
	aq.Add(ticketC)
	q.insertByFee(seqC)
	q.insertByFee(ticketC)

	idx := 0 // processing the seq candidate
	q.dropLastForAccount(aq, seqC, &idx)

	_, ticketGone := aq.Transactions[NewSeqProxyTicket(5)]
	require.False(t, ticketGone, "the ticket is the highest SeqProxy and must be dropped")
	_, seqStays := aq.Transactions[NewSeqProxySequence(2)]
	require.True(t, seqStays, "the current candidate must not be dropped")
}

// TestDropLastForAccount_NeverDropsCurrent pins rippled's
// `if (endIter != candidateIter)` guard (TxQ.cpp:1552-1554): when the current
// candidate is itself the highest-SeqProxy entry, nothing is dropped.
func TestDropLastForAccount_NeverDropsCurrent(t *testing.T) {
	q := mustNew(DefaultConfig())
	acct := [20]byte{1}
	aq := NewAccountQueue(acct)
	q.byAccount[acct] = aq

	low := mkCandidate(acct, NewSeqProxySequence(2), FeeLevel(BaseLevel))
	high := mkCandidate(acct, NewSeqProxySequence(7), FeeLevel(BaseLevel*2))
	aq.Add(low)
	aq.Add(high)
	q.insertByFee(low)
	q.insertByFee(high)

	idx := 0
	q.dropLastForAccount(aq, high, &idx) // current IS the highest

	require.Equal(t, 2, aq.Count(), "nothing should be dropped when current is the last entry")
}

// TestDropLastForAccount_AdjustsIndexWhenDropPrecedes pins the byFee index
// fixup: dropping an element that sits before the current candidate shifts the
// current one down, so idx is decremented to keep the caller's i++ aligned.
func TestDropLastForAccount_AdjustsIndexWhenDropPrecedes(t *testing.T) {
	q := mustNew(DefaultConfig())
	acct := [20]byte{1}
	aq := NewAccountQueue(acct)
	q.byAccount[acct] = aq

	// high fee → byFee index 0, low fee → byFee index 1.
	high := mkCandidate(acct, NewSeqProxySequence(7), FeeLevel(BaseLevel*2))
	low := mkCandidate(acct, NewSeqProxySequence(2), FeeLevel(BaseLevel))
	aq.Add(high)
	aq.Add(low)
	q.insertByFee(high)
	q.insertByFee(low)

	idx := 1 // processing the low-fee seq-2 candidate (drop target seq-7 is at index 0)
	q.dropLastForAccount(aq, low, &idx)

	require.Equal(t, 0, idx, "idx must shift down after dropping an earlier byFee element")
	require.Equal(t, low, q.byFee[idx], "idx must still point at the current candidate")
}
