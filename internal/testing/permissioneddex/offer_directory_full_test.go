package permissioneddex

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	offerBuilder "github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

const fullOfferDirectoryPageEntries = 32

type fullOfferDirectory struct {
	rootKey  keylet.Keylet
	lastKey  keylet.Keylet
	rootData []byte
	lastData []byte
}

func newDirectoryLimitDEX(t *testing.T) (*jtx.TestEnv, *PermissionedDEXEnv) {
	t.Helper()
	env := jtx.NewTestEnv(t)
	dex := SetupPermissionedDEX(t, env)
	env.DisableFeature("fixDirectoryLimit")
	env.Close()
	return env, dex
}

func offerDirectoryKey(env *jtx.TestEnv, takerPays, takerGets tx.Amount, domainID *[32]byte) keylet.Keylet {
	bookBase := keylet.BookBase(
		keylet.IssueSide(keylet.CurrencyBytes(takerPays.Currency), state.GetIssuerBytes(takerPays.Issuer)),
		keylet.IssueSide(keylet.CurrencyBytes(takerGets.Currency), state.GetIssuerBytes(takerGets.Issuer)),
		domainID,
	)
	rate := state.GetRateWithNumberContext(
		takerGets,
		takerPays,
		tx.NumberContextForRules(env.Ledger().Rules()),
	)
	return keylet.Quality(bookBase, rate)
}

func saturateOfferDirectory(
	t *testing.T,
	env *jtx.TestEnv,
	dirKey keylet.Keylet,
	isBook bool,
	setup func(*state.DirectoryNode),
) fullOfferDirectory {
	t.Helper()

	lastPage := state.DirNodeMaxPages - 1
	root := &state.DirectoryNode{RootIndex: dirKey.Key}
	root.SetIndexNext(lastPage)
	root.SetIndexPrevious(lastPage)
	last := &state.DirectoryNode{
		RootIndex: dirKey.Key,
		Indexes:   make([][32]byte, fullOfferDirectoryPageEntries),
	}
	for i := range last.Indexes {
		last.Indexes[i][0] = byte(i + 1)
	}
	if setup != nil {
		setup(root)
		setup(last)
	}

	rootData, err := state.SerializeDirectoryNode(root, isBook)
	require.NoError(t, err)
	lastData, err := state.SerializeDirectoryNode(last, isBook)
	require.NoError(t, err)
	lastKey := keylet.DirPage(dirKey.Key, lastPage)
	if env.LedgerEntryExists(dirKey) {
		require.NoError(t, env.Ledger().Update(dirKey, rootData))
	} else {
		require.NoError(t, env.Ledger().Insert(dirKey, rootData))
	}
	require.NoError(t, env.Ledger().Insert(lastKey, lastData))

	return fullOfferDirectory{
		rootKey:  dirKey,
		lastKey:  lastKey,
		rootData: rootData,
		lastData: lastData,
	}
}

func setOfferBookDirectory(
	dir *state.DirectoryNode,
	takerPays, takerGets tx.Amount,
	rate uint64,
	domainID *[32]byte,
) {
	dir.TakerPaysCurrency = keylet.CurrencyBytes(takerPays.Currency)
	dir.TakerPaysIssuer = state.GetIssuerBytes(takerPays.Issuer)
	dir.TakerGetsCurrency = keylet.CurrencyBytes(takerGets.Currency)
	dir.TakerGetsIssuer = state.GetIssuerBytes(takerGets.Issuer)
	dir.ExchangeRate = rate
	if domainID != nil {
		dir.DomainID = *domainID
	}
}

func requireOfferDirectoryUnchanged(t *testing.T, env *jtx.TestEnv, dir fullOfferDirectory) {
	t.Helper()
	rootData, err := env.LedgerEntry(dir.rootKey)
	require.NoError(t, err)
	require.Equal(t, dir.rootData, rootData)
	lastData, err := env.LedgerEntry(dir.lastKey)
	require.NoError(t, err)
	require.Equal(t, dir.lastData, lastData)
}

func requireOfferCreateClaimOnly(
	t *testing.T,
	env *jtx.TestEnv,
	account *jtx.Account,
	sequence uint32,
	balance uint64,
	ownerCount uint32,
) {
	t.Helper()
	env.Close()
	jtx.RequireLedgerEntryNotExists(t, env, keylet.Offer(account.ID, sequence))
	jtx.RequireOwnerCount(t, env, account, ownerCount)
	jtx.RequireBalance(t, env, account, balance-env.BaseFee())
	jtx.RequireSequence(t, env, account, sequence+1)
}

