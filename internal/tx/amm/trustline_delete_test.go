package amm

import (
	"bytes"
	"errors"
	"sort"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

type trustDeleteTestView struct {
	entries     map[[32]byte][]byte
	erased      map[[32]byte][]byte
	readErrorAt [32]byte
	readError   error
}

func (v *trustDeleteTestView) Read(k keylet.Keylet) ([]byte, error) {
	if v.readError != nil && k.Key == v.readErrorAt {
		return nil, v.readError
	}
	return v.entries[k.Key], nil
}

func (v *trustDeleteTestView) Exists(k keylet.Keylet) (bool, error) {
	_, ok := v.entries[k.Key]
	return ok, nil
}

func (v *trustDeleteTestView) Insert(k keylet.Keylet, data []byte) error {
	v.entries[k.Key] = bytes.Clone(data)
	return nil
}

func (v *trustDeleteTestView) Update(k keylet.Keylet, data []byte) error {
	v.entries[k.Key] = bytes.Clone(data)
	return nil
}

func (v *trustDeleteTestView) Erase(k keylet.Keylet) error {
	v.erased[k.Key] = bytes.Clone(v.entries[k.Key])
	delete(v.entries, k.Key)
	return nil
}

func (*trustDeleteTestView) AdjustDropsDestroyed(drops.XRPAmount) error { return nil }

func (v *trustDeleteTestView) ForEach(fn func([32]byte, []byte) bool) error {
	keys := v.sortedKeys()
	for _, key := range keys {
		if !fn(key, v.entries[key]) {
			break
		}
	}
	return nil
}

func (v *trustDeleteTestView) Succ(after [32]byte) ([32]byte, []byte, bool, error) {
	for _, key := range v.sortedKeys() {
		if bytes.Compare(key[:], after[:]) > 0 {
			return key, v.entries[key], true, nil
		}
	}
	return [32]byte{}, nil, false, nil
}

func (*trustDeleteTestView) TxExists([32]byte) (bool, error) { return false, nil }
func (*trustDeleteTestView) Rules() *amendment.Rules         { return nil }
func (*trustDeleteTestView) LedgerSeq() uint32               { return 1 }

func (v *trustDeleteTestView) sortedKeys() [][32]byte {
	keys := make([][32]byte, 0, len(v.entries))
	for key := range v.entries {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return bytes.Compare(keys[i][:], keys[j][:]) < 0
	})
	return keys
}

type trustDeleteFixture struct {
	view             *trustDeleteTestView
	lineKey          keylet.Keylet
	lineBytes        []byte
	rippleState      *state.RippleState
	lowID            [20]byte
	highID           [20]byte
	nonAMMOwnerCount uint32
}

func newTrustDeleteFixture(t *testing.T, lowHasLine, highHasLine bool) trustDeleteFixture {
	t.Helper()

	lowID := [20]byte{1}
	highID := [20]byte{2}
	lineKey := keylet.Line(lowID, highID, "USD")
	view := &trustDeleteTestView{
		entries: make(map[[32]byte][]byte),
		erased:  make(map[[32]byte][]byte),
	}

	lowAddress := state.EncodeAccountIDSafe(lowID)
	highAddress := state.EncodeAccountIDSafe(highID)
	const nonAMMOwnerCount = uint32(7)
	lowAccount := &state.AccountRoot{
		Account:    lowAddress,
		Balance:    1_000_000_000,
		Sequence:   1,
		OwnerCount: 2,
		AMMID:      [32]byte{1},
	}
	highAccount := &state.AccountRoot{
		Account:    highAddress,
		Balance:    1_000_000_000,
		Sequence:   1,
		OwnerCount: nonAMMOwnerCount,
	}
	insertTrustDeleteAccount(t, view, lowID, lowAccount)
	insertTrustDeleteAccount(t, view, highID, highAccount)

	rs := &state.RippleState{
		Balance:   state.NewIssuedAmountFromValue(0, state.MinExponent, "USD", state.AccountOneAddress),
		LowLimit:  state.NewIssuedAmountFromValue(0, state.MinExponent, "USD", lowAddress),
		HighLimit: state.NewIssuedAmountFromValue(0, state.MinExponent, "USD", highAddress),
		LowNode:   0,
		HighNode:  0,
		Flags:     state.LsfHighReserve,
	}
	lineBytes, err := state.SerializeRippleState(rs)
	require.NoError(t, err)
	require.NoError(t, view.Insert(lineKey, lineBytes))

	insertTrustDeleteDirectory(t, view, lowID, lineKey.Key, [32]byte{31: 1}, lowHasLine)
	insertTrustDeleteDirectory(t, view, highID, lineKey.Key, [32]byte{31: 2}, highHasLine)

	return trustDeleteFixture{
		view:             view,
		lineKey:          lineKey,
		lineBytes:        lineBytes,
		rippleState:      rs,
		lowID:            lowID,
		highID:           highID,
		nonAMMOwnerCount: nonAMMOwnerCount,
	}
}

