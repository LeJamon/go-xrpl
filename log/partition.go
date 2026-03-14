package log

// Partition name constants for goXRPL subsystems.
// Names are kept identical to rippled's log partitions where a direct
// equivalent exists, so operators familiar with rippled can map them directly.
const (
	// PartitionServer covers server startup, shutdown, and port binding.
	PartitionServer = "Server"

	// PartitionLedger covers ledger management and genesis initialization.
	// Equivalent to rippled's "Ledger" and "LedgerMaster" partitions.
	PartitionLedger = "Ledger"

	// PartitionConsensus covers the consensus protocol.
	// Equivalent to rippled's "LedgerConsensus" partition.
	PartitionConsensus = "LedgerConsensus"

	// PartitionTxQ covers transaction queue operations.
	// Equivalent to rippled's "TxQ" partition.
	PartitionTxQ = "TxQ"

	// PartitionTx covers the transaction engine: apply entry/exit and TER results.
	// Use Debug for apply entry/exit; Trace for deep internals.
	PartitionTx = "Tx"

	// PartitionView covers ledger view state changes (SLE insert/update/erase).
	// Equivalent to rippled's "View" partition.
	PartitionView = "View"

	// PartitionFlow covers payment flow and RippleCalc steps.
	// Equivalent to rippled's "Flow" partition.
	PartitionFlow = "Flow"

	// PartitionPathfinder covers path finding operations.
	// Equivalent to rippled's "Pathfinder" partition.
	PartitionPathfinder = "Pathfinder"

	// PartitionAmendments covers amendment/feature processing.
	// Equivalent to rippled's "Amendments" partition.
	PartitionAmendments = "Amendments"

	// PartitionPeer covers peer networking (connect/disconnect/messages).
	// Equivalent to rippled's "Peer" and "Overlay" partitions.
	PartitionPeer = "Peer"

	// PartitionRPC covers the JSON-RPC and WebSocket servers.
	// Equivalent to rippled's "RPC" and "RPCHandler" partitions.
	PartitionRPC = "RPC"

	// PartitionNodeStore covers the node store (key-value persistence).
	// Equivalent to rippled's "NodeStore" partition.
	PartitionNodeStore = "NodeStore"
)
