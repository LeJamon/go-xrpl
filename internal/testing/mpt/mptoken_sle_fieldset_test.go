// Confirmation test that a holder's MPToken SLE carries exactly the field set
// rippled's ltMPTOKEN defines (ledger_entries.macro): sfAccount,
// sfMPTokenIssuanceID, sfMPTAmount (soeDEFAULT), sfLockedAmount (soeOPTIONAL),
// sfOwnerNode, sfPreviousTxnID, sfPreviousTxnLgrSeq, plus the common
// LedgerEntryType/Flags. go-xrpl previously specced Issuer and Sequence as
// extras, which would fork account_hash against rippled.
package mpt_test

import (
	"encoding/hex"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestMPToken_SLE_FieldSet(t *testing.T) {
	env := jtx.NewTestEnv(t)
	issuer := jtx.NewAccount("issuer")
	holder := jtx.NewAccount("holder")

	tester := mpt.NewMPTTester(t, env, issuer, mpt.MPTInit{Holders: []*jtx.Account{holder}})
	tester.Create(mpt.CreateOpts{})
	env.Close()
	tester.Authorize(mpt.AuthorizeOpts{Account: holder})
	env.Close()

	idBytes, err := hex.DecodeString(tester.IssuanceID())
	require.NoError(t, err)
	var mptID [24]byte
	copy(mptID[:], idBytes)
	holderID, err := state.DecodeAccountID(holder.Address)
	require.NoError(t, err)

	data, err := env.LedgerEntry(keylet.MPTokenByID(mptID, holderID))
	require.NoError(t, err)
	fields, err := binarycodec.Decode(hex.EncodeToString(data))
	require.NoError(t, err)

	allowed := map[string]bool{
		"LedgerEntryType":   true,
		"Flags":             true,
		"Account":           true,
		"MPTokenIssuanceID": true,
		"MPTAmount":         true,
		"LockedAmount":      true,
		"OwnerNode":         true,
		"PreviousTxnID":     true,
		"PreviousTxnLgrSeq": true,
	}
	for name := range fields {
		require.True(t, allowed[name], "field %s is not part of rippled's ltMPTOKEN", name)
	}
	require.Equal(t, holder.Address, fields["Account"])
	require.Contains(t, fields, "MPTokenIssuanceID")
	require.NotContains(t, fields, "Issuer")
	require.NotContains(t, fields, "Sequence")
}
