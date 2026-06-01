package adaptor

import (
	"errors"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeLookup is a hand-rolled ledgerLookup double. It maps a ledger hash to
// a *ledger.Ledger so each test can inject the exact graph it wants without
// spinning up a full *service.Service. Sequence-based lookups are used by
// only one production code path (mtGET_LEDGER fallback) and are not
// exercised by the LedgerProvider contract tests, so we leave it minimal.
type fakeLookup struct {
	byHash map[[32]byte]*ledger.Ledger
}

func newFakeLookup() *fakeLookup {
	return &fakeLookup{byHash: make(map[[32]byte]*ledger.Ledger)}
}

func (f *fakeLookup) add(l *ledger.Ledger) {
	f.byHash[l.Hash()] = l
}

func (f *fakeLookup) GetLedgerByHash(hash [32]byte) (*ledger.Ledger, error) {
	l, ok := f.byHash[hash]
	if !ok {
		return nil, errors.New("not found")
	}
	return l, nil
}

func (f *fakeLookup) GetLedgerBySequence(_ uint32) (*ledger.Ledger, error) {
	// Not exercised by these tests — returning an error matches the
	// service's ErrLedgerNotFound contract closely enough for safety.
	return nil, errors.New("not found")
}

// makeGenesisLedger returns a genesis-derived, validated (and therefore
// immutable) ledger. It is the cheapest "real" ledger we can hand the
// provider for tests that don't need transactions in the tx map.
func makeGenesisLedger(t *testing.T) *ledger.Ledger {
	t.Helper()
	res, err := genesis.Create(genesis.DefaultConfig())
	require.NoError(t, err)
	return ledger.FromGenesis(res.Header, res.StateMap, res.TxMap, drops.Fees{})
}

// makeClosedLedgerWithTxs builds a fresh open ledger on top of genesis,
// stuffs the supplied (key, blob) pairs into the tx map, then closes it
// (which freezes both SHAMaps and computes the ledger hash). Returns the
// closed ledger ready for hash-based lookup.
func makeClosedLedgerWithTxs(t *testing.T, txs []struct {
	key  [32]byte
	blob []byte
}) *ledger.Ledger {
	t.Helper()
	parent := makeGenesisLedger(t)
	open, err := ledger.NewOpen(parent, time.Now())
	require.NoError(t, err)
	for _, txn := range txs {
		// Real wire-format txs use NodeTypeTransactionWithMeta; this matches
		// what AcceptConsensusResult would do.
		require.NoError(t, open.AddTransactionWithMeta(txn.key, txn.blob))
	}
	require.NoError(t, open.Close(time.Now(), 0))
	return open
}

// makeOpenLedger returns an unclosed (mutable) ledger built on genesis.
// Unlike makeClosedLedgerWithTxs it leaves state == StateOpen so
// IsImmutable() reports false — the input we want for the
// "open ledger refused" replay-delta test.
func makeOpenLedger(t *testing.T) *ledger.Ledger {
	t.Helper()
	parent := makeGenesisLedger(t)
	open, err := ledger.NewOpen(parent, time.Now())
	require.NoError(t, err)
	return open
}

// fixedKey32 produces a deterministic 32-byte key with byte i+offset, so
// tests can use multiple distinct keys without worrying about collisions.
func fixedKey32(offset byte) [32]byte {
	var k [32]byte
	for i := range k {
		k[i] = byte(i) + offset
	}
	return k
}

// TestLedgerProvider_GetReplayDelta_ImmutableLedger verifies the happy
// path: a closed ledger with three known tx leaves yields the serialized
// header plus those leaves in tx-map iteration order.
func TestLedgerProvider_GetReplayDelta_ImmutableLedger(t *testing.T) {
	// SHAMap leaves require >= 12 bytes; pad the blobs accordingly. The
	// exact contents are opaque to the provider — what matters for this
	// test is that distinct keys yield distinct, recoverable blobs.
	txs := []struct {
		key  [32]byte
		blob []byte
	}{
		{fixedKey32(1), []byte("tx-blob-one--padded")},
		{fixedKey32(2), []byte("tx-blob-two--padded")},
		{fixedKey32(3), []byte("tx-blob-three-padded")},
	}
	closed := makeClosedLedgerWithTxs(t, txs)
	require.True(t, closed.IsImmutable(), "closed ledger must be immutable")

	lookup := newFakeLookup()
	lookup.add(closed)
	provider := newLedgerProviderForTest(lookup)

	hash := closed.Hash()
	headerBytes, leaves, err := provider.GetReplayDelta(hash[:])
	require.NoError(t, err)
	// Mirror rippled's `addRaw(info, s)` (includeHash=false) at
	// LedgerReplayMsgHandler.cpp:207 — the hash is conveyed via the
	// ledger_hash field of the response, not appended to the header
	// body. Including it would defeat the receiver's hash recompute.
	assert.Len(t, headerBytes, header.SizeBase,
		"serve-side replay-delta header must use hash-less encoding")
	require.Len(t, leaves, len(txs),
		"all tx leaves must be returned")

	// SHAMap iteration order is by key (radix tree). Our test keys are
	// already monotonically increasing in their first byte, so the
	// expected order is the input order.
	for i, want := range txs {
		assert.Equal(t, want.blob, leaves[i],
			"leaf %d blob mismatch", i)
		// Defensive copy contract: provider must not share storage with
		// the SHAMap. Mutating the returned slice must not be observable
		// on a subsequent call.
		leaves[i][0] ^= 0xFF
	}
	_, leavesAgain, err := provider.GetReplayDelta(hash[:])
	require.NoError(t, err)
	for i, want := range txs {
		assert.Equal(t, want.blob, leavesAgain[i],
			"leaf %d must be unchanged after caller-side mutation (defensive copy)", i)
	}
}

// TestLedgerProvider_GetReplayDelta_UnknownLedger verifies that a hash
// the lookup doesn't recognize yields (nil, nil, nil) — the documented
// contract for "unknown / not immutable".
func TestLedgerProvider_GetReplayDelta_UnknownLedger(t *testing.T) {
	lookup := newFakeLookup()
	provider := newLedgerProviderForTest(lookup)

	random := fixedKey32(0xAA)
	header, leaves, err := provider.GetReplayDelta(random[:])
	require.NoError(t, err)
	assert.Nil(t, header)
	assert.Nil(t, leaves)
}

// TestLedgerProvider_GetReplayDelta_OpenLedgerRefused verifies that an
// open (mutable) ledger is treated as "not immutable" and refused —
// mirrors rippled's `!ledger->isImmutable()` early-return.
func TestLedgerProvider_GetReplayDelta_OpenLedgerRefused(t *testing.T) {
	open := makeOpenLedger(t)
	require.False(t, open.IsImmutable(), "freshly opened ledger must not be immutable")

	// An open ledger has no real hash yet (hash is computed in Close()),
	// so we install it under a synthetic hash to make the lookup succeed
	// and isolate the immutability check as the cause of the refusal.
	synthetic := fixedKey32(0xCC)
	lookup := newFakeLookup()
	lookup.byHash[synthetic] = open
	provider := newLedgerProviderForTest(lookup)

	header, leaves, err := provider.GetReplayDelta(synthetic[:])
	require.NoError(t, err)
	assert.Nil(t, header,
		"open ledger must be refused (mirrors rippled's !isImmutable check)")
	assert.Nil(t, leaves)
}

// TestLedgerProvider_GetProofPath_TxMap_Existing verifies that requesting
// a proof for a key present in the tx map yields a non-empty leaf-to-root
// path along with the serialized header.
func TestLedgerProvider_GetProofPath_TxMap_Existing(t *testing.T) {
	txs := []struct {
		key  [32]byte
		blob []byte
	}{
		{fixedKey32(1), []byte("tx-leaf-data")},
	}
	closed := makeClosedLedgerWithTxs(t, txs)

	lookup := newFakeLookup()
	lookup.add(closed)
	provider := newLedgerProviderForTest(lookup)

	hash := closed.Hash()
	header, path, err := provider.GetProofPath(hash[:], txs[0].key[:], message.LedgerMapTransaction)
	require.NoError(t, err)
	assert.Equal(t, closed.SerializeHeader(), header)
	require.NotEmpty(t, path, "proof path for an existing key must be non-empty")
}

// TestLedgerProvider_GetProofPath_StateMap_Existing verifies the same for
// the account-state map. Genesis seeds the state map with the master
// account SLE plus a few system entries; we discover one of those keys
// dynamically rather than hard-coding it (genesis layout is not part of
// this test's contract).
func TestLedgerProvider_GetProofPath_StateMap_Existing(t *testing.T) {
	closed := makeGenesisLedger(t)
	require.True(t, closed.IsImmutable(),
		"genesis must be immutable so we can take a snapshot for key discovery")

	// Pull any one key from the state map to use as the proof target.
	var targetKey [32]byte
	var found bool
	require.NoError(t, closed.ForEach(func(key [32]byte, _ []byte) bool {
		targetKey = key
		found = true
		return false // stop after the first entry
	}))
	require.True(t, found, "genesis state map must contain at least one entry")

	lookup := newFakeLookup()
	lookup.add(closed)
	provider := newLedgerProviderForTest(lookup)

	hash := closed.Hash()
	header, path, err := provider.GetProofPath(hash[:], targetKey[:], message.LedgerMapAccountState)
	require.NoError(t, err)
	assert.Equal(t, closed.SerializeHeader(), header)
	require.NotEmpty(t, path, "proof path for an existing state key must be non-empty")
}

// TestLedgerProvider_GetProofPath_KeyAbsent verifies that a key with no
// leaf in the selected map yields ErrKeyNotFound — handler maps this to
// reNO_NODE without packing a header.
func TestLedgerProvider_GetProofPath_KeyAbsent(t *testing.T) {
	closed := makeGenesisLedger(t)
	lookup := newFakeLookup()
	lookup.add(closed)
	provider := newLedgerProviderForTest(lookup)

	missing := fixedKey32(0xEE) // not in genesis state map
	hash := closed.Hash()
	header, path, err := provider.GetProofPath(hash[:], missing[:], message.LedgerMapAccountState)
	require.ErrorIs(t, err, peermanagement.ErrKeyNotFound)
	assert.Nil(t, header)
	assert.Nil(t, path)
}

// TestLedgerProvider_GetProofPath_UnknownLedger verifies that an unknown
// ledger hash yields ErrLedgerNotFound — handler maps this to reNO_LEDGER.
func TestLedgerProvider_GetProofPath_UnknownLedger(t *testing.T) {
	lookup := newFakeLookup()
	provider := newLedgerProviderForTest(lookup)

	random := fixedKey32(0xAA)
	someKey := fixedKey32(0x11)
	header, path, err := provider.GetProofPath(random[:], someKey[:], message.LedgerMapAccountState)
	require.ErrorIs(t, err, peermanagement.ErrLedgerNotFound)
	assert.Nil(t, header)
	assert.Nil(t, path)
}

// TestLedgerProvider_GetProofPath_InvalidMapType verifies that an unknown
// map type yields a non-sentinel error so the handler emits reBAD_REQUEST.
// The handler validates the type up front, so this is defense-in-depth.
func TestLedgerProvider_GetProofPath_InvalidMapType(t *testing.T) {
	closed := makeGenesisLedger(t)
	lookup := newFakeLookup()
	lookup.add(closed)
	provider := newLedgerProviderForTest(lookup)

	hash := closed.Hash()
	someKey := fixedKey32(0x11)
	const bogus message.LedgerMapType = 99

	header, path, err := provider.GetProofPath(hash[:], someKey[:], bogus)
	require.Error(t, err, "invalid map type must surface an error")
	assert.NotErrorIs(t, err, peermanagement.ErrLedgerNotFound,
		"invalid map type must NOT report ErrLedgerNotFound — that would mislead the handler")
	assert.NotErrorIs(t, err, peermanagement.ErrKeyNotFound,
		"invalid map type must NOT report ErrKeyNotFound either")
	assert.Nil(t, header)
	assert.Nil(t, path)
}
