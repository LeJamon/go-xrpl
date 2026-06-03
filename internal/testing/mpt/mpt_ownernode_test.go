// Regression test for the sfOwnerNode directory-page bug class (see issue #729).
// A created MPTokenIssuance must record the owner-directory page from DirInsert
// in sfOwnerNode rather than a hardcoded 0; otherwise the issuance SLE diverges
// from rippled once the issuer's owner directory paginates, forking account_hash.
// Reference: rippled MPTokenIssuanceCreate.cpp:105-117.
package mpt_test

import (
	"encoding/hex"
	"strconv"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	mpttx "github.com/LeJamon/go-xrpl/internal/tx/mpt"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func ownerNodeBySLE(t *testing.T, env *jtx.TestEnv, ledgerIndexHex string) uint64 {
	t.Helper()
	raw, err := hex.DecodeString(ledgerIndexHex)
	require.NoError(t, err)
	var k [32]byte
	copy(k[:], raw)
	data, err := env.LedgerEntry(keylet.Keylet{Key: k})
	require.NoError(t, err)
	fields, err := binarycodec.Decode(hex.EncodeToString(data))
	require.NoError(t, err)
	s, ok := fields["OwnerNode"].(string)
	require.True(t, ok, "OwnerNode must be present in MPTokenIssuance SLE")
	v, err := strconv.ParseUint(s, 16, 64)
	require.NoError(t, err)
	return v
}

func TestMPTokenIssuanceCreate_OwnerNode_Pagination(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.FundAmount(alice, uint64(jtx.XRP(100000)))
	env.Close()

	// 33 issuances: 32 fill owner-dir page 0, the 33rd lands on page 1.
	var last jtx.TxResult
	for i := 0; i < 33; i++ {
		c := mpttx.NewMPTokenIssuanceCreate(alice.Address)
		c.Fee = "10"
		last = env.Submit(c)
		jtx.RequireTxSuccess(t, last)
		env.Close()
	}

	var found bool
	for _, n := range last.Metadata.AffectedNodes {
		if n.NodeType == "CreatedNode" && n.LedgerEntryType == "MPTokenIssuance" {
			require.Equal(t, uint64(1), ownerNodeBySLE(t, env, n.LedgerIndex),
				"33rd issuance must record owner-dir page 1, not hardcoded 0")
			found = true
		}
	}
	require.True(t, found, "expected a created MPTokenIssuance node")
}
