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
	offerKey := keylet.NFTokenOffer(buyer.ID, env.Seq(buyer))
	jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenCreateBuyOffer(buyer, id, tx.NewXRPAmount(1), seller).Build()))
	env.Close()

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
	for _, tc := range []struct {
		name           string
		tokenDirectory bool
		fixLimit       bool
		lowReserve     bool
		mint           bool
		corrupt        bool
		missingPage    bool
		want           ter.Result
	}{
		{name: "full owner directory", want: ter.TecDIR_FULL},
		{name: "full sell directory", tokenDirectory: true, want: ter.TecDIR_FULL},
		{name: "sell directory page overflow", tokenDirectory: true, fixLimit: true, want: ter.TecDIR_FULL},
		{name: "reserve before directory", lowReserve: true, want: ter.TecINSUFFICIENT_RESERVE},
		{name: "mint with full owner directory", mint: true, want: ter.TecDIR_FULL},
		{name: "mint with full sell directory", mint: true, tokenDirectory: true, want: ter.TecDIR_FULL},
		{name: "invalid owner directory", corrupt: true, want: ter.TefINTERNAL},
		{name: "missing sell directory page", tokenDirectory: true, missingPage: true, want: ter.TefEXCEPTION},
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
			alice := jtx.NewAccount("alice")
			env.Fund(alice)
			env.Close()

			nextID := func() string {
				return nft.GetNextNFTokenID(env, alice, 0, nftoken.NFTokenFlagTransferable, 0)
			}
			id := nextID()
			jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenMint(alice, 0).Transferable().Build()))
			env.Close()
			amount := tx.NewXRPAmount(1)
			var transaction tx.Transaction = nft.NFTokenCreateSellOffer(alice, id, amount).Build()
			if tc.mint {
				id = nextID()
				transaction = nft.NFTokenMint(alice, 0).Transferable().Amount(amount).Build()
			}

			rawID, err := hex.DecodeString(id)
			require.NoError(t, err)
			require.Len(t, rawID, 32)
			tokenID := [32]byte(rawID)
			ownerDir := keylet.OwnerDir(alice.ID)
			tokenDir := keylet.NFTSells(tokenID)
			dirKey := ownerDir
			dir := state.DirectoryNode{Owner: alice.ID}
			if tc.tokenDirectory {
				dirKey = tokenDir
				dir = state.DirectoryNode{NFTokenID: tokenID, Flags: entry.LsfNFTokenSellOffers}
			}
			if tc.corrupt {
				accountData, err := env.LedgerEntry(keylet.Account(alice.ID))
				require.NoError(t, err)
				require.NoError(t, env.Ledger().Insert(dirKey, accountData))
			} else {
				fillNFTOfferDirectory(t, env, dirKey, dir, lastPage)
				if tc.missingPage {
					require.NoError(t, env.Ledger().Erase(keylet.DirPage(dirKey.Key, lastPage)))
				}
			}

			count := env.OwnerCount(alice)
			if tc.lowReserve {
				setXRPBalance(t, env, alice, env.ReserveBase()+uint64(count+1)*env.ReserveIncrement()-1)
			}
			wantBalance, wantSequence := env.Balance(alice), env.Seq(alice)
			offerKey := keylet.NFTokenOffer(alice.ID, wantSequence)
			minted := env.MintedCount(alice)
			entries := []keylet.Keylet{ownerDir, tokenDir, keylet.DirPage(dirKey.Key, lastPage), keylet.NFTokenPageMax(alice.ID)}
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

			jtx.RequireLedgerEntryNotExists(t, env, offerKey)
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
