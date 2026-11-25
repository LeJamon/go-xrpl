package nft_test

import (
	"bytes"
	"encoding/hex"
	"math"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/nft"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/applystate"
	"github.com/LeJamon/go-xrpl/internal/tx/nftoken"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/stretchr/testify/require"
)

// DirInsert follows the root's last-page link. These synthetic boundary pages
// exercise the limit without constructing a ledger history that fills the directory.
func fillNFTOfferDirectory(t *testing.T, env *jtx.TestEnv, key keylet.Keylet, identity state.DirectoryNode, lastPage uint64) {
	t.Helper()
	identity.RootIndex = key.Key
	root := identity
	root.SetIndexNext(lastPage)
	root.SetIndexPrevious(lastPage)
	rootData, err := state.SerializeDirectoryNode(&root, false)
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Insert(key, rootData))

	last := identity
	last.Indexes = make([][32]byte, 32)
	for i := range last.Indexes {
		last.Indexes[i][31] = byte(i + 1)
	}
	lastData, err := state.SerializeDirectoryNode(&last, false)
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Insert(keylet.DirPage(key.Key, lastPage), lastData))
}

func TestNFTokenOfferBeyondLegacyDirectoryLimit(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.EnableFeature("fixDirectoryLimit")
	seller := jtx.NewAccount("seller")
	buyer := jtx.NewAccount("buyer")
	env.Fund(seller)
	env.FundAmount(buyer, env.ReserveBase()+env.ReserveIncrement())
	env.Close()

	id := nft.GetNextNFTokenID(env, seller, 0, nftoken.NFTokenFlagTransferable, 0)
	jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenMint(seller, 0).Transferable().Build()))
	env.Close()

	dir := keylet.OwnerDir(buyer.ID)
	fillNFTOfferDirectory(t, env, dir, state.DirectoryNode{Owner: buyer.ID}, state.DirNodeMaxPages-1)
	sequence, balance := env.Seq(buyer), env.Balance(buyer)
	offerKey := keylet.NFTokenOffer(buyer.ID, sequence)
	result := env.Submit(nft.NFTokenCreateBuyOffer(buyer, id, tx.NewXRPAmount(1), seller).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	require.Equal(t, env.BaseFee(), result.Fee)
	jtx.RequireSequence(t, env, buyer, sequence+1)
	jtx.RequireBalance(t, env, buyer, balance-result.Fee)

	data, err := env.LedgerEntry(offerKey)
	require.NoError(t, err)
	offer, err := state.ParseNFTokenOffer(data)
	require.NoError(t, err)
	require.Equal(t, state.DirNodeMaxPages, offer.OwnerNode)
	data, err = env.LedgerEntry(keylet.DirPage(dir.Key, offer.OwnerNode))
	require.NoError(t, err)
	page, err := state.ParseDirectoryNode(data)
	require.NoError(t, err)
	require.Equal(t, [][32]byte{offerKey.Key}, page.Indexes)
}

