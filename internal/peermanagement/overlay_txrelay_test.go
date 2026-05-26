package peermanagement

import "testing"

func TestOverlayTxRelayCounters(t *testing.T) {
	var o Overlay

	o.CountTransactionRelayed(100)
	o.CountTransactionRelayed(50)
	o.haveTxSent.Add(3)
	o.haveTxReceived.Add(4)
	o.droppedTransactions.Add(1)

	s := o.TxRelayStats()
	if s.TransactionsRelayed != 2 {
		t.Errorf("TransactionsRelayed = %d, want 2", s.TransactionsRelayed)
	}
	if s.TransactionsRelayedBytes != 150 {
		t.Errorf("TransactionsRelayedBytes = %d, want 150", s.TransactionsRelayedBytes)
	}
	if s.HaveTransactionsSent != 3 {
		t.Errorf("HaveTransactionsSent = %d, want 3", s.HaveTransactionsSent)
	}
	if s.HaveTransactionsReceived != 4 {
		t.Errorf("HaveTransactionsReceived = %d, want 4", s.HaveTransactionsReceived)
	}
	if s.TransactionsDropped != 1 {
		t.Errorf("TransactionsDropped = %d, want 1", s.TransactionsDropped)
	}
}
