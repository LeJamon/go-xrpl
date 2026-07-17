package state

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestDeleteOfferRemovesEveryBookDirectory(t *testing.T) {
	t.Parallel()

	view := newStubView()
	var owner [20]byte
	owner[19] = 1
	var issuer [20]byte
	issuer[19] = 2
	offerKey := keylet.Offer(owner, 7)
	primary := keylet.Keylet{Key: itemKeyN(10)}
	additional := []keylet.Keylet{
		{Key: itemKeyN(11)},
		{Key: itemKeyN(12)},
	}

	ownerDir := keylet.OwnerDir(owner)
	_, err := DirInsert(view, ownerDir, offerKey.Key, false, nil)
	require.NoError(t, err)
	_, err = DirInsert(view, primary, offerKey.Key, true, nil)
	require.NoError(t, err)
	for _, dir := range additional {
		_, err = DirInsert(view, dir, offerKey.Key, true, nil)
		require.NoError(t, err)
	}
	books := make([]any, 0, len(additional))
	for _, dir := range additional {
		books = append(books, map[string]any{
			"Book": map[string]any{
				"BookDirectory": strings.ToUpper(hex.EncodeToString(dir.Key[:])),
				"BookNode":      "0",
			},
		})
	}
	offer := &LedgerOffer{
		Account:                 EncodeAccountIDSafe(owner),
		Sequence:                7,
		TakerPays:               NewIssuedAmountFromValue(1_000_000_000_000_000, -15, "USD", EncodeAccountIDSafe(issuer)),
		TakerGets:               NewXRPAmountFromInt(1_000_000),
		BookDirectory:           primary.Key,
		AdditionalBookDirectory: additional[0].Key,
		decodedOptionals: map[string]any{
			"AdditionalBooks":         books,
			"AdditionalBookDirectory": additional[0].Key,
			"AdditionalBookNode":      uint64(0),
		},
	}
	data, err := SerializeLedgerOffer(offer)
	require.NoError(t, err)
	parsed, err := ParseLedgerOffer(data)
	require.NoError(t, err)
	require.NoError(t, view.Insert(offerKey, data))

	removed, err := DeleteOffer(view, offerKey, parsed)
	require.NoError(t, err)
	require.True(t, removed)

	for _, dir := range append([]keylet.Keylet{ownerDir, primary}, additional...) {
		exists, err := view.Exists(dir)
		require.NoError(t, err)
		require.False(t, exists)
	}
	exists, err := view.Exists(offerKey)
	require.NoError(t, err)
	require.False(t, exists)
}

func TestDeleteOfferStopsAfterMissingAdditionalDirectoryEntry(t *testing.T) {
	t.Parallel()

	view := newStubView()
	var owner [20]byte
	owner[19] = 1
	offerKey := keylet.Offer(owner, 7)
	ownerDir := keylet.OwnerDir(owner)
	primary := keylet.Keylet{Key: itemKeyN(10)}
	additional := keylet.Keylet{Key: itemKeyN(11)}

	_, err := DirInsert(view, ownerDir, offerKey.Key, false, nil)
	require.NoError(t, err)
	_, err = DirInsert(view, primary, offerKey.Key, true, nil)
	require.NoError(t, err)
	require.NoError(t, view.Insert(offerKey, []byte{1}))

	offer := &LedgerOffer{
		Account:                 EncodeAccountIDSafe(owner),
		Sequence:                7,
		BookDirectory:           primary.Key,
		AdditionalBookDirectory: additional.Key,
	}
	removed, err := DeleteOffer(view, offerKey, offer)
	require.NoError(t, err)
	require.False(t, removed)

	for _, dir := range []keylet.Keylet{ownerDir, primary} {
		exists, err := view.Exists(dir)
		require.NoError(t, err)
		require.False(t, exists)
	}
	exists, err := view.Exists(offerKey)
	require.NoError(t, err)
	require.True(t, exists)
}