func TestOfferCreateOwnerDirectoryFullClaim(t *testing.T) {
	env, dex := newDirectoryLimitDEX(t)
	takerPays := dex.USD(10)
	takerGets := jtx.XRPTxAmount(10_000_000)
	bookDir := offerDirectoryKey(env, takerPays, takerGets, nil)
	ownerDir := saturateOfferDirectory(t, env, keylet.OwnerDir(dex.Bob.ID), false, func(dir *state.DirectoryNode) {
		dir.Owner = dex.Bob.ID
	})
	sequence := env.Seq(dex.Bob)
	balance := env.Balance(dex.Bob)
	ownerCount := env.OwnerCount(dex.Bob)

	result := env.Submit(offerBuilder.OfferCreate(dex.Bob, takerPays, takerGets).Build())
	jtx.RequireTxClaimed(t, result, jtx.TecDIR_FULL)
	requireOfferCreateClaimOnly(t, env, dex.Bob, sequence, balance, ownerCount)

	requireOfferDirectoryUnchanged(t, env, ownerDir)
	jtx.RequireLedgerEntryNotExists(t, env, bookDir)
}

func TestOfferCreateBookDirectoryFullClaim(t *testing.T) {
	env, dex := newDirectoryLimitDEX(t)
	takerPays := dex.USD(10)
	takerGets := jtx.XRPTxAmount(10_000_000)
	bookDirKey := offerDirectoryKey(env, takerPays, takerGets, nil)
	rate := state.GetRateWithNumberContext(takerGets, takerPays, tx.NumberContextForRules(env.Ledger().Rules()))
	bookDir := saturateOfferDirectory(t, env, bookDirKey, true, func(dir *state.DirectoryNode) {
		setOfferBookDirectory(dir, takerPays, takerGets, rate, nil)
	})
	ownerDirBefore, err := env.LedgerEntry(keylet.OwnerDir(dex.Bob.ID))
	require.NoError(t, err)
	sequence := env.Seq(dex.Bob)
	balance := env.Balance(dex.Bob)
	ownerCount := env.OwnerCount(dex.Bob)

	result := env.Submit(offerBuilder.OfferCreate(dex.Bob, takerPays, takerGets).Build())
	jtx.RequireTxClaimed(t, result, jtx.TecDIR_FULL)
	requireOfferCreateClaimOnly(t, env, dex.Bob, sequence, balance, ownerCount)

	requireOfferDirectoryUnchanged(t, env, bookDir)
	ownerDirAfter, err := env.LedgerEntry(keylet.OwnerDir(dex.Bob.ID))
	require.NoError(t, err)
	require.Equal(t, ownerDirBefore, ownerDirAfter)
}

func TestOfferCreateHybridOpenBookDirectoryFullClaim(t *testing.T) {
	env, dex := newDirectoryLimitDEX(t)
	takerPays := dex.USD(10)
	takerGets := jtx.XRPTxAmount(10_000_000)
	openBookDirKey := offerDirectoryKey(env, takerPays, takerGets, nil)
	domainBookDirKey := offerDirectoryKey(env, takerPays, takerGets, &dex.DomainID)
	rate := state.GetRateWithNumberContext(takerGets, takerPays, tx.NumberContextForRules(env.Ledger().Rules()))
	openBookDir := saturateOfferDirectory(t, env, openBookDirKey, true, func(dir *state.DirectoryNode) {
		setOfferBookDirectory(dir, takerPays, takerGets, rate, nil)
	})
	ownerDirBefore, err := env.LedgerEntry(keylet.OwnerDir(dex.Bob.ID))
	require.NoError(t, err)
	sequence := env.Seq(dex.Bob)
	balance := env.Balance(dex.Bob)
	ownerCount := env.OwnerCount(dex.Bob)

	result := env.Submit(
		offerBuilder.OfferCreate(dex.Bob, takerPays, takerGets).
			DomainID(dex.DomainID).
			Hybrid().
			Build(),
	)
	jtx.RequireTxClaimed(t, result, jtx.TecDIR_FULL)
	requireOfferCreateClaimOnly(t, env, dex.Bob, sequence, balance, ownerCount)

	requireOfferDirectoryUnchanged(t, env, openBookDir)
	jtx.RequireLedgerEntryNotExists(t, env, domainBookDirKey)
	ownerDirAfter, err := env.LedgerEntry(keylet.OwnerDir(dex.Bob.ID))
	require.NoError(t, err)
	require.Equal(t, ownerDirBefore, ownerDirAfter)
}
