package log

// Partition name constants for go-xrpl subsystems.
// Names are kept identical to rippled's log partitions where a direct
// equivalent exists, so operators familiar with rippled can map them directly.
const (
	// PartitionServer covers server startup, shutdown, and port binding.
	PartitionServer = "Server"

	// PartitionLedger covers ledger management and genesis initialization.
	// Equivalent to rippled's "Ledger" and "LedgerMaster" partitions.
	PartitionLedger = "Ledger"

	// PartitionTx covers the transaction engine: apply entry/exit and TER results.
	// Use Debug for apply entry/exit; Trace for deep internals.
	PartitionTx = "Tx"

	// PartitionRPC covers the JSON-RPC and WebSocket servers.
	// Equivalent to rippled's "RPC" and "RPCHandler" partitions.
	PartitionRPC = "RPC"
)
