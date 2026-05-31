package protocol

// Wire types identify the kind of node carried in a SHAMap wire-format payload.
const (
	WireTypeTransaction = iota
	WireTypeAccountState
	WireTypeInner
	WireTypeCompressedInner
	WireTypeTransactionWithMeta
)
