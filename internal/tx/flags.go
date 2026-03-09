package tx

// Universal transaction flag constants matching rippled TxFlags.h.
// These are the flags allowed on ALL transaction types.
const (
	// TfFullyCanonicalSig indicates the signature is fully canonical
	TfFullyCanonicalSig uint32 = 0x80000000

	// TfInnerBatchTxn indicates this is an inner batch transaction
	TfInnerBatchTxn uint32 = 0x40000000

	// TfUniversal is the combination of flags allowed on all transactions
	TfUniversal uint32 = TfFullyCanonicalSig | TfInnerBatchTxn

	// TfUniversalMask is used to check for invalid flags (any bit not in TfUniversal)
	TfUniversalMask uint32 = ^TfUniversal
)
