package offer

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/stretchr/testify/require"
)

type offerMPTLedgerView struct {
	data  map[[32]byte][]byte
	rules *amendment.Rules
}

func newOfferMPTLedgerView() *offerMPTLedgerView {
	return &offerMPTLedgerView{
		data:  make(map[[32]byte][]byte),
		rules: amendment.AllSupportedRules(),
	}
}

func (v *offerMPTLedgerView) Read(k keylet.Keylet) ([]byte, error) {
	return v.data[k.Key], nil
}

func (v *offerMPTLedgerView) Exists(k keylet.Keylet) (bool, error) {
	_, ok := v.data[k.Key]
	return ok, nil
}

func (v *offerMPTLedgerView) Insert(k keylet.Keylet, data []byte) error {
	v.data[k.Key] = data
	return nil
}

func (v *offerMPTLedgerView) Update(k keylet.Keylet, data []byte) error {
	v.data[k.Key] = data
	return nil
}

func (v *offerMPTLedgerView) Erase(k keylet.Keylet) error {
	delete(v.data, k.Key)
	return nil
}

func (*offerMPTLedgerView) AdjustDropsDestroyed(drops.XRPAmount) error { return nil }
func (*offerMPTLedgerView) TxExists([32]byte) (bool, error)            { return false, nil }
func (v *offerMPTLedgerView) Rules() *amendment.Rules                  { return v.rules }
func (*offerMPTLedgerView) LedgerSeq() uint32                          { return 1 }

func (v *offerMPTLedgerView) ForEach(fn func([32]byte, []byte) bool) error {
	for k, data := range v.data {
		if !fn(k, data) {
			break
		}
	}
	return nil
}

func (*offerMPTLedgerView) Succ([32]byte) ([32]byte, []byte, bool, error) {
	return [32]byte{}, nil, false, nil
}

func putOfferMPTAccount(t *testing.T, view *offerMPTLedgerView, account [20]byte) {
	t.Helper()
	raw, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:  state.EncodeAccountIDSafe(account),
		Balance:  100_000_000,
		Sequence: 2,
	})
	require.NoError(t, err)
	view.data[keylet.Account(account).Key] = raw
}

func putOfferMPTIssuance(
	t *testing.T,
	view *offerMPTLedgerView,
	id [24]byte,
	flags uint32,
	outstanding, maximum uint64,
) {
	t.Helper()
	raw, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{
		Issuer:            mptutil.Issuer(id),
		Sequence:          1,
		Flags:             flags,
		OutstandingAmount: outstanding,
		MaximumAmount:     &maximum,
	})
	require.NoError(t, err)
	view.data[keylet.MPTIssuance(id).Key] = raw
}

func putOfferMPTHolding(t *testing.T, view *offerMPTLedgerView, id [24]byte, holder [20]byte, amount uint64) {
	t.Helper()
	raw, err := state.SerializeMPToken(&state.MPTokenData{
		Account:           holder,
		MPTokenIssuanceID: id,
		MPTAmount:         amount,
	})
	require.NoError(t, err)
	view.data[keylet.MPTokenByID(id, holder).Key] = raw
}

func offerMPTAmount(id [24]byte, value int64) tx.Amount {
	return state.NewMPTAmountWithIssuanceID(
		value,
		state.EncodeAccountIDSafe(mptutil.Issuer(id)),
		mptutil.EncodeID(id),
	)
}

func TestOfferMPTBookDirectoryAssets(t *testing.T) {
	var issuer, counterparty [20]byte
	copy(issuer[:], []byte("issuer12345678901234"))
	copy(counterparty[:], []byte("counterparty123456789"))
	id := keylet.MakeMPTID(7, issuer)
	domain := [32]byte{1, 2, 3}
	mpt := offerMPTAmount(id, 100)
	iou := tx.NewIssuedAmount(5_000_000_000_000_000, -15, "USD", state.EncodeAccountIDSafe(counterparty))

	base, err := offerBookBase(mpt, iou, &domain)
	require.NoError(t, err)
	require.Equal(t, keylet.BookBase(
		keylet.MPTSide(id),
		keylet.IssueSide(keylet.CurrencyBytes("USD"), counterparty),
		&domain,
	), base)

	dir := &state.DirectoryNode{RootIndex: base.Key, ExchangeRate: 1}
	require.NoError(t, setBookDirectoryAssets(dir, mpt, iou))
	require.NotNil(t, dir.TakerPaysMPT)
	require.Equal(t, id, *dir.TakerPaysMPT)
	require.Nil(t, dir.TakerGetsMPT)
	require.Equal(t, [20]byte{}, dir.TakerPaysCurrency)
	require.Equal(t, [20]byte{}, dir.TakerPaysIssuer)
	require.Equal(t, keylet.CurrencyBytes("USD"), dir.TakerGetsCurrency)
	require.Equal(t, counterparty, dir.TakerGetsIssuer)

	raw, err := state.SerializeDirectoryNode(dir, true)
	require.NoError(t, err)
	parsed, err := state.ParseDirectoryNode(raw)
	require.NoError(t, err)
	require.NotNil(t, parsed.TakerPaysMPT)
	require.Equal(t, id, *parsed.TakerPaysMPT)
	require.Nil(t, parsed.TakerGetsMPT)
	require.Equal(t, keylet.CurrencyBytes("USD"), parsed.TakerGetsCurrency)
	require.Equal(t, counterparty, parsed.TakerGetsIssuer)
}

