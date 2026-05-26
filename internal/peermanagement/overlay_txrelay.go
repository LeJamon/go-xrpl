package peermanagement

// TxRelayMetrics is a snapshot of the transaction reduce-relay counters
// surfaced by the tx_reduce_relay RPC. Values are cumulative since startup.
type TxRelayMetrics struct {
	TransactionsRelayed      uint64
	TransactionsRelayedBytes uint64
	HaveTransactionsSent     uint64
	HaveTransactionsReceived uint64
	TransactionsDropped      uint64
}

// CountTransactionRelayed records that a TMTransaction frame of the given wire
// size was forwarded to peers. Called from the relay path after a successful
// broadcast.
func (o *Overlay) CountTransactionRelayed(frameBytes int) {
	o.txRelayed.Add(1)
	if frameBytes > 0 {
		o.txRelayBytes.Add(uint64(frameBytes))
	}
}

// TxRelayStats returns a snapshot of the transaction reduce-relay counters.
func (o *Overlay) TxRelayStats() TxRelayMetrics {
	return TxRelayMetrics{
		TransactionsRelayed:      o.txRelayed.Load(),
		TransactionsRelayedBytes: o.txRelayBytes.Load(),
		HaveTransactionsSent:     o.haveTxSent.Load(),
		HaveTransactionsReceived: o.haveTxReceived.Load(),
		TransactionsDropped:      o.droppedTransactions.Load(),
	}
}
