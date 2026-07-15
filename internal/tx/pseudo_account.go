package tx

import (
	"encoding/binary"

	"github.com/LeJamon/go-xrpl/amendment"
	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// maxPseudoAccountAttempts caps the pseudo-account address search. It
// participates in the derived address, so changing it would require an amendment.
const maxPseudoAccountAttempts = 256

// PseudoAccountAddress derives an unoccupied pseudo-account ID from ownerKey,
// trying up to 256 candidates hashed from (i, parentHash, ownerKey) until one is
// free in view. Returns the zero AccountID when every candidate slot is occupied.
func PseudoAccountAddress(view LedgerView, parentHash, ownerKey [32]byte) [20]byte {
	for i := range uint16(maxPseudoAccountAttempts) {
		var iBytes [2]byte
		binary.BigEndian.PutUint16(iBytes[:], i)
		hash := sha512half.Sum(iBytes[:], parentHash[:], ownerKey[:])

		var id [20]byte
		copy(id[:], addresscodec.SHA256RIPEMD160(hash[:]))

		if exists, _ := view.Exists(keylet.Account(id)); !exists {
			return id
		}
	}
	return [20]byte{}
}

// IsPseudoAccountID reports whether id is an existing pseudo-account (an
// AccountRoot carrying at least one owner-designator field).
func IsPseudoAccountID(view LedgerView, id [20]byte) bool {
	ar, err := ReadAccountRoot(view, id)
	if err != nil || ar == nil {
		return false
	}
	return ar.IsPseudoAccount()
}

// PseudoMarker selects which owner-designator field links a pseudo-account back
// to the object that owns it.
type PseudoMarker uint8

const (
	PseudoAMMID PseudoMarker = iota
	PseudoVaultID
	PseudoLoanBrokerID
)

// CreatePseudoAccount derives an unoccupied pseudo-account for ownerKey, builds a
// fresh AccountRoot marked with the given owner-designator field, inserts it, and
// returns the account ID and its (still mutable) SLE. The sequence is 0 when
// SingleAssetVault or LendingProtocol is enabled, else the building ledger's
// sequence. Balance is 0, the master key is disabled, default rippling is on, and
// deposit auth is set. Returns tecDUPLICATE when all 256 candidate slots are
// occupied.
func CreatePseudoAccount(ctx *ApplyContext, ownerKey [32]byte, marker PseudoMarker) ([20]byte, *state.AccountRoot, ter.Result) {
	id := PseudoAccountAddress(ctx.View, ctx.Config.ParentHash, ownerKey)
	if id == ([20]byte{}) {
		return id, nil, ter.TecDUPLICATE
	}
	addr, err := state.EncodeAccountID(id)
	if err != nil {
		return id, nil, ctx.Internal("createPseudoAccount: encode account id", err)
	}

	var seq uint32
	if !ctx.Rules().Enabled(amendment.FeatureSingleAssetVault) &&
		!ctx.Rules().Enabled(amendment.FeatureLendingProtocol) {
		seq = ctx.Config.LedgerSequence
	}

	ar := &state.AccountRoot{
		Account:  addr,
		Balance:  0,
		Sequence: seq,
		Flags:    state.LsfDisableMaster | state.LsfDefaultRipple | state.LsfDepositAuth,
	}
	switch marker {
	case PseudoAMMID:
		ar.AMMID = ownerKey
	case PseudoVaultID:
		ar.VaultID = ownerKey
	case PseudoLoanBrokerID:
		ar.LoanBrokerID = ownerKey
	}

	data, serr := state.SerializeAccountRoot(ar)
	if serr != nil {
		return id, nil, ctx.Internal("createPseudoAccount: serialize account root", serr)
	}
	if ierr := ctx.View.Insert(keylet.Account(id), data); ierr != nil {
		return id, nil, ctx.Internal("createPseudoAccount: insert account root", ierr)
	}
	return id, ar, ter.TesSUCCESS
}
