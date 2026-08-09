package state

import (
	"fmt"
	"strings"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	ledgerfields "github.com/LeJamon/go-xrpl/ledger/entry"
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
	Sponsor         string
	HasSponsor      bool

	// Sequence records the creating tx/ticket sequence (a keylet input),
	// stored once fixIncludeKeyletFields is active.
	Sequence    uint32
	HasSequence bool

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

	entry := &ledgerfields.PayChannel{}
	entry.SetAccount(ownerAddress)
	entry.SetDestination(destAddress)
	entry.SetAmount(fmt.Sprintf("%d", channel.Amount))
	entry.SetBalance(fmt.Sprintf("%d", channel.Balance))
	entry.SetSettleDelay(channel.SettleDelay)
	entry.SetOwnerNode(fmt.Sprintf("%x", channel.OwnerNode))
	entry.SetFlags(0)
	entry.SetPublicKey(channel.PublicKey)
	if channel.HasSponsor || channel.Sponsor != "" {
		entry.SetSponsor(channel.Sponsor)
	}

	if channel.CancelAfter > 0 {
		entry.SetCancelAfter(channel.CancelAfter)
	}
	if channel.Expiration > 0 {
		entry.SetExpiration(channel.Expiration)
	}
	if channel.HasSourceTag {
		entry.SetSourceTag(channel.SourceTag)
	}
	if channel.HasDestTag {
		entry.SetDestinationTag(channel.DestinationTag)
	}
	if channel.HasDestNode {
		entry.SetDestinationNode(fmt.Sprintf("%x", channel.DestinationNode))
	}
	if channel.HasSequence {
		entry.SetSequence(channel.Sequence)
	}
	if channel.PreviousTxnID != ([32]byte{}) {
		entry.SetPreviousTxnID(fmt.Sprintf("%X", channel.PreviousTxnID[:]))
		entry.SetPreviousTxnLgrSeq(channel.PreviousTxnLgrSeq)
	}

	return entry.Encode()
}

// ParsePayChannel parses a PayChannel ledger entry from binary data
func ParsePayChannel(data []byte) (*PayChannelData, error) {
	entry := &ledgerfields.PayChannel{}
	if err := entry.Decode(data); err != nil {
		return nil, err
	}
	fields := entry.ToMap()
	channel := &PayChannelData{
		SettleDelay:       entry.SettleDelay,
		PublicKey:         strings.ToLower(entry.PublicKey),
		Expiration:        entry.Expiration,
		CancelAfter:       entry.CancelAfter,
		SourceTag:         entry.SourceTag,
		DestinationTag:    entry.DestinationTag,
		HasSourceTag:      fields["SourceTag"] != nil,
		HasDestTag:        fields["DestinationTag"] != nil,
		HasDestNode:       fields["DestinationNode"] != nil,
		Sponsor:           entry.Sponsor,
		HasSponsor:        fields["Sponsor"] != nil,
		Sequence:          entry.Sequence,
		HasSequence:       fields["Sequence"] != nil,
		PreviousTxnLgrSeq: entry.PreviousTxnLgrSeq,
	}

	var err error
	if fields["Account"] != nil {
		channel.Account, err = decodeLedgerAccount("PayChannel.Account", entry.Account)
		if err != nil {
			return nil, err
		}
	}
	if fields["Destination"] != nil {
		channel.DestinationID, err = decodeLedgerAccount("PayChannel.Destination", entry.Destination)
		if err != nil {
			return nil, err
		}
	}
	if fields["OwnerNode"] != nil {
		channel.OwnerNode, err = parseLedgerUint64("PayChannel.OwnerNode", entry.OwnerNode)
		if err != nil {
			return nil, err
		}
	}
	if channel.HasDestNode {
		channel.DestinationNode, err = parseLedgerUint64("PayChannel.DestinationNode", entry.DestinationNode)
		if err != nil {
			return nil, err
		}
	}
	if fields["PreviousTxnID"] != nil {
		if err := decodeLedgerHex("PayChannel.PreviousTxnID", entry.PreviousTxnID, channel.PreviousTxnID[:]); err != nil {
			return nil, err
		}
	}
	if fields["Amount"] != nil {
		amount, err := decodeLedgerAmount("PayChannel.Amount", entry.Amount)
		if err != nil {
			return nil, err
		}
		channel.Amount, err = nonNegativeNativeDrops("PayChannel.Amount", amount)
		if err != nil {
			return nil, err
		}
	}
	if fields["Balance"] != nil {
		balance, err := decodeLedgerAmount("PayChannel.Balance", entry.Balance)
		if err != nil {
			return nil, err
		}
		channel.Balance, err = nonNegativeNativeDrops("PayChannel.Balance", balance)
		if err != nil {
			return nil, err
		}
	}

	return channel, nil
}