func TestOfferMPTValidationUsesIssuanceID(t *testing.T) {
	var issuer [20]byte
	copy(issuer[:], []byte("issuer12345678901234"))
	firstID := keylet.MakeMPTID(1, issuer)
	secondID := keylet.MakeMPTID(2, issuer)

	offer := &OfferCreate{
		BaseTx:    *tx.NewBaseTx(tx.TypeOfferCreate, "rAlice"),
		TakerGets: offerMPTAmount(firstID, 100),
		TakerPays: offerMPTAmount(secondID, 100),
	}
	require.NoError(t, offer.Validate())

	offer.TakerPays = offerMPTAmount(firstID, 50)
	require.EqualError(t, offer.Validate(), "temREDUNDANT: cannot create offer with same currency and issuer on both sides")
}

func TestOfferMPTValidationRejectsZeroIssuer(t *testing.T) {
	badID := keylet.MakeMPTID(1, [20]byte{})
	mpt := offerMPTAmount(badID, 100)
	xrp := tx.NewXRPAmount(100)

	for _, tt := range []struct {
		name      string
		takerPays tx.Amount
		takerGets tx.Amount
	}{
		{name: "taker pays", takerPays: mpt, takerGets: xrp},
		{name: "taker gets", takerPays: xrp, takerGets: mpt},
	} {
		t.Run(tt.name, func(t *testing.T) {
			offer := &OfferCreate{
				BaseTx:    *tx.NewBaseTx(tx.TypeOfferCreate, "rAlice"),
				TakerPays: tt.takerPays,
				TakerGets: tt.takerGets,
			}
			require.EqualError(t, offer.Validate(), "temBAD_CURRENCY: MPT issuance ID has a zero issuer")
		})
	}
}

func TestOfferMPTPostCrossRoundingPreservesIntegralAsset(t *testing.T) {
	var issuer [20]byte
	copy(issuer[:], []byte("issuer12345678901234"))
	id := keylet.MakeMPTID(1, issuer)
	asset := offerMPTAmount(id, 5)
	half := tx.NewIssuedAmount(5_000_000_000_000_000, -16, "", "")
	two := tx.NewIssuedAmount(2_000_000_000_000_000, -15, "", "")
	one := tx.NewIssuedAmount(1_000_000_000_000_000, -15, "", "")

	identity := offerMulRoundLike(offerMPTAmount(id, 123_456_789), one, asset, true)
	value, ok := identity.MPTRaw()
	require.True(t, ok)
	require.Equal(t, int64(123_456_789), value)
	require.Equal(t, mptutil.EncodeID(id), identity.MPTIssuanceID())

	roundedUp := offerMulRoundLike(asset, half, asset, true)
	value, ok = roundedUp.MPTRaw()
	require.True(t, ok)
	require.Equal(t, int64(3), value)

	roundedDown := offerDivRoundStrictLike(asset, two, asset, false)
	value, ok = roundedDown.MPTRaw()
	require.True(t, ok)
	require.Equal(t, int64(2), value)
}

func TestOfferMPTIssuerMayPlaceUnfundedOffer(t *testing.T) {
	var issuer, holder [20]byte
	copy(issuer[:], []byte("issuer12345678901234"))
	copy(holder[:], []byte("holder12345678901234"))
	id := keylet.MakeMPTID(1, issuer)
	mpt := offerMPTAmount(id, 100)

	require.False(t, offerDisallowUnfunded(mpt, issuer))
	require.True(t, offerDisallowUnfunded(mpt, holder))
	require.True(t, offerDisallowUnfunded(tx.NewXRPAmount(100), issuer))

	remainingGets, remainingPays := computePostCrossAmounts(
		tx.NewXRPAmount(500),
		mpt,
		offerMPTAmount(id, 0),
		tx.NewXRPAmount(0),
		offerMPTAmount(id, 0),
		true,
		false,
		state.NewNumberContext(state.MantissaScaleLarge, true),
	)
	value, ok := remainingGets.MPTRaw()
	require.True(t, ok)
	require.Equal(t, int64(100), value)
	require.Equal(t, int64(500), remainingPays.Drops())
}

