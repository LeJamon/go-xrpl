package service

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/ledger/header"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAcceptConsensusResult_CloseTimeCorrectFlag pins issue #361. When
// consensus did not agree on close time, AcceptConsensusResult must
// stamp the closed ledger header with sLCF_NoConsensusTime (0x01) so
// the resulting hash matches what rippled produces in the same case
// (RCLConsensus.cpp:775-801 → Ledger.cpp:367).
func TestAcceptConsensusResult_CloseTimeCorrectFlag(t *testing.T) {
	correct := closeAndGetFlags(t, true)
	noConsensus := closeAndGetFlags(t, false)

	assert.Zero(t, correct&header.LCFNoConsensusTime,
		"closeTimeCorrect=true must clear LCFNoConsensusTime")
	assert.NotZero(t, noConsensus&header.LCFNoConsensusTime,
		"closeTimeCorrect=false must set LCFNoConsensusTime")
}

// TestAcceptConsensusResult_CloseFlagsAffectHash verifies the flag
// participates in the ledger hash. Two ledgers with identical inputs but
// different closeTimeCorrect values must have different hashes; otherwise
// the flag could not be the cause of the silent rippled rejection in #358.
func TestAcceptConsensusResult_CloseFlagsAffectHash(t *testing.T) {
	hashCorrect := closeAndGetHash(t, true)
	hashNoConsensus := closeAndGetHash(t, false)

	assert.NotEqual(t, hashCorrect, hashNoConsensus,
		"closeFlags byte is part of the hashed header — flipping it must change the hash")
}

func closeAndGetFlags(t *testing.T, closeTimeCorrect bool) uint8 {
	t.Helper()
	hdr := closeAndGetHeader(t, closeTimeCorrect)
	return hdr.CloseFlags
}

func closeAndGetHash(t *testing.T, closeTimeCorrect bool) [32]byte {
	t.Helper()
	hdr := closeAndGetHeader(t, closeTimeCorrect)
	return hdr.Hash
}

func closeAndGetHeader(t *testing.T, closeTimeCorrect bool) header.LedgerHeader {
	t.Helper()
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	parent := svc.GetClosedLedger()
	require.NotNil(t, parent)
	closeTime := time.Unix(1700000000, 0)

	_, err = svc.AcceptConsensusResult(context.TODO(), parent, nil, closeTime, closeTimeCorrect)
	require.NoError(t, err)

	svc.mu.RLock()
	defer svc.mu.RUnlock()
	return svc.closedLedger.Header()
}
