package nft_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	ledgercore "github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/nft"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/engine"
	"github.com/LeJamon/go-xrpl/internal/tx/nftoken"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

type issuerReadFaultView struct {
	tx.LedgerView
	atomic    ledgercore.AtomicWriter
	issuerKey keylet.Keylet
	err       error
	malformed bool
}

func (v *issuerReadFaultView) ApplyAtomically(apply func(ledgercore.Writer) error) error {
	return v.atomic.ApplyAtomically(apply)
}

func (v *issuerReadFaultView) Read(k keylet.Keylet) ([]byte, error) {
	if k.Key == v.issuerKey.Key {
		if v.err != nil {
			return nil, v.err
		}
		if v.malformed {
			return []byte{1, 2, 3}, nil
		}
	}
	return v.LedgerView.Read(k)
}

type acceptXRPFixture struct {
	env         *jtx.TestEnv
	issuer      *jtx.Account
	seller      *jtx.Account
	buyer       *jtx.Account
	broker      *jtx.Account
	source      *jtx.Account
	txn         tx.Transaction
	tokenID     [32]byte
	offerKeys   []keylet.Keylet
	price       uint64
	brokerFee   uint64
	transferFee uint16
}

func newAcceptXRPFixture(t *testing.T, mode string, issuerIsBroker bool) *acceptXRPFixture {
	t.Helper()
	env := jtx.NewTestEnv(t)
	issuer := jtx.NewAccount("issuer")
	seller := jtx.NewAccount("seller")
	buyer := jtx.NewAccount("buyer")
	broker := jtx.NewAccount("broker")
	env.Fund(issuer, seller, buyer, broker)
	env.Close()

	const transferFee = uint16(10_000)
	tokenIDHex := nft.GetNextNFTokenID(env, issuer, 0, nftoken.NFTokenFlagTransferable, transferFee)
	jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenMint(issuer, 0).
		Transferable().TransferFee(transferFee).Build()))
	env.Close()

	primaryOffer := nft.GetOfferIndex(env, issuer)
	jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenCreateSellOffer(
		issuer, tokenIDHex, tx.NewXRPAmount(0)).Build()))
	env.Close()
	jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenAcceptSellOffer(seller, primaryOffer).Build()))
	env.Close()

	tokenIDBytes, err := hex.DecodeString(tokenIDHex)
	require.NoError(t, err)
	var tokenID [32]byte
	copy(tokenID[:], tokenIDBytes)

	fixture := &acceptXRPFixture{
		env:         env,
		issuer:      issuer,
		seller:      seller,
		buyer:       buyer,
		broker:      broker,
		tokenID:     tokenID,
		price:       1_000_000,
		transferFee: transferFee,
	}

	addOffer := func(owner *jtx.Account) (string, keylet.Keylet) {
		return nft.GetOfferIndex(env, owner), keylet.NFTokenOffer(owner.ID, env.Seq(owner))
	}

	switch mode {
	case "direct sell":
		offerID, offerKey := addOffer(seller)
		jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenCreateSellOffer(
			seller, tokenIDHex, tx.NewXRPAmount(int64(fixture.price))).Build()))
		env.Close()
		fixture.source = buyer
		fixture.offerKeys = []keylet.Keylet{offerKey}
		fixture.txn = nft.NFTokenAcceptSellOffer(buyer, offerID).
			Sequence(env.Seq(buyer)).Build()
	case "direct buy":
		offerID, offerKey := addOffer(buyer)
		jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenCreateBuyOffer(
			buyer, tokenIDHex, tx.NewXRPAmount(int64(fixture.price)), seller).Build()))
		env.Close()
		fixture.source = seller
		fixture.offerKeys = []keylet.Keylet{offerKey}
		fixture.txn = nft.NFTokenAcceptBuyOffer(seller, offerID).
			Sequence(env.Seq(seller)).Build()
	case "brokered":
		sellOfferID, sellOfferKey := addOffer(seller)
		jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenCreateSellOffer(
			seller, tokenIDHex, tx.NewXRPAmount(800_000)).Build()))
		env.Close()
		buyOfferID, buyOfferKey := addOffer(buyer)
		jtx.RequireTxSuccess(t, env.Submit(nft.NFTokenCreateBuyOffer(
			buyer, tokenIDHex, tx.NewXRPAmount(int64(fixture.price)), seller).Build()))
		env.Close()
		fixture.brokerFee = 100_000
		if issuerIsBroker {
			fixture.broker = issuer
		}
		fixture.source = fixture.broker
		fixture.offerKeys = []keylet.Keylet{sellOfferKey, buyOfferKey}
		fixture.txn = nft.NFTokenBrokeredSale(
			fixture.broker, sellOfferID, buyOfferID).
			BrokerFee(tx.NewXRPAmount(int64(fixture.brokerFee))).
			Sequence(env.Seq(fixture.broker)).Build()
	default:
		t.Fatalf("unknown mode %q", mode)
	}

	require.True(t, ownsNFToken(t, env.Ledger(), seller.ID, tokenID))
	require.False(t, ownsNFToken(t, env.Ledger(), buyer.ID, tokenID))
	return fixture
}

