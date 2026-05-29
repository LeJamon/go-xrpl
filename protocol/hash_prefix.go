package protocol

// HashPrefix defines the prefix bytes used in XRPL hashing operations.
// These prefixes provide domain separation for different hash contexts.
type HashPrefix [4]byte

var (
	HashPrefixLedgerMaster        = HashPrefix{'L', 'W', 'R', 0x00}
	HashPrefixInnerNode           = HashPrefix{'M', 'I', 'N', 0x00}
	HashPrefixLeafNode            = HashPrefix{'M', 'L', 'N', 0x00}
	HashPrefixTxNode              = HashPrefix{'S', 'N', 'D', 0x00}
	HashPrefixAccountStateEntry   = HashPrefix{'M', 'L', 'N', 0x00}
	HashPrefixTxSign              = HashPrefix{'S', 'T', 'X', 0x00}
	HashPrefixTxMultiSign         = HashPrefix{'S', 'M', 'T', 0x00}
	HashPrefixTransactionID       = HashPrefix{'T', 'X', 'N', 0x00}
	HashPrefixValidation          = HashPrefix{'V', 'A', 'L', 0x00}
	HashPrefixProposal            = HashPrefix{'P', 'R', 'P', 0x00}
	HashPrefixManifest            = HashPrefix{'M', 'A', 'N', 0x00}
	HashPrefixPaymentChannelClaim = HashPrefix{'C', 'L', 'M', 0x00}
	HashPrefixCredential          = HashPrefix{'C', 'R', 'D', 0x00}
	HashPrefixBatch               = HashPrefix{'B', 'C', 'H', 0x00}
)

// Bytes returns the prefix as a byte slice.
func (h HashPrefix) Bytes() []byte {
	return h[:]
}
