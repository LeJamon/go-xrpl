package txq

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// seqTx is a minimal sequence-based tx.Transaction for driving TxQ.Apply.
type seqTx struct {
	seq uint32
	fee string
}

func (m *seqTx) TxType() tx.Type { return tx.TypeAccountSet }
func (m *seqTx) GetCommon() *tx.Common {
	s := m.seq
	return &tx.Common{Account: "rTest", Sequence: &s, Fee: m.fee}
}
func (m *seqTx) Validate() error                  { return nil }
func (m *seqTx) Flatten() (map[string]any, error) { return map[string]any{}, nil }
func (m *seqTx) GetRawBytes() []byte              { return []byte{byte(m.seq)} }
func (m *seqTx) SetRawBytes([]byte)               {}
func (m *seqTx) RequiredAmendments() [][32]byte   { return nil }

// stubApplyCtx is a configurable txq.ApplyContext for admission tests. The
// preflight/preclaim/apply results are dialled in per test so we can pin which
// admission path rejects (or queues) a submission.
type stubApplyCtx struct {
	seq        uint32
	balance    uint64
	reserve    uint64
	exists     bool
	tickets    map[uint32]bool
	baseFee    uint64
	txInLedger uint32
	ledgerSeq  uint32
	flags      tx.ApplyFlags

	preflight ter.Result
	preclaim  ter.Result
	applyRes  ter.Result
	applied   bool
}

func (c *stubApplyCtx) GetAccountSequence([20]byte) uint32             { return c.seq }
func (c *stubApplyCtx) AccountExists([20]byte) bool                    { return c.exists }
func (c *stubApplyCtx) TicketExists(_ [20]byte, t uint32) bool         { return c.tickets[t] }
func (c *stubApplyCtx) GetAccountBalance([20]byte) uint64              { return c.balance }
func (c *stubApplyCtx) GetAccountReserve(uint32) uint64                { return c.reserve }
func (c *stubApplyCtx) GetBaseFee(tx.Transaction) uint64               { return c.baseFee }
func (c *stubApplyCtx) GetTxInLedger() uint32                          { return c.txInLedger }
func (c *stubApplyCtx) GetLedgerSequence() uint32                      { return c.ledgerSeq }
func (c *stubApplyCtx) GetApplyFlags() tx.ApplyFlags                   { return c.flags }
func (c *stubApplyCtx) PreflightTransaction(tx.Transaction) ter.Result { return c.preflight }
func (c *stubApplyCtx) PreclaimTransaction(tx.Transaction, [20]byte, uint64, uint32) ter.Result {
	return c.preclaim
}
func (c *stubApplyCtx) ApplyTransaction(tx.Transaction) (ter.Result, bool) {
	return c.applyRes, c.applied
}
func (c *stubApplyCtx) NewSandbox() (SandboxContext, error) {
	return nil, errors.New("stubApplyCtx: no sandbox")
}

// addQueued appends a sequence-based candidate to the account queue with an
// explicit followingSeq, so getNextQueuableSeq can walk the chain.
func addQueued(q *TxQ, aq *AccountQueue, seq, followingSeq uint32) {
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
}

// TestApply_H2_ExpirationGap pins rippled TxQ.cpp:1031-1040: a tx that lands
// after an expiration gap in the account's queue must fill the FIRST hole.
// Account seq 5 with queued {5,6,9} (7,8 expired out) and a new seq 10 is
// telCAN_NOT_QUEUE because the first gap is 7 — not terQUEUED.
func TestApply_H2_ExpirationGap(t *testing.T) {
	q := New(makeAdmissionConfig())
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
	q := New(makeAdmissionConfig())
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
	q := New(makeAdmissionConfig())
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
	q := New(makeAdmissionConfig())
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
	q := New(makeAdmissionConfig())
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
	q := New(makeAdmissionConfig())
	acct := [20]byte{9}
	ctx := &stubApplyCtx{
		seq: 5, balance: 1_000_000_000, exists: true, baseFee: 10, txInLedger: 100,
	}

	res := q.Apply(ctx, &seqTx{seq: 5, fee: "10"}, [32]byte{0xEE}, acct)

	require.Equal(t, ter.TerQUEUED, res.Result)
	require.True(t, res.Queued)
}

// TestApply_BadFeeRejected pins that a malformed Fee string is rejected with
// temBAD_FEE rather than being silently treated as fee level 0.
func TestApply_BadFeeRejected(t *testing.T) {
	q := New(makeAdmissionConfig())
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
	q := New(DefaultConfig())
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
	q := New(DefaultConfig())
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
	q := New(DefaultConfig())
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