func TestOfferMPTPreclaimPermissionsAndFunding(t *testing.T) {
	var issuer, holder [20]byte
	copy(issuer[:], []byte("issuer12345678901234"))
	copy(holder[:], []byte("holder12345678901234"))
	id := keylet.MakeMPTID(1, issuer)
	config := tx.EngineConfig{ReserveBase: 10_000_000, ReserveIncrement: 2_000_000}

	newOffer := func(account [20]byte, takerPays, takerGets tx.Amount) *OfferCreate {
		return &OfferCreate{
			BaseTx:    *tx.NewBaseTx(tx.TypeOfferCreate, state.EncodeAccountIDSafe(account)),
			TakerPays: takerPays,
			TakerGets: takerGets,
		}
	}

	t.Run("funded holder", func(t *testing.T) {
		view := newOfferMPTLedgerView()
		putOfferMPTAccount(t, view, issuer)
		putOfferMPTAccount(t, view, holder)
		putOfferMPTIssuance(t, view, id, entry.LsfMPTCanTrade|entry.LsfMPTCanTransfer, 100, 1_000)
		putOfferMPTHolding(t, view, id, holder, 100)

		offer := newOffer(holder, tx.NewXRPAmount(1_000), offerMPTAmount(id, 10))
		require.Equal(t, ter.TesSUCCESS, offer.Preclaim(view, config))
	})

	t.Run("corrupt holding is internal error", func(t *testing.T) {
		view := newOfferMPTLedgerView()
		putOfferMPTAccount(t, view, issuer)
		putOfferMPTAccount(t, view, holder)
		putOfferMPTIssuance(t, view, id, entry.LsfMPTCanTrade|entry.LsfMPTCanTransfer, 100, 1_000)
		view.data[keylet.MPTokenByID(id, holder).Key] = []byte{1}

		offer := newOffer(holder, tx.NewXRPAmount(1_000), offerMPTAmount(id, 10))
		require.Equal(t, ter.TefINTERNAL, offer.Preclaim(view, config))
	})

	t.Run("trading disabled", func(t *testing.T) {
		view := newOfferMPTLedgerView()
		putOfferMPTAccount(t, view, issuer)
		putOfferMPTAccount(t, view, holder)
		putOfferMPTIssuance(t, view, id, entry.LsfMPTCanTransfer, 100, 1_000)
		putOfferMPTHolding(t, view, id, holder, 100)

		offer := newOffer(holder, tx.NewXRPAmount(1_000), offerMPTAmount(id, 10))
		require.Equal(t, ter.TecNO_PERMISSION, offer.Preclaim(view, config))
	})

	t.Run("globally locked", func(t *testing.T) {
		view := newOfferMPTLedgerView()
		putOfferMPTAccount(t, view, issuer)
		putOfferMPTAccount(t, view, holder)
		putOfferMPTIssuance(t, view, id, entry.LsfMPTCanTrade|entry.LsfMPTCanTransfer|entry.LsfMPTLocked, 100, 1_000)
		putOfferMPTHolding(t, view, id, holder, 100)

		offer := newOffer(holder, tx.NewXRPAmount(1_000), offerMPTAmount(id, 10))
		require.Equal(t, ter.TecLOCKED, offer.Preclaim(view, config))
	})

	t.Run("issuer may offer beyond available issuance", func(t *testing.T) {
		view := newOfferMPTLedgerView()
		putOfferMPTAccount(t, view, issuer)
		putOfferMPTIssuance(t, view, id, entry.LsfMPTCanTrade|entry.LsfMPTCanTransfer, 100, 100)

		offer := newOffer(issuer, tx.NewXRPAmount(1_000), offerMPTAmount(id, 10))
		require.Equal(t, ter.TesSUCCESS, offer.Preclaim(view, config))
	})

	t.Run("receiver requires authorization", func(t *testing.T) {
		view := newOfferMPTLedgerView()
		putOfferMPTAccount(t, view, issuer)
		putOfferMPTAccount(t, view, holder)
		putOfferMPTIssuance(t, view, id,
			entry.LsfMPTCanTrade|entry.LsfMPTCanTransfer|entry.LsfMPTRequireAuth,
			0, 1_000,
		)

		offer := newOffer(holder, offerMPTAmount(id, 10), tx.NewXRPAmount(1_000))
		require.Equal(t, ter.TecNO_AUTH, offer.Preclaim(view, config))
	})
}

var _ tx.LedgerView = (*offerMPTLedgerView)(nil)
