package protocol

// WireType identifies the kind of node carried in a SHAMap wire-format payload.
// It is the trailing byte of the serialized node.
type WireType uint8

// SHAMap wire-format node types.
const (
	WireTypeTransaction WireType = iota
	WireTypeAccountState
	WireTypeInner
	WireTypeCompressedInner
	WireTypeTransactionWithMeta
)

// String returns the wire type's name for logging and diagnostics.
func (w WireType) String() string {
	switch w {
	case WireTypeTransaction:
		return "Transaction"
	case WireTypeAccountState:
		return "AccountState"
	case WireTypeInner:
		return "Inner"
	case WireTypeCompressedInner:
		return "CompressedInner"
	case WireTypeTransactionWithMeta:
		return "TransactionWithMeta"
	default:
		return "Unknown"
	}
}
