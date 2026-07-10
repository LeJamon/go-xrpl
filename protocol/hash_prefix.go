package protocol

// HashPrefix defines the prefix bytes used in XRPL hashing operations.
// These prefixes provide domain separation for different hash contexts.
type HashPrefix [4]byte

// Hash prefixes provide domain separation for the XRPL protocol's hashing
// contexts: each is the four-byte tag prepended to the payload before hashing,
// mirroring rippled's HashPrefix values. They are unexported so the shared
// process-wide values cannot be mutated; the exported accessors below return
// copies by value.
var (
	hashPrefixLedgerMaster        = HashPrefix{'L', 'W', 'R', 0x00}
	hashPrefixInnerNode           = HashPrefix{'M', 'I', 'N', 0x00}
	hashPrefixLeafNode            = HashPrefix{'M', 'L', 'N', 0x00}
	hashPrefixTxNode              = HashPrefix{'S', 'N', 'D', 0x00}
	hashPrefixTxSign              = HashPrefix{'S', 'T', 'X', 0x00}
	hashPrefixTxMultiSign         = HashPrefix{'S', 'M', 'T', 0x00}
	hashPrefixTransactionID       = HashPrefix{'T', 'X', 'N', 0x00}
	hashPrefixValidation          = HashPrefix{'V', 'A', 'L', 0x00}
	hashPrefixProposal            = HashPrefix{'P', 'R', 'P', 0x00}
	hashPrefixManifest            = HashPrefix{'M', 'A', 'N', 0x00}
	hashPrefixPaymentChannelClaim = HashPrefix{'C', 'L', 'M', 0x00}
	hashPrefixCredential          = HashPrefix{'C', 'R', 'D', 0x00}
	hashPrefixBatch               = HashPrefix{'B', 'C', 'H', 0x00}
)

// HashPrefixLedgerMaster returns the "LWR" ledger-header hash prefix.
func HashPrefixLedgerMaster() HashPrefix { return hashPrefixLedgerMaster }

// HashPrefixInnerNode returns the "MIN" SHAMap inner-node hash prefix.
func HashPrefixInnerNode() HashPrefix { return hashPrefixInnerNode }

// HashPrefixLeafNode returns the "MLN" account-state leaf-node hash prefix.
func HashPrefixLeafNode() HashPrefix { return hashPrefixLeafNode }

// HashPrefixTxNode returns the "SND" transaction-node hash prefix.
func HashPrefixTxNode() HashPrefix { return hashPrefixTxNode }

// HashPrefixTxSign returns the "STX" single-signature signing hash prefix.
func HashPrefixTxSign() HashPrefix { return hashPrefixTxSign }

// HashPrefixTxMultiSign returns the "SMT" multi-signature signing hash prefix.
func HashPrefixTxMultiSign() HashPrefix { return hashPrefixTxMultiSign }

// HashPrefixTransactionID returns the "TXN" transaction-ID hash prefix.
func HashPrefixTransactionID() HashPrefix { return hashPrefixTransactionID }

// HashPrefixValidation returns the "VAL" validation signing hash prefix.
func HashPrefixValidation() HashPrefix { return hashPrefixValidation }

// HashPrefixProposal returns the "PRP" proposal signing hash prefix.
func HashPrefixProposal() HashPrefix { return hashPrefixProposal }

// HashPrefixManifest returns the "MAN" manifest hash prefix.
func HashPrefixManifest() HashPrefix { return hashPrefixManifest }

// HashPrefixPaymentChannelClaim returns the "CLM" payment-channel-claim hash prefix.
func HashPrefixPaymentChannelClaim() HashPrefix { return hashPrefixPaymentChannelClaim }

// HashPrefixCredential returns the "CRD" credential signature hash prefix.
func HashPrefixCredential() HashPrefix { return hashPrefixCredential }

// HashPrefixBatch returns the "BCH" batch transaction hash prefix.
func HashPrefixBatch() HashPrefix { return hashPrefixBatch }

// Bytes returns the prefix as a byte slice.
func (h HashPrefix) Bytes() []byte {
	return h[:]
}
