package txq

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestUpperBoundSeqProxy_PicksSmallestStrictlyGreater pins the
// std::map::upper_bound contract on the SeqProxy ordering: with
// queued seq 9, 11, 13 and a probe at seq 10, the helper must return
// seq 11 (the smallest strictly greater).
func TestUpperBoundSeqProxy_PicksSmallestStrictlyGreater(t *testing.T) {
	aq := NewAccountQueue([20]byte{1})
	for _, s := range []uint32{9, 11, 13} {
		aq.Transactions[NewSeqProxySequence(s)] = &Candidate{
			SeqProxy: NewSeqProxySequence(s),
		}
	}
	q := New(makeAdmissionConfig())
	next, ok := q.upperBoundSeqProxy(aq, NewSeqProxySequence(10))
	require.True(t, ok)
	require.Equal(t, NewSeqProxySequence(11), next)
}

// TestUpperBoundSeqProxy_RejectsTicketAsNeighbour pins
// TxQ.cpp:440-444: when the only tx greater than the probe is a
// ticket, rippled's `nextTxIter->first.isSeq()` check rejects the gap
// fill. upperBoundSeqProxy returns the ticket so canBeHeld can reject.
func TestUpperBoundSeqProxy_RejectsTicketAsNeighbour(t *testing.T) {
	aq := NewAccountQueue([20]byte{1})
	ticket := SeqProxy{Value: 12, IsTicket: true}
	aq.Transactions[ticket] = &Candidate{SeqProxy: ticket}
	q := New(makeAdmissionConfig())
	next, ok := q.upperBoundSeqProxy(aq, NewSeqProxySequence(9))
	require.True(t, ok)
	require.True(t, next.IsTicket, "ticket dominates the upper-bound when no later seq exists")
}

// TestUpperBoundSeqProxy_PrefersSeqBeforeTicket pins the SeqProxy
// ordering rule from SeqProxy.h:58 (`enum Type : uint8_t { seq=0,
// ticket=1 }`) — sequences sort BEFORE tickets globally. With seq 11
// and ticket 5 both queued, the upper bound of seq 10 is seq 11
// (the ticket is "greater" by value but `seq < ticket` per the type
// ordering, so the smallest strictly greater is the seq).
func TestUpperBoundSeqProxy_PrefersSeqBeforeTicket(t *testing.T) {
	aq := NewAccountQueue([20]byte{1})
	aq.Transactions[NewSeqProxySequence(11)] = &Candidate{SeqProxy: NewSeqProxySequence(11)}
	ticket := SeqProxy{Value: 5, IsTicket: true}
	aq.Transactions[ticket] = &Candidate{SeqProxy: ticket}
	q := New(makeAdmissionConfig())
	next, ok := q.upperBoundSeqProxy(aq, NewSeqProxySequence(10))
	require.True(t, ok)
	require.False(t, next.IsTicket, "seq 11 outranks ticket 5 under SeqProxy ordering")
	require.Equal(t, uint32(11), next.Value)
}

// TestUpperBoundSeqProxy_EmptyQueueReturnsFalse pins the
// nextTxIter == end() branch.
func TestUpperBoundSeqProxy_EmptyQueueReturnsFalse(t *testing.T) {
	aq := NewAccountQueue([20]byte{1})
	q := New(makeAdmissionConfig())
	_, ok := q.upperBoundSeqProxy(aq, NewSeqProxySequence(0))
	require.False(t, ok)
}

func makeAdmissionConfig() Config {
	return Config{
		LedgersInQueue:                 20,
		MinimumTxnInLedger:             3,
		TargetTxnInLedger:              5,
		MaximumTxnInLedger:             10,
		MinimumEscalationMultiplier:    128000,
		MinimumLastLedgerBuffer:        2,
		QueueSizeMin:                   10,
		MaximumTxnPerAccount:           10,
		MinimumTxnInLedgerStandalone:   100,
		NormalConsensusIncreasePercent: 20,
		SlowConsensusDecreasePercent:   50,
		Standalone:                     false,
	}
}
