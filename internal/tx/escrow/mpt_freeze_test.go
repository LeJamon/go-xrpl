package escrow

import (
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/stretchr/testify/require"
)

type rulesMapView struct {
	*mapView
	rules *amendment.Rules
}

func (v *rulesMapView) Rules() *amendment.Rules { return v.rules }

func TestMPTVaultShareBackingFreezeBlocksEscrowCreateAndFinish(t *testing.T) {
	view := &rulesMapView{mapView: newMapView(), rules: amendment.AllSupportedRules()}
	var sender, destination, owner, pseudo, underlyingIssuer [20]byte
	sender[0], destination[0], owner[0], pseudo[0], underlyingIssuer[0] = 0x10, 0x20, 0x30, 0x70, 0x50
	vaultID := [32]byte{9}
	shareID := keylet.MakeMPTID(1, pseudo)
	underlyingID := keylet.MakeMPTID(2, underlyingIssuer)
	shareHex := mptutil.EncodeID(shareID)

	putFreezeAccount(t, view, pseudo, vaultID)
	putFreezeAccount(t, view, underlyingIssuer, [32]byte{})
	putFreezeVault(t, view, vaultID, owner, pseudo, shareID, underlyingID)
	putFreezeIssuance(t, view, underlyingID, entry.LsfMPTLocked|entry.LsfMPTCanTransfer, nil)
	reference := keylet.MPTokenByID(underlyingID, pseudo)
	referenceHex := strings.ToUpper(hex.EncodeToString(reference.Key[:]))
	putFreezeIssuance(t, view, shareID, entry.LsfMPTCanEscrow|entry.LsfMPTCanTransfer, &referenceHex)
	putFreezeHolding(t, view, shareID, sender, 10)
	putFreezeHolding(t, view, underlyingID, pseudo, 1)

	amount := state.NewMPTAmountWithIssuanceID(1, state.EncodeAccountIDSafe(pseudo), shareHex)
	require.Equal(t, ter.TecLOCKED, escrowCreatePreclaimMPT(view, view.rules, sender, destination, amount, 0))
	require.Equal(t, ter.TecLOCKED, escrowFinishPreclaimMPT(view, destination, amount, 0))

	// A transferable share still inherits the backing MPT's transfer flag.
	putFreezeIssuance(t, view, underlyingID, 0, nil)
	require.Equal(t, ter.TecNO_AUTH, escrowCreatePreclaimMPT(view, view.rules, sender, destination, amount, 0))
}

func putFreezeAccount(t *testing.T, view *rulesMapView, account [20]byte, vaultID [32]byte) {
	t.Helper()
	raw, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:  state.EncodeAccountIDSafe(account),
		Balance:  1_000_000_000,
		Sequence: 1,
		VaultID:  vaultID,
	})
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.Account(account), raw))
}

func putFreezeIssuance(t *testing.T, view *rulesMapView, id [24]byte, flags uint32, reference *string) {
	t.Helper()
	raw, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{
		Issuer:           mptutil.Issuer(id),
		Sequence:         binary.BigEndian.Uint32(id[:4]),
		Flags:            flags,
		ReferenceHolding: reference,
	})
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.MPTIssuance(id), raw))
}

func putFreezeHolding(t *testing.T, view *rulesMapView, id [24]byte, account [20]byte, amount uint64) {
	t.Helper()
	raw, err := state.SerializeMPToken(&state.MPTokenData{
		Account:           account,
		MPTokenIssuanceID: id,
		MPTAmount:         amount,
	})
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.MPTokenByID(id, account), raw))
}

func putFreezeVault(t *testing.T, view *rulesMapView, vaultID [32]byte, owner, pseudo [20]byte, shareID, assetID [24]byte) {
	t.Helper()
	hexRaw, err := binarycodec.Encode(map[string]any{
		"LedgerEntryType":   "Vault",
		"Flags":             uint32(0),
		"Sequence":          uint32(1),
		"OwnerNode":         "0",
		"Owner":             state.EncodeAccountIDSafe(owner),
		"Account":           state.EncodeAccountIDSafe(pseudo),
		"Asset":             map[string]any{"mpt_issuance_id": mptutil.EncodeID(assetID)},
		"ShareMPTID":        mptutil.EncodeID(shareID),
		"WithdrawalPolicy":  uint8(0),
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
	})
	require.NoError(t, err)
	raw, err := hex.DecodeString(hexRaw)
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.VaultByID(vaultID), raw))
}