func ownsNFToken(t *testing.T, view tx.LedgerView, owner [20]byte, tokenID [32]byte) bool {
	t.Helper()
	after := keylet.NFTokenPageMin(owner).Key
	maxKey := keylet.NFTokenPageMax(owner).Key
	for {
		key, data, found, err := view.Succ(after)
		require.NoError(t, err)
		if !found || bytes.Compare(key[:], maxKey[:]) > 0 {
			return false
		}
		page, err := state.ParseNFTokenPage(data)
		require.NoError(t, err)
		for _, token := range page.NFTokens {
			if token.NFTokenID == tokenID {
				return true
			}
		}
		after = key
	}
}

func snapshotLedger(t *testing.T, view tx.LedgerView) map[[32]byte][]byte {
	t.Helper()
	snapshot := make(map[[32]byte][]byte)
	require.NoError(t, view.ForEach(func(key [32]byte, data []byte) bool {
		snapshot[key] = bytes.Clone(data)
		return true
	}))
	return snapshot
}

func setXRPBalance(t *testing.T, env *jtx.TestEnv, account *jtx.Account, balance uint64) {
	t.Helper()
	accountKey := keylet.Account(account.ID)
	data, err := env.Ledger().Read(accountKey)
	require.NoError(t, err)
	accountRoot, err := state.ParseAccountRoot(data)
	require.NoError(t, err)
	accountRoot.Balance = balance
	data, err = state.SerializeAccountRoot(accountRoot)
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Update(accountKey, data))
}

func TestNFTokenAcceptOfferXRP_IssuerCreditFailuresRollBack(t *testing.T) {
	for _, mode := range []string{"brokered", "direct sell", "direct buy"} {
		for _, fault := range []string{"read", "malformed"} {
			t.Run(mode+"/"+fault, func(t *testing.T) {
				fixture := newAcceptXRPFixture(t, mode, false)
				before := snapshotLedger(t, fixture.env.Ledger())
				balances := map[*jtx.Account]uint64{
					fixture.issuer: fixture.env.Balance(fixture.issuer),
					fixture.seller: fixture.env.Balance(fixture.seller),
					fixture.buyer:  fixture.env.Balance(fixture.buyer),
					fixture.source: fixture.env.Balance(fixture.source),
				}
				view := &issuerReadFaultView{
					LedgerView: fixture.env.Ledger(),
					atomic:     fixture.env.Ledger(),
					issuerKey:  keylet.Account(fixture.issuer.ID),
				}
				if fault == "read" {
					view.err = errors.New("storage read failure")
				} else {
					view.malformed = true
				}

				result := engine.NewEngine(view, tx.EngineConfig{
					BaseFee:                   fixture.env.BaseFee(),
					ReserveBase:               fixture.env.ReserveBase(),
					ReserveIncrement:          fixture.env.ReserveIncrement(),
					LedgerSequence:            fixture.env.LedgerSeq(),
					Rules:                     fixture.env.Ledger().Rules(),
					SkipSignatureVerification: true,
				}).Apply(fixture.txn)

				require.Equal(t, ter.TefINTERNAL, result.Result)
				require.False(t, result.Applied)
				require.Zero(t, result.Fee)
				require.Nil(t, result.Metadata)
				require.Equal(t, before, snapshotLedger(t, fixture.env.Ledger()))
				for account, balance := range balances {
					require.Equal(t, balance, fixture.env.Balance(account))
				}
				for _, offerKey := range fixture.offerKeys {
					require.True(t, fixture.env.LedgerEntryExists(offerKey))
				}
				require.True(t, ownsNFToken(t, fixture.env.Ledger(), fixture.seller.ID, fixture.tokenID))
				require.False(t, ownsNFToken(t, fixture.env.Ledger(), fixture.buyer.ID, fixture.tokenID))
			})
		}
	}
}

func TestNFTokenAcceptOfferXRP_HealthySettlement(t *testing.T) {
	for _, tc := range []struct {
		name           string
		mode           string
		issuerIsBroker bool
	}{
		{name: "brokered", mode: "brokered"},
		{name: "brokered issuer source", mode: "brokered", issuerIsBroker: true},
		{name: "direct sell", mode: "direct sell"},
		{name: "direct buy", mode: "direct buy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newAcceptXRPFixture(t, tc.mode, tc.issuerIsBroker)
			balances := map[*jtx.Account]uint64{
				fixture.issuer: fixture.env.Balance(fixture.issuer),
				fixture.seller: fixture.env.Balance(fixture.seller),
				fixture.buyer:  fixture.env.Balance(fixture.buyer),
				fixture.broker: fixture.env.Balance(fixture.broker),
			}
			basis := fixture.price - fixture.brokerFee
			issuerCut := basis * uint64(fixture.transferFee) / 100_000
			expected := make(map[*jtx.Account]uint64, len(balances))
			for account, balance := range balances {
				expected[account] = balance
			}
			expected[fixture.buyer] -= fixture.price
			expected[fixture.issuer] += issuerCut
			expected[fixture.seller] += basis - issuerCut
			expected[fixture.broker] += fixture.brokerFee
			expected[fixture.source] -= fixture.env.BaseFee()

			result := fixture.env.Submit(fixture.txn)
			jtx.RequireTxSuccess(t, result)
			require.NotNil(t, result.Metadata)
			for account, balance := range expected {
				require.Equal(t, balance, fixture.env.Balance(account))
			}
			for _, offerKey := range fixture.offerKeys {
				require.False(t, fixture.env.LedgerEntryExists(offerKey))
			}
			require.False(t, ownsNFToken(t, fixture.env.Ledger(), fixture.seller.ID, fixture.tokenID))
			require.True(t, ownsNFToken(t, fixture.env.Ledger(), fixture.buyer.ID, fixture.tokenID))
		})
	}
}

