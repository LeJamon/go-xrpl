package adaptor

import (
	"context"
	"errors"
	"fmt"
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

type cancelOnNthErrContext struct {
	cancelAt int
	calls    int
	canceled bool
}

func (c *cancelOnNthErrContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *cancelOnNthErrContext) Done() <-chan struct{}       { return nil }
func (c *cancelOnNthErrContext) Value(any) any               { return nil }

func (c *cancelOnNthErrContext) Err() error {
	c.calls++
	if c.calls >= c.cancelAt {
		c.canceled = true
		return context.Canceled
	}
	return nil
}

// fakeLookup is a hand-rolled ledgerLookup double. It maps a ledger hash to
// a *ledger.Ledger so each test can inject the exact graph it wants without
// spinning up a full *service.Service. Sequence-based lookups are used by
// only one production code path (mtGET_LEDGER fallback) and are not
// exercised by the LedgerProvider contract tests, so we leave it minimal.
type fakeLookup struct {
	byHash        map[[32]byte]*ledger.Ledger
	earliestFetch uint32
}

type contextErrorLookup struct {
	*fakeLookup
	err          error
	beforeReturn func()
}

func (f *contextErrorLookup) GetLedgerByHashContext(context.Context, [32]byte) (*ledger.Ledger, error) {
	if f.beforeReturn != nil {
		f.beforeReturn()
	}
	return nil, f.err
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

func (f *fakeLookup) EarliestFetch() uint32 { return f.earliestFetch }

type contextFakeLookup struct {
	*fakeLookup
	contextCalls int
}

func (f *contextFakeLookup) GetLedgerByHashContext(_ context.Context, hash [32]byte) (*ledger.Ledger, error) {
	f.contextCalls++
	return f.GetLedgerByHash(hash)
}

// makeGenesisLedger returns a genesis-derived, validated (and therefore
// immutable) ledger. It is the cheapest "real" ledger we can hand the
// provider for tests that don't need transactions in the tx map.
func makeGenesisLedger(t *testing.T) *ledger.Ledger {
	t.Helper()
	res, err := genesis.Create(genesis.DefaultConfig())
	require.NoError(t, err)
	l, err := ledger.FromGenesis(res.Header, res.StateMap, res.TxMap, drops.Fees{})
	require.NoError(t, err)
	return l
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

func TestLedgerProvider_ContextMethodsUseCancellableLedgerLookup(t *testing.T) {
	closed := makeGenesisLedger(t)
	lookup := &contextFakeLookup{fakeLookup: newFakeLookup()}
	lookup.add(closed)
	provider := newLedgerProviderForTest(lookup)
	hash := closed.Hash()

	headerBytes, _, err := provider.GetReplayDeltaContext(context.Background(), hash[:])
	require.NoError(t, err)
	require.NotEmpty(t, headerBytes)
	var key [32]byte
	found := false
	require.NoError(t, closed.ForEach(func(candidate [32]byte, _ []byte) bool {
		key = candidate
		found = true
		return false
	}))
	require.True(t, found)
	_, _, err = provider.GetProofPathContext(context.Background(), hash[:], key[:], message.LedgerMapAccountState)
	require.NoError(t, err)
	assert.Equal(t, 2, lookup.contextCalls,
		"context serving paths must use the cancellable ledger lookup")
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

func TestLedgerProvider_GetReplayDelta_ContextCancellationNeverReturnsPartialDelta(t *testing.T) {
	closed := makeClosedLedgerWithTxs(t, []struct {
		key  [32]byte
		blob []byte
	}{
		{fixedKey32(1), []byte("tx-blob-one--padded")},
		{fixedKey32(2), []byte("tx-blob-two--padded")},
	})
	lookup := newFakeLookup()
	lookup.add(closed)
	provider := newLedgerProviderForTest(lookup)
	hash := closed.Hash()

	for cancelAt := 2; cancelAt <= 256; cancelAt++ {
		ctx := &cancelOnNthErrContext{cancelAt: cancelAt}
		headerBytes, leaves, err := provider.GetReplayDeltaContext(ctx, hash[:])
		if !ctx.canceled {
			continue
		}
		require.ErrorIs(t, err, context.Canceled, "cancelAt=%d", cancelAt)
		assert.Nil(t, headerBytes, "cancelAt=%d", cancelAt)
		assert.Nil(t, leaves, "cancelAt=%d", cancelAt)
	}
}

func TestLedgerProvider_GetReplayDelta_PropagatesLookupCancellation(t *testing.T) {
	provider := newLedgerProviderForTest(&contextErrorLookup{
		fakeLookup: newFakeLookup(),
		err:        fmt.Errorf("lookup canceled: %w", context.Canceled),
	})
	headerBytes, leaves, err := provider.GetReplayDeltaContext(context.Background(), make([]byte, 32))
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, headerBytes)
	assert.Nil(t, leaves)
}

func TestLedgerProvider_GetProofPath_PropagatesLookupDeadline(t *testing.T) {
	provider := newLedgerProviderForTest(&contextErrorLookup{
		fakeLookup: newFakeLookup(),
		err:        fmt.Errorf("lookup timed out: %w", context.DeadlineExceeded),
	})
	headerBytes, path, err := provider.GetProofPathContext(
		context.Background(), make([]byte, 32), make([]byte, 32), message.LedgerMapAccountState,
	)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Nil(t, headerBytes)
	assert.Nil(t, path)
}

func TestLedgerProvider_ContextCancellationWinsOverLookupErrorTranslation(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*LedgerProvider, context.Context) error
	}{
		{
			name: "replay delta",
			call: func(provider *LedgerProvider, ctx context.Context) error {
				_, _, err := provider.GetReplayDeltaContext(ctx, make([]byte, 32))
				return err
			},
		},
		{
			name: "proof path",
			call: func(provider *LedgerProvider, ctx context.Context) error {
				_, _, err := provider.GetProofPathContext(
					ctx, make([]byte, 32), make([]byte, 32), message.LedgerMapAccountState,
				)
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			provider := newLedgerProviderForTest(&contextErrorLookup{
				fakeLookup:   newFakeLookup(),
				err:          errors.New("storage unavailable"),
				beforeReturn: cancel,
			})
			require.ErrorIs(t, tc.call(provider, ctx), context.Canceled)
		})
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
	headerBytes, path, err := provider.GetProofPath(hash[:], txs[0].key[:], message.LedgerMapTransaction)
	require.NoError(t, err)
	assert.Equal(t, header.AddRaw(closed.Header(), false), headerBytes)
	assert.Len(t, headerBytes, header.SizeBase)
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
	headerBytes, path, err := provider.GetProofPath(hash[:], targetKey[:], message.LedgerMapAccountState)
	require.NoError(t, err)
	assert.Equal(t, header.AddRaw(closed.Header(), false), headerBytes)
	assert.Len(t, headerBytes, header.SizeBase)
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
// ledger hash yields errLedgerNotFound — handler maps this to reNO_LEDGER.
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
		"invalid map type must NOT report errLedgerNotFound — that would mislead the handler")
	assert.NotErrorIs(t, err, peermanagement.ErrKeyNotFound,
		"invalid map type must NOT report ErrKeyNotFound either")
	assert.Nil(t, header)
	assert.Nil(t, path)
}
