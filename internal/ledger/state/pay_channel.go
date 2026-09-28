package state

import (
	"fmt"
	"strings"

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
	decoded           ledgerfields.PayChannel
}

// SerializePayChannelFromData serializes a PayChannel ledger entry from data
func SerializePayChannelFromData(channel *PayChannelData) ([]byte, error) {
	entry := channel.decoded
	if !entry.HasAccount() {
		if err := entry.SetAccountValue(channel.Account); err != nil {
			return nil, fmt.Errorf("failed to encode owner address: %w", err)
		}
	} else if account, err := entry.GetAccount(); err != nil || account != channel.Account {
		if err := entry.SetAccountValue(channel.Account); err != nil {
			return nil, fmt.Errorf("failed to encode owner address: %w", err)
		}
	}
	if !entry.HasDestination() {
		if err := entry.SetDestinationValue(channel.DestinationID); err != nil {
			return nil, fmt.Errorf("failed to encode destination address: %w", err)
		}
	} else if destination, err := entry.GetDestination(); err != nil || destination != channel.DestinationID {
		if err := entry.SetDestinationValue(channel.DestinationID); err != nil {
			return nil, fmt.Errorf("failed to encode destination address: %w", err)
		}
	}
	if err := entry.SetAmountValue(ledgerfields.AmountValue{Value: fmt.Sprintf("%d", channel.Amount)}); err != nil {
		return nil, err
	}
	if err := entry.SetBalanceValue(ledgerfields.AmountValue{Value: fmt.Sprintf("%d", channel.Balance)}); err != nil {
		return nil, err
	}
	entry.SetSettleDelayValue(channel.SettleDelay)
	entry.SetOwnerNodeValue(channel.OwnerNode)
	entry.SetFlagsValue(0)
	entry.SetPublicKey(channel.PublicKey)
	if channel.Sponsor != "" || (entry.HasSponsor() && channel.Sponsor == entry.Sponsor) {
		entry.SetSponsor(channel.Sponsor)
	} else {
		entry.ClearSponsor()
	}

	if channel.CancelAfter > 0 || (entry.HasCancelAfter() && channel.CancelAfter == entry.CancelAfter) {
		entry.SetCancelAfterValue(channel.CancelAfter)
	} else {
		entry.ClearCancelAfter()
	}
	if channel.Expiration > 0 || (entry.HasExpiration() && channel.Expiration == entry.Expiration) {
		entry.SetExpirationValue(channel.Expiration)
	} else {
		entry.ClearExpiration()
	}
	if channel.HasSourceTag {
		entry.SetSourceTagValue(channel.SourceTag)
	} else {
		entry.ClearSourceTag()
	}
	if channel.HasDestTag {
		entry.SetDestinationTagValue(channel.DestinationTag)
	} else {
		entry.ClearDestinationTag()
	}
	if channel.HasDestNode {
		entry.SetDestinationNodeValue(channel.DestinationNode)
	} else {
		entry.ClearDestinationNode()
	}
	if channel.HasSequence {
		entry.SetSequenceValue(channel.Sequence)
	} else {
		entry.ClearSequence()
	}
	entry.SetPreviousTxnIDValue(channel.PreviousTxnID)
	entry.SetPreviousTxnLgrSeqValue(channel.PreviousTxnLgrSeq)

	return entry.Encode()
}

// ParsePayChannel parses a PayChannel ledger entry from binary data
func ParsePayChannel(data []byte) (*PayChannelData, error) {
	entry := &ledgerfields.PayChannel{}
	if err := entry.Decode(data); err != nil {
		return nil, err
	}
	var err error
	channel := &PayChannelData{
		SettleDelay:       entry.SettleDelay,
		PublicKey:         strings.ToLower(entry.PublicKey),
		Expiration:        entry.Expiration,
		CancelAfter:       entry.CancelAfter,
		Sequence:          entry.Sequence,
		PreviousTxnLgrSeq: entry.PreviousTxnLgrSeq,
		HasSourceTag:      entry.HasSourceTag(),
		HasDestTag:        entry.HasDestinationTag(),
		HasDestNode:       entry.HasDestinationNode(),
		HasSequence:       entry.HasSequence(),
		SourceTag:         entry.SourceTag,
		DestinationTag:    entry.DestinationTag,
		Sponsor:           entry.Sponsor,
		decoded:           *entry,
	}

	if entry.HasAccount() {
		channel.Account, err = entry.GetAccount()
		if err != nil {
			return nil, err
		}
	}
	if entry.HasDestination() {
		channel.DestinationID, err = entry.GetDestination()
		if err != nil {
			return nil, err
		}
	}
	if entry.HasOwnerNode() {
		channel.OwnerNode, err = entry.GetOwnerNode()
		if err != nil {
			return nil, err
		}
	}
	if channel.HasDestNode {
		channel.DestinationNode, err = entry.GetDestinationNode()
		if err != nil {
			return nil, err
		}
	}
	if entry.HasPreviousTxnID() {
		channel.PreviousTxnID, err = entry.GetPreviousTxnID()
		if err != nil {
			return nil, err
		}
	}
	if entry.HasAmount() {
		amountValue, err := entry.GetAmount()
		if err != nil {
			return nil, err
		}
		amount, err := decodeLedgerAmount("PayChannel.Amount", amountValue)
		if err != nil {
			return nil, err
		}
		channel.Amount, err = nonNegativeNativeDrops("PayChannel.Amount", amount)
		if err != nil {
			return nil, err
		}
	}
	if entry.HasBalance() {
		balanceValue, err := entry.GetBalance()
		if err != nil {
			return nil, err
		}
		balance, err := decodeLedgerAmount("PayChannel.Balance", balanceValue)
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
