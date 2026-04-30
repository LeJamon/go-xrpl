// Package offer implements the OfferCreate and OfferCancel transactions.
// Reference: rippled CreateOffer.cpp, CancelOffer.cpp
package offer

import (
	"encoding/hex"
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/tx"
)

// OfferCreate flag mask - invalid flags
// Reference: rippled TxFlags.h
const (
	// tfHybrid flag for permissioned DEX hybrid offers
	tfHybrid uint32 = 0x00100000

	// tfOfferCreateMask is the mask for INVALID flags.
	// Any flags set in this mask are invalid for OfferCreate.
	// Reference: rippled TxFlags.h lines 103-104
	// Derived from: ~(tfUniversal | tfPassive | tfImmediateOrCancel | tfFillOrKill | tfSell | tfHybrid)
	// Valid flags: 0xC0000000 | 0x00010000 | 0x00020000 | 0x00040000 | 0x00080000 | 0x00100000 = 0xC01F0000
	// Mask for invalid: ~0xC01F0000 = 0x3FE0FFFF
	tfOfferCreateMask uint32 = 0x3FE0FFFF
)

// OfferCreate places an offer on the decentralized exchange.
type OfferCreate struct {
	tx.BaseTx

	// TakerGets is the amount and currency the offer creator receives (required)
	TakerGets tx.Amount `json:"TakerGets" xrpl:"TakerGets,amount"`

	// TakerPays is the amount and currency the offer creator pays (required)
	TakerPays tx.Amount `json:"TakerPays" xrpl:"TakerPays,amount"`

	// Expiration is the time when the offer expires (optional)
	Expiration *uint32 `json:"Expiration,omitempty" xrpl:"Expiration,omitempty"`

	// OfferSequence is the sequence number of an offer to cancel (optional)
	OfferSequence *uint32 `json:"OfferSequence,omitempty" xrpl:"OfferSequence,omitempty"`

	// DomainID is the permissioned domain for hybrid offers (optional, requires PermissionedDEX amendment)
	DomainID *[32]byte `json:"DomainID,omitempty" xrpl:"DomainID,omitempty"`
}

func init() {
	tx.Register(tx.TypeOfferCreate, func() tx.Transaction {
		return &OfferCreate{BaseTx: *tx.NewBaseTx(tx.TypeOfferCreate, "")}
	})
}

// UnmarshalJSON handles DomainID as a hex string from the binary codec.
func (o *OfferCreate) UnmarshalJSON(data []byte) error {
	type Alias OfferCreate
	aux := &struct {
		DomainID *string `json:"DomainID,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(o),
	}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	if aux.DomainID != nil {
		b, err := hex.DecodeString(*aux.DomainID)
		if err != nil || len(b) != 32 {
			return &json.UnmarshalTypeError{Value: "string", Type: nil}
		}
		var id [32]byte
		copy(id[:], b)
		o.DomainID = &id
	}
	return nil
}

// NewOfferCreate creates a new OfferCreate transaction
func NewOfferCreate(account string, takerGets, takerPays tx.Amount) *OfferCreate {
	return &OfferCreate{
		BaseTx:    *tx.NewBaseTx(tx.TypeOfferCreate, account),
		TakerGets: takerGets,
		TakerPays: takerPays,
	}
}

func (o *OfferCreate) TxType() tx.Type {
	return tx.TypeOfferCreate
}

func (o *OfferCreate) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(o)
}

// SetPassive makes the offer passive
func (o *OfferCreate) SetPassive() {
	flags := o.GetFlags() | OfferCreateFlagPassive
	o.SetFlags(flags)
}

// SetImmediateOrCancel makes the offer immediate-or-cancel
func (o *OfferCreate) SetImmediateOrCancel() {
	flags := o.GetFlags() | OfferCreateFlagImmediateOrCancel
	o.SetFlags(flags)
}

// SetFillOrKill makes the offer fill-or-kill
func (o *OfferCreate) SetFillOrKill() {
	flags := o.GetFlags() | OfferCreateFlagFillOrKill
	o.SetFlags(flags)
}