func insertTrustDeleteAccount(t *testing.T, view *trustDeleteTestView, id [20]byte, account *state.AccountRoot) {
	t.Helper()
	data, err := state.SerializeAccountRoot(account)
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.Account(id), data))
}

func insertTrustDeleteDirectory(t *testing.T, view *trustDeleteTestView, owner [20]byte, lineKey, decoy [32]byte, includeLine bool) {
	t.Helper()
	indexes := [][32]byte{decoy}
	if includeLine {
		indexes = append(indexes, lineKey)
	}
	dirKey := keylet.OwnerDir(owner)
	data, err := state.SerializeDirectoryNode(&state.DirectoryNode{
		RootIndex: dirKey.Key,
		Indexes:   indexes,
		Owner:     owner,
	}, false)
	require.NoError(t, err)
	require.NoError(t, view.Insert(dirKey, data))
}

func assertTrustDeleteFailureState(t *testing.T, fixture trustDeleteFixture) {
	t.Helper()
	require.Equal(t, fixture.lineBytes, fixture.view.entries[fixture.lineKey.Key])
	accountData := fixture.view.entries[keylet.Account(fixture.highID).Key]
	account, err := state.ParseAccountRoot(accountData)
	require.NoError(t, err)
	require.Equal(t, fixture.nonAMMOwnerCount, account.OwnerCount)
}

func TestDeleteAMMTrustLineDirectoryMismatch(t *testing.T) {
	tests := []struct {
		name        string
		lowHasLine  bool
		highHasLine bool
	}{
		{name: "missing low", lowHasLine: false, highHasLine: true},
		{name: "missing high", lowHasLine: true, highHasLine: false},
		{name: "missing both", lowHasLine: false, highHasLine: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTrustDeleteFixture(t, test.lowHasLine, test.highHasLine)
			result := deleteAMMTrustLine(fixture.view, fixture.lineKey, fixture.rippleState, fixture.lowID)
			require.Equal(t, ter.TefBAD_LEDGER, result)
			assertTrustDeleteFailureState(t, fixture)
		})
	}
}

func TestDeleteAMMTrustLineStorageError(t *testing.T) {
	fixture := newTrustDeleteFixture(t, true, true)
	fixture.view.readErrorAt = keylet.OwnerDir(fixture.lowID).Key
	fixture.view.readError = errors.New("low directory read failed")

	result := deleteAMMTrustLine(fixture.view, fixture.lineKey, fixture.rippleState, fixture.lowID)
	require.Equal(t, ter.TefEXCEPTION, result)
	assertTrustDeleteFailureState(t, fixture)
}

func TestTrustDeletePreservesHighDirectoryReadError(t *testing.T) {
	fixture := newTrustDeleteFixture(t, true, true)
	injected := errors.New("high directory read failed")
	fixture.view.readErrorAt = keylet.OwnerDir(fixture.highID).Key
	fixture.view.readError = injected

	err := trustDelete(fixture.view, fixture.lineKey, fixture.lowID, fixture.highID, 0, 0)
	require.ErrorIs(t, err, injected)
	require.Equal(t, fixture.lineBytes, fixture.view.entries[fixture.lineKey.Key])

	lowDirectoryData := fixture.view.entries[keylet.OwnerDir(fixture.lowID).Key]
	lowDirectory, parseErr := state.ParseDirectoryNode(lowDirectoryData)
	require.NoError(t, parseErr)
	require.NotContains(t, lowDirectory.Indexes, fixture.lineKey.Key)
}

func TestTrustDeleteClearsSponsorsBeforeErase(t *testing.T) {
	fixture := newTrustDeleteFixture(t, true, true)
	fixture.rippleState.LowSponsor = state.EncodeAccountIDSafe([20]byte{3})
	fixture.rippleState.HighSponsor = state.EncodeAccountIDSafe([20]byte{4})
	lineBytes, err := state.SerializeRippleState(fixture.rippleState)
	require.NoError(t, err)
	require.NoError(t, fixture.view.Update(fixture.lineKey, lineBytes))

	require.NoError(t, trustDelete(fixture.view, fixture.lineKey, fixture.lowID, fixture.highID, 0, 0))
	require.NotContains(t, fixture.view.entries, fixture.lineKey.Key)

	erasedLine, err := state.ParseRippleState(fixture.view.erased[fixture.lineKey.Key])
	require.NoError(t, err)
	require.Empty(t, erasedLine.LowSponsor)
	require.Empty(t, erasedLine.HighSponsor)
}
