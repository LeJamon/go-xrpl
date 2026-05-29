package protocol_test

import (
	"encoding/binary"
	"testing"

	"github.com/LeJamon/goXRPLd/crypto"
	"github.com/LeJamon/goXRPLd/protocol"
)

// TestHashPrefixSyncWithCrypto guards against the two hash-prefix mirrors
// drifting apart. The protocol package exposes prefixes as [4]byte while
// crypto encodes the same domain-separation values as big-endian uint32;
// every shared prefix must agree byte-for-byte.
func TestHashPrefixSyncWithCrypto(t *testing.T) {
	cases := []struct {
		name     string
		protocol protocol.HashPrefix
		crypto   crypto.HashPrefix
	}{
		{"LedgerMaster", protocol.HashPrefixLedgerMaster, crypto.HashPrefixLedgerMaster},
		{"InnerNode", protocol.HashPrefixInnerNode, crypto.HashPrefixInnerNode},
		{"LeafNode", protocol.HashPrefixLeafNode, crypto.HashPrefixLeafNode},
		{"TxNode", protocol.HashPrefixTxNode, crypto.HashPrefixTxNode},
		{"TxSign", protocol.HashPrefixTxSign, crypto.HashPrefixTxSign},
		{"TxMultiSign", protocol.HashPrefixTxMultiSign, crypto.HashPrefixTxMultiSign},
		{"TransactionID", protocol.HashPrefixTransactionID, crypto.HashPrefixTransactionID},
		{"Validation", protocol.HashPrefixValidation, crypto.HashPrefixValidation},
		{"Proposal", protocol.HashPrefixProposal, crypto.HashPrefixProposal},
		{"Manifest", protocol.HashPrefixManifest, crypto.HashPrefixManifest},
		{"PaymentChannelClaim", protocol.HashPrefixPaymentChannelClaim, crypto.HashPrefixPaymentChannelClaim},
		{"Credential", protocol.HashPrefixCredential, crypto.HashPrefixCredential},
		{"Batch", protocol.HashPrefixBatch, crypto.HashPrefixBatch},
	}

	for _, tc := range cases {
		var want [4]byte
		binary.BigEndian.PutUint32(want[:], uint32(tc.crypto))
		if protocol.HashPrefix(want) != tc.protocol {
			t.Errorf("%s prefix mismatch: protocol=%v crypto=%#08x", tc.name, tc.protocol, uint32(tc.crypto))
		}
	}
}