func TestNFTokenAcceptOfferXRP_PostFeeDebitFailureRollsBack(t *testing.T) {
	for _, tc := range []struct {
		name    string
		open    bool
		result  ter.Result
		applied bool
	}{
		{name: "closed ledger", result: ter.TecFAILED_PROCESSING, applied: true},
		{name: "open ledger", open: true, result: ter.TelFAILED_PROCESSING},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newAcceptXRPFixture(t, "direct sell", false)
			fee := fixture.env.ReserveBase() + 1
			balance := fixture.env.ReserveBase() + fixture.price
			setXRPBalance(t, fixture.env, fixture.buyer, balance)
			fixture.txn.GetCommon().Fee = strconv.FormatUint(fee, 10)
			fixture.env.SetOpenLedger(tc.open)
			fixture.env.SetViewOpen(tc.open)

			offerBefore, err := fixture.env.Ledger().Read(fixture.offerKeys[0])
			require.NoError(t, err)
			sellerBalance := fixture.env.Balance(fixture.seller)
			issuerBalance := fixture.env.Balance(fixture.issuer)
			sequence := fixture.env.Seq(fixture.buyer)

			result := fixture.env.Submit(fixture.txn)

			require.Equal(t, tc.result, result.Result)
			require.Equal(t, tc.applied, result.Applied)
			if tc.applied {
				require.Equal(t, fee, result.Fee)
				require.Equal(t, balance-fee, fixture.env.Balance(fixture.buyer))
				require.Equal(t, sequence+1, fixture.env.Seq(fixture.buyer))
			} else {
				require.Zero(t, result.Fee)
				require.Equal(t, balance, fixture.env.Balance(fixture.buyer))
				require.Equal(t, sequence, fixture.env.Seq(fixture.buyer))
			}
			require.Equal(t, sellerBalance, fixture.env.Balance(fixture.seller))
			require.Equal(t, issuerBalance, fixture.env.Balance(fixture.issuer))
			offerAfter, err := fixture.env.Ledger().Read(fixture.offerKeys[0])
			require.NoError(t, err)
			require.Equal(t, offerBefore, offerAfter)
			require.True(t, ownsNFToken(t, fixture.env.Ledger(), fixture.seller.ID, fixture.tokenID))
			require.False(t, ownsNFToken(t, fixture.env.Ledger(), fixture.buyer.ID, fixture.tokenID))
		})
	}
}

func TestNFTokenAcceptOfferXRP_DirectBuyOrdinaryInsufficiency(t *testing.T) {
	fixture := newAcceptXRPFixture(t, "direct buy", false)
	buyerReserve := fixture.env.ReserveBase() +
		uint64(fixture.env.OwnerCount(fixture.buyer))*fixture.env.ReserveIncrement()
	buyerBalance := buyerReserve + fixture.price - 1
	setXRPBalance(t, fixture.env, fixture.buyer, buyerBalance)

	offerBefore, err := fixture.env.Ledger().Read(fixture.offerKeys[0])
	require.NoError(t, err)
	sellerBalance := fixture.env.Balance(fixture.seller)
	issuerBalance := fixture.env.Balance(fixture.issuer)
	sequence := fixture.env.Seq(fixture.seller)

	result := fixture.env.Submit(fixture.txn)

	require.Equal(t, ter.TecINSUFFICIENT_FUNDS, result.Result)
	require.True(t, result.Applied)
	require.Equal(t, fixture.env.BaseFee(), result.Fee)
	require.Equal(t, buyerBalance, fixture.env.Balance(fixture.buyer))
	require.Equal(t, sellerBalance-fixture.env.BaseFee(), fixture.env.Balance(fixture.seller))
	require.Equal(t, issuerBalance, fixture.env.Balance(fixture.issuer))
	require.Equal(t, sequence+1, fixture.env.Seq(fixture.seller))
	offerAfter, err := fixture.env.Ledger().Read(fixture.offerKeys[0])
	require.NoError(t, err)
	require.Equal(t, offerBefore, offerAfter)
	require.True(t, ownsNFToken(t, fixture.env.Ledger(), fixture.seller.ID, fixture.tokenID))
	require.False(t, ownsNFToken(t, fixture.env.Ledger(), fixture.buyer.ID, fixture.tokenID))
}
