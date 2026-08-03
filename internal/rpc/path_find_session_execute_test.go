package rpc

import (
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

type pathFindSessionLedger struct {
	entries map[[32]byte][]byte
}

func newPathFindSessionLedger() *pathFindSessionLedger {
	return &pathFindSessionLedger{entries: make(map[[32]byte][]byte)}
}

func (m *pathFindSessionLedger) Read(k keylet.Keylet) ([]byte, error) {
	return m.entries[k.Key], nil
}

func (m *pathFindSessionLedger) Exists(k keylet.Keylet) (bool, error) {
	_, ok := m.entries[k.Key]
	return ok, nil
}

func (m *pathFindSessionLedger) Insert(k keylet.Keylet, data []byte) error {
	m.entries[k.Key] = data
	return nil
}

func (m *pathFindSessionLedger) Update(k keylet.Keylet, data []byte) error {
	m.entries[k.Key] = data
	return nil
}

func (m *pathFindSessionLedger) Erase(k keylet.Keylet) error {
	delete(m.entries, k.Key)
	return nil
}

func (m *pathFindSessionLedger) AdjustDropsDestroyed(drops.XRPAmount) error { return nil }

func (m *pathFindSessionLedger) ForEach(fn func([32]byte, []byte) bool) error {
	for key, data := range m.entries {
		if !fn(key, data) {
			break
		}
	}
	return nil
}

func (m *pathFindSessionLedger) Succ(key [32]byte) ([32]byte, []byte, bool, error) {
	var best [32]byte
	var bestData []byte
	found := false
	for candidate, data := range m.entries {
		if pathFindSessionCompareKeys(candidate, key) <= 0 {
			continue
		}
		if !found || pathFindSessionCompareKeys(candidate, best) < 0 {
			best = candidate
			bestData = data
			found = true
		}
	}
	return best, bestData, found, nil
}

func (m *pathFindSessionLedger) TxExists([32]byte) (bool, error) { return false, nil }

func (m *pathFindSessionLedger) Rules() *amendment.Rules { return nil }

func (m *pathFindSessionLedger) LedgerSeq() uint32 { return 0 }

func pathFindSessionCompareKeys(a, b [32]byte) int {
	for i := range a {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

func pathFindSessionAccount(seed byte) [20]byte {
	var account [20]byte
	account[0] = seed
	for i := 1; i < len(account); i++ {
		account[i] = seed + byte(i)
	}
	return account
}

func pathFindSessionAccountAddress(account [20]byte) string {
	return state.EncodeAccountIDSafe(account)
}

func pathFindSessionAddAccount(t *testing.T, ledger *pathFindSessionLedger, account [20]byte) {
	t.Helper()
	data, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:  pathFindSessionAccountAddress(account),
		Balance:  10_000_000_000,
		Sequence: 1,
	})
	require.NoError(t, err)
	ledger.entries[keylet.Account(account).Key] = data
}

func pathFindSessionAddRippleState(
	t *testing.T,
	ledger *pathFindSessionLedger,
	lowAccount, highAccount [20]byte,
	currency string,
	balance float64,
) {
	t.Helper()
	data, err := state.SerializeRippleState(&state.RippleState{
		Balance:   state.NewIssuedAmountFromFloat64(balance, currency, state.AccountOneAddress),
		LowLimit:  state.NewIssuedAmountFromFloat64(10000, currency, pathFindSessionAccountAddress(lowAccount)),
		HighLimit: state.NewIssuedAmountFromFloat64(10000, currency, pathFindSessionAccountAddress(highAccount)),
	})
	require.NoError(t, err)
	lineKey := keylet.Line(lowAccount, highAccount, currency)
	ledger.entries[lineKey.Key] = data
	pathFindSessionAddOwnerDirectoryEntry(t, ledger, lowAccount, lineKey.Key)
	pathFindSessionAddOwnerDirectoryEntry(t, ledger, highAccount, lineKey.Key)
}

func pathFindSessionAddOwnerDirectoryEntry(
	t *testing.T,
	ledger *pathFindSessionLedger,
	account [20]byte,
	itemKey [32]byte,
) {
	t.Helper()
	directoryKey := keylet.OwnerDir(account)
	directory := &state.DirectoryNode{
		Owner:     account,
		RootIndex: directoryKey.Key,
		Indexes:   [][32]byte{itemKey},
	}
	if existing, ok := ledger.entries[directoryKey.Key]; ok {
		var err error
		directory, err = state.ParseDirectoryNode(existing)
		require.NoError(t, err)
		for _, existingKey := range directory.Indexes {
			if existingKey == itemKey {
				return
			}
		}
		directory.Indexes = append(directory.Indexes, itemKey)
	}
	data, err := state.SerializeDirectoryNode(directory, false)
	require.NoError(t, err)
	ledger.entries[directoryKey.Key] = data
}

func pathFindSessionAddDomain(t *testing.T, ledger *pathFindSessionLedger, domainID [32]byte, owner [20]byte) {
	t.Helper()
	data, err := state.SerializePermissionedDomain(&state.PermissionedDomainData{
		Owner:    owner,
		Sequence: 1,
	}, pathFindSessionAccountAddress(owner))
	require.NoError(t, err)
	ledger.entries[keylet.PermissionedDomainByID(domainID).Key] = data
}

func pathFindSessionBookSide(amount state.Amount) keylet.BookSide {
	issue := payment.GetIssue(amount)
	return keylet.IssueSide(keylet.CurrencyBytes(issue.Currency), issue.Issuer)
}

func pathFindSessionAddOffer(
	t *testing.T,
	ledger *pathFindSessionLedger,
	account [20]byte,
	sequence uint32,
	takerPays, takerGets state.Amount,
	domainID [32]byte,
) (offerKey, bookKey [32]byte) {
	t.Helper()
	var domainPtr *[32]byte
	if domainID != ([32]byte{}) {
		domainPtr = &domainID
	}
	quality := payment.QualityFromAmounts(
		payment.ToEitherAmount(takerPays),
		payment.ToEitherAmount(takerGets),
	)
	bookKey = keylet.Quality(keylet.BookBase(
		pathFindSessionBookSide(takerPays),
		pathFindSessionBookSide(takerGets),
		domainPtr,
	), quality.Value).Key
	offer := &state.LedgerOffer{
		Account:       pathFindSessionAccountAddress(account),
		Sequence:      sequence,
		TakerPays:     takerPays,
		TakerGets:     takerGets,
		BookDirectory: bookKey,
		DomainID:      domainID,
	}
	offerData, err := state.SerializeLedgerOffer(offer)
	require.NoError(t, err)
	offerKey = keylet.Offer(account, sequence).Key
	ledger.entries[offerKey] = offerData
	directoryData, err := state.SerializeDirectoryNode(&state.DirectoryNode{
		RootIndex: bookKey,
		Indexes:   [][32]byte{offerKey},
	}, true)
	require.NoError(t, err)
	ledger.entries[bookKey] = directoryData
	return offerKey, bookKey
}

func TestPathFindSessionExecuteRetainsDomain(t *testing.T) {
	ledger := newPathFindSessionLedger()
	source := pathFindSessionAccount(1)
	destination := pathFindSessionAccount(2)
	issuer := pathFindSessionAccount(3)
	targetOwner := pathFindSessionAccount(4)
	unrelatedOwner := pathFindSessionAccount(5)
	openOwner := pathFindSessionAccount(6)
	for _, account := range [][20]byte{source, destination, issuer, targetOwner, unrelatedOwner, openOwner} {
		pathFindSessionAddAccount(t, ledger, account)
	}

	low, high := destination, issuer
	if state.CompareAccountIDs(low, high) > 0 {
		low, high = high, low
	}
	pathFindSessionAddRippleState(t, ledger, low, high, "USD", 0)
	for _, owner := range [][20]byte{targetOwner, unrelatedOwner, openOwner} {
		low, high = owner, issuer
		if state.CompareAccountIDs(low, high) > 0 {
			low, high = high, low
		}
		balance := -500.0
		if low == owner {
			balance = 500
		}
		pathFindSessionAddRippleState(t, ledger, low, high, "USD", balance)
	}

	var targetDomain, unrelatedDomain [32]byte
	targetDomain[31] = 1
	unrelatedDomain[31] = 2
	pathFindSessionAddDomain(t, ledger, targetDomain, targetOwner)
	pathFindSessionAddDomain(t, ledger, unrelatedDomain, unrelatedOwner)

	takerPays := state.NewXRPAmountFromInt(10_000_000)
	takerGets := state.NewIssuedAmountFromFloat64(100, "USD", pathFindSessionAccountAddress(issuer))
	targetOffer, targetBook := pathFindSessionAddOffer(t, ledger, targetOwner, 1, takerPays, takerGets, targetDomain)
	pathFindSessionAddOffer(t, ledger, unrelatedOwner, 1, takerPays, takerGets, unrelatedDomain)
	pathFindSessionAddOffer(t, ledger, openOwner, 1, takerPays, takerGets, [32]byte{})

	baseParams := `{"source_account":"` + pathFindSessionAccountAddress(source) +
		`","destination_account":"` + pathFindSessionAccountAddress(destination) +
		`","destination_amount":{"currency":"USD","issuer":"` + pathFindSessionAccountAddress(issuer) +
		`","value":"50"},"source_currencies":[{"currency":"XRP"}]`
	params := baseParams + `,"domain":"` + hex.EncodeToString(targetDomain[:]) + `"}`
	session, rpcErr := ParseAndCreateSession([]byte(params), "session-id")
	require.Nil(t, rpcErr)

	initial := session.Execute(ledger, false)
	require.False(t, initial.FullReply)
	require.Empty(t, initial.Type)
	require.Len(t, initial.Alternatives, 1)
	require.Equal(t, `"5000000"`, string(initial.Alternatives[0].SourceAmount))

	delete(ledger.entries, targetOffer)
	delete(ledger.entries, targetBook)
	unscopedSession, rpcErr := ParseAndCreateSession([]byte(baseParams+`}`), "unscoped")
	require.Nil(t, rpcErr)
	unscoped := unscopedSession.Execute(ledger, false)
	require.NotEmpty(t, unscoped.Alternatives,
		"the open/unrelated fixture must provide liquidity when no domain is requested")

	refresh := session.Execute(ledger, true)
	require.True(t, refresh.FullReply)
	require.Equal(t, "path_find", refresh.Type)
	require.Empty(t, refresh.Alternatives,
		"the refresh must not use open or unrelated-domain offers after target liquidity is removed")
}