func TestNFTokenOfferDirectoryFailure(t *testing.T) {
	type offerKind int
	const (
		sellOffer offerKind = iota
		buyOffer
		mintOffer
	)
	type directoryFault int
	const (
		wrongRootType directoryFault = iota + 1
		malformedRoot
		malformedLastPage
		missingLastPage
		occupiedNextPage
		occupiedOffer
	)
	for _, tc := range []struct {
		name           string
		kind           offerKind
		tokenDirectory bool
		fixLimit       bool
		lowReserve     bool
		fault          directoryFault
		want           ter.Result
	}{
		{name: "full owner directory", want: ter.TecDIR_FULL},
		{name: "full sell directory", tokenDirectory: true, want: ter.TecDIR_FULL},
		{name: "full buy directory", kind: buyOffer, tokenDirectory: true, want: ter.TecDIR_FULL},
		{name: "sell directory page overflow", tokenDirectory: true, fixLimit: true, want: ter.TecDIR_FULL},
		{name: "reserve before directory", lowReserve: true, want: ter.TecINSUFFICIENT_RESERVE},
		{name: "mint reserve before directory", kind: mintOffer, lowReserve: true, want: ter.TecINSUFFICIENT_RESERVE},
		{name: "mint with full owner directory", kind: mintOffer, want: ter.TecDIR_FULL},
		{name: "mint with full sell directory", kind: mintOffer, tokenDirectory: true, want: ter.TecDIR_FULL},
		{name: "wrong owner directory root type", fault: wrongRootType, want: ter.TefEXCEPTION},
		{name: "malformed owner directory root", fault: malformedRoot, want: ter.TefEXCEPTION},
		{name: "malformed sell directory page", tokenDirectory: true, fault: malformedLastPage, want: ter.TefEXCEPTION},
		{name: "missing sell directory page", tokenDirectory: true, fault: missingLastPage, want: ter.TefEXCEPTION},
		{name: "occupied next sell directory page", tokenDirectory: true, fault: occupiedNextPage, want: ter.TefEXCEPTION},
		{name: "occupied offer key", tokenDirectory: true, fault: occupiedOffer, want: ter.TefEXCEPTION},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			lastPage := state.DirNodeMaxPages - 1
			if tc.fixLimit {
				env.EnableFeature("fixDirectoryLimit")
				lastPage = math.MaxUint64
			} else {
				env.DisableFeature("fixDirectoryLimit")
			}
			if tc.fault == occupiedNextPage || tc.fault == occupiedOffer {
				lastPage = 1
			}
			alice := jtx.NewAccount("alice")
			env.Fund(alice)
			issuer := alice
			if tc.kind == buyOffer {
				issuer = jtx.NewAccount("issuer")
				env.Fund(issuer)
			}
			env.Close()

			nextID := func() string {
				return nft.GetNextNFTokenID(env, issuer, 0, nftoken.NFTokenFlagTransferable, 0)
			}
			id := nextID()
			jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenMint(issuer, 0).Transferable().Build()))
			env.Close()
			amount := tx.NewXRPAmount(1)
			var transaction tx.Transaction
			switch tc.kind {
			case buyOffer:
				transaction = nft.NFTokenCreateBuyOffer(alice, id, amount, issuer).Build()
			case mintOffer:
				id = nextID()
				transaction = nft.NFTokenMint(alice, 0).Transferable().Amount(amount).Build()
			case sellOffer:
				transaction = nft.NFTokenCreateSellOffer(alice, id, amount).Build()
			}

			rawID, err := hex.DecodeString(id)
			require.NoError(t, err)
			require.Len(t, rawID, 32)
			tokenID := [32]byte(rawID)
			ownerDir := keylet.OwnerDir(alice.ID)
			tokenDir := keylet.NFTSells(tokenID)
			tokenDirFlags := entry.LsfNFTokenSellOffers
			if tc.kind == buyOffer {
				tokenDir = keylet.NFTBuys(tokenID)
				tokenDirFlags = entry.LsfNFTokenBuyOffers
			}
			dirKey := ownerDir
			dir := state.DirectoryNode{Owner: alice.ID}
			if tc.tokenDirectory {
				dirKey = tokenDir
				dir = state.DirectoryNode{NFTokenID: tokenID, Flags: tokenDirFlags}
			}
			fillNFTOfferDirectory(t, env, dirKey, dir, lastPage)
			lastKey := keylet.DirPage(dirKey.Key, lastPage)
			nextKey := keylet.DirPage(dirKey.Key, lastPage+1)
			offerKey := keylet.NFTokenOffer(alice.ID, env.Seq(alice))
			switch tc.fault {
			case wrongRootType:
				accountData, err := env.LedgerEntry(keylet.Account(alice.ID))
				require.NoError(t, err)
				require.NoError(t, env.Ledger().Update(dirKey, accountData))
			case malformedRoot, malformedLastPage:
				key := dirKey
				if tc.fault == malformedLastPage {
					key = lastKey
				}
				data, err := env.LedgerEntry(key)
				require.NoError(t, err)
				require.NotEmpty(t, data)
				truncated := data[:len(data)-1]
				_, err = state.ParseDirectoryNode(truncated)
				require.Error(t, err)
				require.NoError(t, env.Ledger().Update(key, truncated))
			case missingLastPage:
				require.NoError(t, env.Ledger().Erase(lastKey))
			case occupiedNextPage, occupiedOffer:
				key := nextKey
				if tc.fault == occupiedOffer {
					key = offerKey
				}
				data, err := env.LedgerEntry(lastKey)
				require.NoError(t, err)
				require.NoError(t, env.Ledger().Insert(key, data))
			}

			count := env.OwnerCount(alice)
			if tc.lowReserve {
				setXRPBalance(t, env, alice, env.ReserveBase()+uint64(count+1)*env.ReserveIncrement()-1)
			}
			wantBalance, wantSequence := env.Balance(alice), env.Seq(alice)
			minted := env.MintedCount(alice)
			entries := []keylet.Keylet{ownerDir, tokenDir, lastKey, nextKey, offerKey, keylet.NFTokenPageMax(alice.ID)}
			before := make(map[keylet.Keylet][]byte, len(entries))
			for _, key := range entries {
				key.Type = entry.TypeAny
				data, err := env.LedgerEntry(key)
				require.NoError(t, err)
				before[key] = bytes.Clone(data)
			}

			result := env.Submit(transaction)
			require.Equal(t, tc.want.String(), result.Code)
			require.Equal(t, tc.want.IsTec(), result.Applied)
			if tc.want.IsTec() {
				require.NotNil(t, result.Metadata)
				require.Equal(t, tc.want, result.Metadata.TransactionResult)
				require.Len(t, result.Metadata.AffectedNodes, 1)
				require.Equal(t, "AccountRoot", result.Metadata.AffectedNodes[0].LedgerEntryType)
				require.Equal(t, env.BaseFee(), result.Fee)
				wantBalance -= env.BaseFee()
				wantSequence++
			} else {
				require.Zero(t, result.Fee)
				require.Nil(t, result.Metadata)
			}
			env.Close()

			jtx.RequireBalance(t, env, alice, wantBalance)
			jtx.RequireSequence(t, env, alice, wantSequence)
			jtx.RequireOwnerCount(t, env, alice, count)
			jtx.RequireMintedCount(t, env, alice, minted)
			for key, want := range before {
				data, err := env.LedgerEntry(key)
				require.NoError(t, err)
				require.Equalf(t, want, data, "ledger entry %x", key.Key)
			}
		})
	}
}

func TestCachedWrongTypeOfferDirectory(t *testing.T) {
	env := jtx.NewTestEnv(t)
	owner := jtx.NewAccount("owner")
	env.Fund(owner)
	data, err := env.LedgerEntry(keylet.Account(owner.ID))
	require.NoError(t, err)

	dir := keylet.OwnerDir(owner.ID)
	require.NoError(t, env.Ledger().Insert(dir, data))
	view := applystate.NewApplyStateTable(env.Ledger(), [32]byte{}, 1, env.Rules())
	cached, err := view.Read(keylet.Keylet{Key: dir.Key, Type: entry.TypeAny})
	require.NoError(t, err)
	require.Equal(t, data, cached)
	offer := keylet.NFTokenOffer(owner.ID, env.Seq(owner))
	_, err = state.DirInsert(view, dir, offer.Key, false, nil)
	require.ErrorIs(t, err, state.ErrMalformedDirectory)
}
