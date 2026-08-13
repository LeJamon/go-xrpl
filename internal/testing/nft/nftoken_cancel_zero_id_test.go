package nft_test

import (
	"encoding/hex"
	"strings"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/nft"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/nftoken"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// TestNFTokenCancelOffer_ZeroOfferID verifies that fixCleanup3_2_0 rejects a zero
// offer ID with temMALFORMED at preflight. Before the amendment a zero ID passed
// preflight and was silently treated as an already-consumed offer at apply time.
// Reference: rippled NFTokenCancelOffer.cpp preflight (commit fded06652a).
func TestNFTokenCancelOffer_ZeroOfferID(t *testing.T) {
	zeroID := strings.Repeat("0", 64)

	newEnv := func(t *testing.T, amendmentEnabled bool) (*jtx.TestEnv, *jtx.Account) {
		env := jtx.NewTestEnv(t)
		if !amendmentEnabled {
			env.DisableFeature("fixCleanup3_2_0")
		}
		env.Close()
		alice := jtx.NewAccount("alice")
		env.Fund(alice)
		env.Close()
		return env, alice
	}

	t.Run("PureZero", func(t *testing.T) {
		t.Run("enabled_temMALFORMED", func(t *testing.T) {
			env, alice := newEnv(t, true)
			balanceBefore := env.Balance(alice)
			sequenceBefore := env.Seq(alice)
			res := env.Submit(nft.NFTokenCancelOffer(alice, zeroID).Build())
			jtx.RequireTxFail(t, res, "temMALFORMED")
			require.False(t, res.Applied)
			require.Zero(t, res.Fee)
			require.Nil(t, res.Metadata)
			require.Equal(t, balanceBefore, env.Balance(alice))
			require.Equal(t, sequenceBefore, env.Seq(alice))
		})
		t.Run("disabled_tesSUCCESS", func(t *testing.T) {
			env, alice := newEnv(t, false)
			balanceBefore := env.Balance(alice)
			sequenceBefore := env.Seq(alice)
			res := env.Submit(nft.NFTokenCancelOffer(alice, zeroID).Build())
			jtx.RequireTxSuccess(t, res)
			require.True(t, res.Applied)
			require.Equal(t, uint64(10), res.Fee)
			require.NotNil(t, res.Metadata)
			require.Equal(t, balanceBefore-10, env.Balance(alice))
			require.Equal(t, sequenceBefore+1, env.Seq(alice))
		})
	})

	t.Run("Precedence", func(t *testing.T) {
		t.Run("invalid_flag", func(t *testing.T) {
			env, alice := newEnv(t, true)
			cancel := nft.NFTokenCancelOffer(alice, zeroID).Build()
			cancel.GetCommon().SetFlags(1)
			jtx.RequireTxFail(t, env.Submit(cancel), "temINVALID_FLAG")
		})
		t.Run("bad_fee", func(t *testing.T) {
			env, alice := newEnv(t, true)
			cancel := nft.NFTokenCancelOffer(alice, zeroID).Build()
			cancel.GetCommon().Fee = "-1"
			jtx.RequireTxFail(t, env.Submit(cancel), "temBAD_FEE")
		})
	})

	// A batch mixing a valid offer with a zero ID: with the amendment the whole
	// transaction is rejected (temMALFORMED); without it the valid offer is
	// cancelled and the zero ID is silently skipped.
	t.Run("MixedWithRealOffer", func(t *testing.T) {
		setup := func(t *testing.T, amendmentEnabled bool) (*jtx.TestEnv, *jtx.Account, string, keylet.Keylet) {
			env, alice := newEnv(t, amendmentEnabled)
			nftID := nft.GetNextNFTokenID(env, alice, 0, nftoken.NFTokenFlagTransferable, 0)
			jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenMint(alice, 0).Transferable().Build()))
			env.Close()
			offerSeq := env.Seq(alice)
			offerKL := keylet.NFTokenOffer(alice.ID, offerSeq)
			jtx.RequireTxSuccess(t, env.Submit(
				nft.NFTokenCreateSellOffer(alice, nftID, tx.NewXRPAmount(0)).Build()))
			env.Close()
			return env, alice, hex.EncodeToString(offerKL.Key[:]), offerKL
		}

		t.Run("enabled_temMALFORMED", func(t *testing.T) {
			env, alice, realOfferID, offerKL := setup(t, true)
			balanceBefore := env.Balance(alice)
			sequenceBefore := env.Seq(alice)
			res := env.Submit(nft.NFTokenCancelOffer(alice, realOfferID, zeroID).Build())
			jtx.RequireTxFail(t, res, "temMALFORMED")
			require.False(t, res.Applied)
			require.Zero(t, res.Fee)
			require.Nil(t, res.Metadata)
			require.Equal(t, balanceBefore, env.Balance(alice))
			require.Equal(t, sequenceBefore, env.Seq(alice))
			require.True(t, env.LedgerEntryExists(offerKL), "offer must survive a rejected cancel")
		})
		t.Run("disabled_cancelsReal", func(t *testing.T) {
			env, alice, realOfferID, offerKL := setup(t, false)
			res := env.Submit(nft.NFTokenCancelOffer(alice, realOfferID, zeroID).Build())
			jtx.RequireTxSuccess(t, res)
			require.False(t, env.LedgerEntryExists(offerKL), "valid offer must be cancelled")
		})
	})
}
