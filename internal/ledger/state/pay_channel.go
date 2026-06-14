package state

import (
	"encoding/hex"
	"fmt"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
)

// PayChannelData represents a PayChannel ledger entry
type PayChannelData struct {
	Account         [20]byte
	DestinationID   [20]byte
	Amount          uint64
	Balance         uint64
	SettleDelay     uint32
	PublicKey       string
	Expiration      uint32
	CancelAfter     uint32
	SourceTag       uint32
	DestinationTag  uint32
	HasSourceTag    bool
	HasDestTag      bool
	OwnerNode       uint64
	DestinationNode uint64
	HasDestNode     bool

	// Transaction threading fields. PayChannel is an unconditionally threaded
	// type, so these must survive a parse→serialize round-trip. Dropping them
	// makes a write-back of unchanged logical state differ from the original
	// bytes only in the threading fields, defeating the engine's
	// bytes.Equal(Original, Current) no-op-modify drop
	// (ApplyStateTable.cpp:156-157) and producing a ghost ModifiedNode whose
	// PreviousTxnID is then bumped — a tx + state fork. Mirrors the
	// DirectoryNode fix in this package.
	PreviousTxnID     [32]byte
	PreviousTxnLgrSeq uint32
}

// SerializePayChannelFromData serializes a PayChannel ledger entry from data
func SerializePayChannelFromData(channel *PayChannelData) ([]byte, error) {
	ownerAddress, err := addresscodec.EncodeAccountIDToClassicAddress(channel.Account[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode owner address: %w", err)
	}

	destAddress, err := addresscodec.EncodeAccountIDToClassicAddress(channel.DestinationID[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode destination address: %w", err)
	}

	jsonObj := map[string]any{
		"LedgerEntryType": "PayChannel",
		"Account":         ownerAddress,
		"Destination":     destAddress,
		"Amount":          fmt.Sprintf("%d", channel.Amount),
		"Balance":         fmt.Sprintf("%d", channel.Balance),
		"SettleDelay":     channel.SettleDelay,
		"OwnerNode":       fmt.Sprintf("%x", channel.OwnerNode),
		"Flags":           uint32(0),
	}

	if channel.PublicKey != "" {
		jsonObj["PublicKey"] = channel.PublicKey
	}
	if channel.CancelAfter > 0 {
		jsonObj["CancelAfter"] = channel.CancelAfter
	}
	if channel.Expiration > 0 {
		jsonObj["Expiration"] = channel.Expiration
	}
	if channel.HasSourceTag {
		jsonObj["SourceTag"] = channel.SourceTag
	}
	if channel.HasDestTag {
		jsonObj["DestinationTag"] = channel.DestinationTag
	}
	if channel.HasDestNode {
		jsonObj["DestinationNode"] = fmt.Sprintf("%x", channel.DestinationNode)
	}
	// Preserve threading fields across the round-trip. PreviousTxnLgrSeq is
	// only meaningful alongside PreviousTxnID, so gate both on the id.
	if channel.PreviousTxnID != ([32]byte{}) {
		jsonObj["PreviousTxnID"] = fmt.Sprintf("%X", channel.PreviousTxnID[:])
		jsonObj["PreviousTxnLgrSeq"] = channel.PreviousTxnLgrSeq
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode PayChannel: %w", err)
	}

	return hex.DecodeString(hexStr)
}

// ParsePayChannel parses a PayChannel ledger entry from binary data
func ParsePayChannel(data []byte) (*PayChannelData, error) {
	channel := &PayChannelData{}

	err := WalkFields(data, func(f Field) error {
		switch f.TypeCode {
		case stUInt32:
			switch f.FieldCode {
			case 39: // SettleDelay (nth=39)
				channel.SettleDelay = f.UInt32()
			case 36: // CancelAfter (nth=36)
				channel.CancelAfter = f.UInt32()
			case 10: // Expiration (nth=10)
				channel.Expiration = f.UInt32()
			case 3: // SourceTag
				channel.SourceTag = f.UInt32()
				channel.HasSourceTag = true
			case 14: // DestinationTag
				channel.DestinationTag = f.UInt32()
				channel.HasDestTag = true
			case 5: // PreviousTxnLgrSeq
				channel.PreviousTxnLgrSeq = f.UInt32()
			}

		case stUInt64:
			switch f.FieldCode {
			case 4: // OwnerNode (nth=4)
				channel.OwnerNode = f.UInt64()
			case 9: // DestinationNode (nth=9)
				channel.DestinationNode = f.UInt64()
				channel.HasDestNode = true
			}

		case stAmount:
			// PayChannel's Amount/Balance are native XRP (8 bytes).
			switch f.FieldCode {
			case 1: // Amount (nth=1)
				channel.Amount = xrpDrops(f.Value)
			case 2: // Balance (nth=2)
				channel.Balance = xrpDrops(f.Value)
			}

		case stAccountID:
			if id, ok := f.AccountID(); ok {
				switch f.FieldCode {
				case 1: // Account
					channel.Account = id
				case 3: // Destination
					channel.DestinationID = id
				}
			}

		case stHash256:
			if f.FieldCode == 5 { // PreviousTxnID
				channel.PreviousTxnID = f.Hash256()
			}

		case stBlob:
			if f.FieldCode == 1 { // PublicKey (Blob, nth=1)
				channel.PublicKey = hex.EncodeToString(f.VLBytes())
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return channel, nil
}
