package ledgerstatefix

import (
	"errors"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

func init() {
	tx.Register(tx.TypeLedgerStateFix, func() tx.Transaction {
		return &LedgerStateFix{BaseTx: *tx.NewBaseTx(tx.TypeLedgerStateFix, "")}
	})
}

// LedgerStateFix fix types
// Reference: rippled LedgerStateFix.h FixType enum
const (
	// LedgerFixTypeNFTokenPageLink repairs NFToken directory page links
	LedgerFixTypeNFTokenPageLink uint8 = 1
)

// LedgerStateFix errors
var (
	ErrLedgerFixInvalidType   = errors.New("tefINVALID_LEDGER_FIX_TYPE: invalid LedgerFixType")
	ErrLedgerFixOwnerRequired = errors.New("temINVALID: Owner is required for nfTokenPageLink fix")
)

// LedgerStateFix is a system transaction to fix ledger state issues.
// Reference: rippled LedgerStateFix.cpp
type LedgerStateFix struct {
	tx.BaseTx

	// LedgerFixType identifies the type of fix (required)
	LedgerFixType uint8 `json:"LedgerFixType" xrpl:"LedgerFixType"`

	// Owner is the owner account (required for nfTokenPageLink fix)
	Owner string `json:"Owner,omitempty" xrpl:"Owner,omitempty"`
}

// NewLedgerStateFix creates a new LedgerStateFix transaction
func NewLedgerStateFix(account string, fixType uint8) *LedgerStateFix {
	return &LedgerStateFix{
		BaseTx:        *tx.NewBaseTx(tx.TypeLedgerStateFix, account),
		LedgerFixType: fixType,
	}
}

// NewNFTokenPageLinkFix creates a LedgerStateFix for NFToken page link repair
func NewNFTokenPageLinkFix(account, owner string) *LedgerStateFix {
	return &LedgerStateFix{
		BaseTx:        *tx.NewBaseTx(tx.TypeLedgerStateFix, account),
		LedgerFixType: LedgerFixTypeNFTokenPageLink,
		Owner:         owner,
	}
}

// TxType returns the transaction type
func (l *LedgerStateFix) TxType() tx.Type {
	return tx.TypeLedgerStateFix
}

// Validate validates the LedgerStateFix transaction
// Reference: rippled LedgerStateFix.cpp preflight()
func (l *LedgerStateFix) Validate() error {
	if err := l.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags (universal mask)
	// Reference: rippled LedgerStateFix.cpp:36-37
	if l.Common.Flags != nil && *l.Common.Flags&tx.TfUniversalMask != 0 {
		return tx.ErrInvalidFlags
	}

	// Validate LedgerFixType and required fields based on type
	// Reference: rippled LedgerStateFix.cpp:42-51
	switch l.LedgerFixType {
	case LedgerFixTypeNFTokenPageLink:
		// Owner is required for nfTokenPageLink fix
		// Reference: rippled LedgerStateFix.cpp:45-46
		if l.Owner == "" {
			return ErrLedgerFixOwnerRequired
		}
	default:
		// Invalid fix type
		// Reference: rippled LedgerStateFix.cpp:49-50
		return ErrLedgerFixInvalidType
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (l *LedgerStateFix) Flatten() (map[string]any, error) {
	m, err := tx.ReflectFlatten(l)
	if err != nil {
		return nil, err
	}
	// LedgerFixType is UInt16 in the binary codec but uint8 in Go.
	// Convert to int so the codec's UInt16.FromJSON() can handle it.
	if v, ok := m["LedgerFixType"]; ok {
		switch val := v.(type) {
		case uint8:
			m["LedgerFixType"] = int(val)
		}
	}
	return m, nil
}

// RequiredAmendments returns the amendments required for this transaction type
func (l *LedgerStateFix) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureFixNFTokenPageLinks}
}

// Apply applies the LedgerStateFix transaction to the ledger.
func (l *LedgerStateFix) Apply() tx.Result {
	if l.Owner != "" {
		_, err := sle.DecodeAccountID(l.Owner)
		if err != nil {
			return tx.TecNO_TARGET
		}
	}
	return tx.TesSUCCESS
}
