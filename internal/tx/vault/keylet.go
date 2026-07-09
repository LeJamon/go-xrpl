package vault

import (
	"encoding/binary"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/crypto/common"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
)

// maxPseudoAccountAttempts mirrors rippled's pseudoAccountAddress cap; changing
// it would require an amendment.
const maxPseudoAccountAttempts = 256

// pseudoAccountAddress derives a vault pseudo-account ID from the vault keylet,
// trying up to 256 candidates hashed from (i, parentHash, pseudoOwnerKey) until
// one is unoccupied. Returns the zero AccountID when every slot is taken.
func pseudoAccountAddress(view tx.LedgerView, parentHash [32]byte, pseudoOwnerKey [32]byte) [20]byte {
	for i := range uint16(maxPseudoAccountAttempts) {
		var iBytes [2]byte
		binary.BigEndian.PutUint16(iBytes[:], i)
		hash := common.Sha512Half(iBytes[:], parentHash[:], pseudoOwnerKey[:])

		var accountID [20]byte
		copy(accountID[:], addresscodec.SHA256RIPEMD160(hash[:]))

		if exists, _ := view.Exists(keylet.Account(accountID)); !exists {
			return accountID
		}
	}
	return [20]byte{}
}
