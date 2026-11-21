package mptutil

import (
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/stretchr/testify/require"
)

// TestLPTokenFrozenForIssuer_AMMUnresolvable covers the corrupt-ledger arm: an
// issuer AccountRoot carrying sfAMMID whose referenced AMM SLE is absent. The
// status must remain distinct so account-funds and check-freeze callers can
// handle the failure differently.
func TestLPTokenFrozenForIssuer_AMMUnresolvable(t *testing.T) {
	var issuer [20]byte
	issuer[0] = 0xAA
	var holder [20]byte
	holder[0] = 0xBB
	var ammID [32]byte
	ammID[0] = 0xCC

	view := newMPTTestView()
	putLPAMMIssuer(t, view, issuer, ammID)

	if got := LPTokenFrozenForIssuer(view, holder, issuer); got != LPTokenAMMUnresolvable {
		t.Fatalf("LPTokenFrozenForIssuer with missing AMM SLE = %v, want LPTokenAMMUnresolvable", got)
	}
}

func TestLPTokenFrozenForIssuer_NotAMM(t *testing.T) {
	var issuer [20]byte
	issuer[0] = 0x11
	var holder [20]byte
	holder[0] = 0x22

	view := newMPTTestView()
	putTestAccount(t, view, issuer, 0, [32]byte{})

	if got := LPTokenFrozenForIssuer(view, holder, issuer); got != LPTokenIssuerNotAMM {
		t.Fatalf("LPTokenFrozenForIssuer for non-AMM issuer = %v, want LPTokenIssuerNotAMM", got)
	}
}

func TestLPTokenFrozenForIssuer_MissingIssuer(t *testing.T) {
	var issuer [20]byte
	issuer[0] = 0x33
	var holder [20]byte
	holder[0] = 0x44

	view := newMPTTestView()
	if got := LPTokenFrozenForIssuer(view, holder, issuer); got != LPTokenIssuerNotAMM {
		t.Fatalf("LPTokenFrozenForIssuer for missing issuer = %v, want LPTokenIssuerNotAMM", got)
	}
}

func TestLPTokenFrozenForIssuerChecksMPTLocks(t *testing.T) {
	view := newMPTTestView()
	var ammAccount, holder, issuerOne, issuerTwo [20]byte
	ammAccount[19] = 1
	holder[19] = 2
	issuerOne[19] = 3
	issuerTwo[19] = 4
	ammID := [32]byte{9}
	idOne := keylet.MakeMPTID(1, issuerOne)
	idTwo := keylet.MakeMPTID(1, issuerTwo)
	putLPAMMIssuer(t, view, ammAccount, ammID)
	putTestIssuance(t, view, idOne, 0, nil)
	putTestIssuance(t, view, idTwo, 0, nil)

	ammHex, err := binarycodec.Encode(map[string]any{
		"LedgerEntryType": "AMM",
		"Account":         state.EncodeAccountIDSafe(ammAccount),
		"LPTokenBalance": map[string]any{
			"value": "1", "currency": "USD", "issuer": state.EncodeAccountIDSafe(ammAccount),
		},
		"Asset":     map[string]any{"mpt_issuance_id": EncodeID(idOne)},
		"Asset2":    map[string]any{"mpt_issuance_id": EncodeID(idTwo)},
		"OwnerNode": "0",
		"Flags":     uint32(0),
	})
	require.NoError(t, err)
	ammRaw, err := hex.DecodeString(ammHex)
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.AMMByID(ammID), ammRaw))

	require.Equal(t, LPTokenNotFrozen, LPTokenFrozenForIssuer(view, holder, ammAccount))
	putTestIssuance(t, view, idOne, entry.LsfMPTLocked, nil)
	require.Equal(t, LPTokenFrozen, LPTokenFrozenForIssuer(view, holder, ammAccount))
	putTestIssuance(t, view, idOne, 0, nil)
	putTestHolding(t, view, idTwo, holder, entry.LsfMPTLocked)
	require.Equal(t, LPTokenFrozen, LPTokenFrozenForIssuer(view, holder, ammAccount))
}

func putLPAMMIssuer(t *testing.T, view *mptTestView, issuer [20]byte, ammID [32]byte) {
	t.Helper()
	raw, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:  state.EncodeAccountIDSafe(issuer),
		Balance:  1_000_000,
		Sequence: 1,
		AMMID:    ammID,
	})
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.Account(issuer), raw))
}
