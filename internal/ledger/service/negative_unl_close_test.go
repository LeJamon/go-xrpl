package service_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/testutil/rippled"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

// TestAcceptConsensusResult_FlagLedgerAppliesNegativeUNL is the regression test
// for issue #1091: the live consensus close path must apply the flag-ledger
// NegativeUNL transition (move sfValidatorToDisable into sfDisabledValidators,
// clear the transition field) just like the catchup/replay path, or a
// locally-built flag ledger computes a different account_hash than the rest of
// the network and forks.
//
// The empty-consensus-tx-set close is the gap: AcceptConsensusResult skips
// buildClosedLedgerLocked when the agreed tx set is empty, yet rippled applies
// the transition on every flag ledger (seq % 256 == 0) regardless of whether it
// carries transactions. This drives a flag-ledger close through that path with
// a pending ValidatorToDisable seeded on the parent and asserts the transition
// materialised.
func TestAcceptConsensusResult_FlagLedgerAppliesNegativeUNL(t *testing.T) {
	cfg := defaultServiceConfig()
	cfg.Standalone = false
	svc, err := service.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	closeTime := time.Unix(1700000000, 0)

	// Build a chain from genesis up to seq 254 so the next ledger (256, built
	// below) is the first flag ledger.
	parent := svc.GetClosedLedger()
	for parent.Sequence() < 254 {
		closeTime = closeTime.Add(2 * time.Second)
		open, err := ledger.NewOpen(parent, closeTime)
		if err != nil {
			t.Fatalf("NewOpen %d: %v", parent.Sequence()+1, err)
		}
		if err := open.Close(closeTime, 0); err != nil {
			t.Fatalf("Close %d: %v", open.Sequence(), err)
		}
		parent = open
	}

	// Hand-build seq 255 carrying a pending ValidatorToDisable, as a UNLModify
	// pseudo-tx on the prior flag ledger would have left it.
	validator := negUNLValidator()
	seed, err := pseudo.SerializeNegativeUNLSLE(&pseudo.NegativeUNLSLE{
		ValidatorToDisable: validator,
	})
	if err != nil {
		t.Fatalf("serialize NegativeUNL seed: %v", err)
	}

	closeTime = closeTime.Add(2 * time.Second)
	open255, err := ledger.NewOpen(parent, closeTime)
	if err != nil {
		t.Fatalf("NewOpen 255: %v", err)
	}
	if err := open255.Insert(keylet.NegativeUNL(), seed); err != nil {
		t.Fatalf("insert NegativeUNL SLE: %v", err)
	}
	if err := open255.Close(closeTime, 0); err != nil {
		t.Fatalf("Close 255: %v", err)
	}
	if open255.Sequence() != 255 {
		t.Fatalf("hand-built parent seq = %d, want 255", open255.Sequence())
	}

	// Build the flag ledger (seq 256) through the consensus path with an EMPTY
	// tx set — the sub-path that skipped the transition before this fix.
	closeTime = closeTime.Add(2 * time.Second)
	if err := svc.SwitchToPreferredLedger(open255); err != nil {
		t.Fatalf("switch flag-ledger parent: %v", err)
	}
	seq, err := svc.AcceptConsensusResult(context.TODO(), open255, nil, nil, closeTime, true)
	if err != nil {
		t.Fatalf("flag-ledger consensus close: %v", err)
	}
	if seq != 256 {
		t.Fatalf("flag-ledger seq = %d, want 256", seq)
	}

	assertFlagLedgerTransitioned(t, svc, validator)
}

func TestAcceptConsensusResult_UNLModifyVote(t *testing.T) {
	all.RegisterAll()
	var fixture struct {
		OracleCommit string
		ParentHeader string
		ParentState  []struct{ Index, Data string }
		ChildHeader  string
		Vote         string
		TxID         string
		Metadata     string
		NegativeUNL  string
	}
	data, err := os.ReadFile("testdata/unl_modify_vote.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.Equal(t, rippled.Pin.Commit, fixture.OracleCommit)
	decode := func(s string) []byte {
		t.Helper()
		b, err := hex.DecodeString(s)
		require.NoError(t, err)
		return b
	}

	parentHeader, err := header.DeserializeHeader(decode(fixture.ParentHeader), false)
	require.NoError(t, err)
	parentHeader.Hash = header.CalculateHash(*parentHeader)
	state := shamap.New(shamap.TypeState)
	for _, entry := range fixture.ParentState {
		key, err := protocol.Hash256FromHex(entry.Index)
		require.NoError(t, err)
		require.NoError(t, state.Put(key, decode(entry.Data)))
	}
	root, err := state.Hash()
	require.NoError(t, err)
	require.Equal(t, parentHeader.AccountHash, root)
	require.Zero(t, parentHeader.TxHash)
	parent, err := ledger.NewFromHeader(*parentHeader, state, shamap.New(shamap.TypeTransaction), drops.DefaultFees())
	require.NoError(t, err)

	config := defaultServiceConfig()
	config.Standalone = false
	svc, err := service.New(config)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	require.NoError(t, svc.SwitchToPreferredLedger(parent))

	child, err := header.DeserializeHeader(decode(fixture.ChildHeader), false)
	require.NoError(t, err)
	require.Equal(t, parent.Hash(), child.ParentHash)
	vote := decode(fixture.Vote)
	seq, err := svc.AcceptConsensusResult(t.Context(), parent, [][]byte{vote}, nil, child.CloseTime, child.GetCloseAgree())
	require.NoError(t, err)
	require.Equal(t, child.LedgerIndex, seq)

	closed := svc.GetClosedLedger()
	id, err := protocol.Hash256FromHex(fixture.TxID)
	require.NoError(t, err)
	leaf, found, err := closed.GetTransaction(id)
	require.NoError(t, err)
	require.True(t, found)
	txBlob, meta, err := tx.SplitTxWithMetaBlobStrict(leaf)
	require.NoError(t, err)
	require.Equal(t, vote, txBlob)
	require.Equal(t, decode(fixture.Metadata), meta)
	sle, err := closed.Read(keylet.NegativeUNL())
	require.NoError(t, err)
	require.Equal(t, decode(fixture.NegativeUNL), sle)
	require.Equal(t, child.TxHash, closed.Header().TxHash)
	require.Equal(t, child.AccountHash, closed.Header().AccountHash)
	require.Equal(t, header.CalculateHash(*child), closed.Hash())
}
