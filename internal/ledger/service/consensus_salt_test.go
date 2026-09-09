package service_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

func TestConsensusAcceptanceMalformedEntryPreservesAgreedSalt(t *testing.T) {
	svc := newServiceForOpenLedgerTest(t)
	t.Cleanup(svc.Stop)
	env := jtx.NewTestEnv(t)
	master := jtx.MasterAccount()
	alice, bob := jtx.NewAccount("salt-alice"), jtx.NewAccount("salt-bob")
	fundAlice, _ := buildSignedPaymentBlob(t, env, master, alice, 100_000_000, 1)
	fundBob, _ := buildSignedPaymentBlob(t, env, master, bob, 100_000_000, 2)
	parent := svc.GetClosedLedger()
	_, err := svc.AcceptConsensusResult(t.Context(), parent, [][]byte{fundAlice, fundBob}, nil, parent.CloseTime().Add(10*time.Second), true)
	require.NoError(t, err)

	aliceInfo, err := svc.GetAccountInfo(t.Context(), alice.Address, "closed")
	require.NoError(t, err)
	bobInfo, err := svc.GetAccountInfo(t.Context(), bob.Address, "closed")
	require.NoError(t, err)
	aliceBlob, aliceHash := buildSignedPaymentBlob(t, env, alice, master, 1_000_000, aliceInfo.Sequence)
	bobBlob, bobHash := buildSignedPaymentBlob(t, env, bob, master, 1_000_000, bobInfo.Sequence)

	setSalt := func(blobs ...[]byte) [32]byte {
		t.Helper()
		set := shamap.New(shamap.TypeTransaction)
		for _, blob := range blobs {
			hash := sha512half.Sum(protocol.HashPrefixTransactionID().Bytes(), blob)
			require.NoError(t, set.PutWithNodeType(hash, blob, shamap.NodeTypeTransactionNoMeta))
		}
		salt, err := set.Hash()
		require.NoError(t, err)
		return salt
	}
	aliceFirst := func(salt [32]byte) bool {
		a, b := alice.ID, bob.ID
		for i := range a {
			a[i] ^= salt[i]
			b[i] ^= salt[i]
		}
		return bytes.Compare(a[:], b[:]) < 0
	}
	withoutMalformed := aliceFirst(setSalt(aliceBlob, bobBlob))
	malformed := bytes.Repeat([]byte{0xff}, 32)
	var wantAliceFirst bool
	found := false
	for i := range 256 {
		malformed[len(malformed)-1] = byte(i)
		wantAliceFirst = aliceFirst(setSalt(aliceBlob, bobBlob, malformed))
		if wantAliceFirst != withoutMalformed {
			found = true
			break
		}
	}
	require.True(t, found, "fixture must distinguish the full agreed salt from a filtered salt")
	_, err = openledger.ParsePendingTx(malformed)
	require.Error(t, err)

	parent = svc.GetClosedLedger()
	_, err = svc.AcceptConsensusResult(t.Context(), parent, [][]byte{aliceBlob, malformed, bobBlob}, nil, parent.CloseTime().Add(10*time.Second), true)
	require.NoError(t, err)
	require.Equal(t, uint32(2), svc.GetClosedLedger().TxCount())
	aliceResult, err := svc.GetTransaction(aliceHash)
	require.NoError(t, err)
	bobResult, err := svc.GetTransaction(bobHash)
	require.NoError(t, err)
	if wantAliceFirst {
		require.Equal(t, uint32(0), aliceResult.TxIndex)
		require.Equal(t, uint32(1), bobResult.TxIndex)
	} else {
		require.Equal(t, uint32(1), aliceResult.TxIndex)
		require.Equal(t, uint32(0), bobResult.TxIndex)
	}
}
