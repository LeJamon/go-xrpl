package payment

import (
	"encoding/binary"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/require"
)

func TestLimitOutMPTIntegralRoundingHonorsAverageQuality(t *testing.T) {
	var id [24]byte
	id[23] = 1

	qf := NewAMMQualityFunction(newMPTAmount(10, id), newMPTAmount(10, id), 0)
	require.NotNil(t, qf)
	limitQuality := QualityFromAmounts(NewMPTEitherAmount(25, id), NewMPTEitherAmount(21, id))
	step := &qualityFunctionStep{qf: qf}

	for _, test := range []struct {
		name    string
		enabled bool
		want    int64
	}{
		{name: "before MPTokensV2", want: 2},
		{name: "with MPTokensV2", enabled: true, want: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			view := newPaymentMockLedgerView()
			rules := amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported)
			if test.enabled {
				rules.Enable(amendment.FeatureMPTokensV2)
			}
			view.rules = rules.Build()

			got := limitOut(NewPaymentSandbox(view), Strand{step}, NewMPTEitherAmount(10, id), limitQuality)
			require.True(t, got.IsMPT)
			require.Equal(t, test.want, got.MPT)
			require.Equal(t, id, got.MPTID)
		})
	}

	continuous := qf.OutFromAvgQ(limitQuality)
	require.NotNil(t, continuous)
	require.True(t, qf.SatisfiesAvgQ(limitQuality, qf.math.fromAmount(newMPTAmount(1, id), state.RoundToNearest)))
	require.False(t, qf.SatisfiesAvgQ(limitQuality, qf.math.fromAmount(newMPTAmount(2, id), state.RoundToNearest)))
}

func TestMPTEndpointLargeRequestsWithLimitedFunds(t *testing.T) {
	const (
		aliceBalance       = int64(1_000)
		bobBalance         = int64(666)
		maxRepresentable   = int64(6_148_914_691_236_517_204)
		maximumTransferFee = uint16(50_000)
	)

	for _, test := range []struct {
		name    string
		sendMax int64
		deliver int64
	}{
		{
			name:    "requested amount exceeds representable bound",
			sendMax: int64(protocol.MaxMPTokenAmount),
			deliver: maxRepresentable + 1,
		},
		{
			name:    "send maximum exceeds representable bound",
			sendMax: maxRepresentable + 1,
			deliver: int64(protocol.MaxMPTokenAmount),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			issuer := [20]byte{1}
			alice := [20]byte{2}
			bob := [20]byte{3}
			id := keylet.MakeMPTID(1, issuer)
			view := newPaymentMockLedgerView()
			view.rules = amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported).Enable(amendment.FeatureMPTokensV2).Build()
			for _, account := range [][20]byte{issuer, alice, bob} {
				view.createAccount(account, 100_000_000, 1)
			}
			putMPTIssuance(t, view, id, uint64(aliceBalance), maximumTransferFee)
			putMPTHolding(t, view, id, alice, uint64(aliceBalance))
			putMPTHolding(t, view, id, bob, 0)

			sandbox := NewPaymentSandbox(view)
			deliver := state.NewMPTAmountWithIssuanceID(
				test.deliver,
				state.EncodeAccountIDSafe(issuer),
				keyletIDHex(id),
			)
			strands, result := ToStrands(sandbox, alice, bob, deliver, nil, nil, true, false)
			require.Equal(t, ter.TesSUCCESS, result)
			require.Len(t, strands, 1)

			sendMax := NewMPTEitherAmount(test.sendMax, id)
			flow := Flow(
				sandbox,
				strands,
				NewMPTEitherAmount(test.deliver, id),
				true,
				nil,
				&sendMax,
				nil,
				false,
			)
			require.Equal(t, ter.TesSUCCESS, flow.Result)
			require.Equal(t, aliceBalance, flow.In.MPT)
			require.Equal(t, bobBalance, flow.Out.MPT)
			require.NoError(t, flow.Sandbox.Apply(sandbox))
			require.NoError(t, sandbox.ApplyToView(view))

			outstanding, balances := readMPTAmounts(t, view, id, alice, bob)
			require.Equal(t, uint64(bobBalance), outstanding)
			require.Equal(t, []uint64{0, uint64(bobBalance)}, balances)
		})
	}
}

func putMPTBookOffer(
	t *testing.T,
	view *paymentMockLedgerView,
	step *BookStep,
	owner [20]byte,
	sequence uint32,
	takerPays, takerGets tx.Amount,
	bookDirectory [32]byte,
) [32]byte {
	t.Helper()
	offerKey := keylet.Offer(owner, sequence).Key
	ownerDirectory := keylet.OwnerDir(owner)
	_, err := state.DirInsert(view, ownerDirectory, offerKey, false, func(dir *state.DirectoryNode) {
		dir.Owner = owner
	})
	require.NoError(t, err)

	_, err = state.DirInsert(view, keylet.Keylet{Key: bookDirectory}, offerKey, true, func(dir *state.DirectoryNode) {
		if step.book.In.IsMPT {
			id := step.book.In.MPTID
			dir.TakerPaysMPT = &id
		} else {
			dir.TakerPaysCurrency = keylet.CurrencyBytes(step.book.In.Currency)
			dir.TakerPaysIssuer = step.book.In.Issuer
		}
		if step.book.Out.IsMPT {
			id := step.book.Out.MPTID
			dir.TakerGetsMPT = &id
		} else {
			dir.TakerGetsCurrency = keylet.CurrencyBytes(step.book.Out.Currency)
			dir.TakerGetsIssuer = step.book.Out.Issuer
		}
	})
	require.NoError(t, err)

	offer := &state.LedgerOffer{
		Account:       state.EncodeAccountIDSafe(owner),
		Sequence:      sequence,
		TakerPays:     takerPays,
		TakerGets:     takerGets,
		BookDirectory: bookDirectory,
	}
	data, err := state.SerializeLedgerOffer(offer)
	require.NoError(t, err)
	view.data[offerKey] = data
	return offerKey
}

func TestMPTBookTransferFeeOverflowRemovesOffer(t *testing.T) {
	issuerA := [20]byte{1}
	issuerB := [20]byte{2}
	taker := [20]byte{3}
	validMaker := [20]byte{4}
	poisonMaker := [20]byte{5}
	idA := keylet.MakeMPTID(1, issuerA)
	idB := keylet.MakeMPTID(1, issuerB)
	issueA := NewMPTIssue(idA)
	issueB := NewMPTIssue(idB)

	view := newPaymentMockLedgerView()
	view.rules = amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported).Enable(amendment.FeatureMPTokensV2).Build()
	for _, account := range [][20]byte{issuerA, issuerB, taker} {
		view.createAccount(account, 100_000_000, 1)
	}
	for _, account := range [][20]byte{validMaker, poisonMaker} {
		view.createAccount(account, 100_000_000, 3)
	}
	putMPTIssuance(t, view, idA, 600, 50_000)
	putMPTIssuance(t, view, idB, 34_000_000_000_000_001, 0)
	putMPTHolding(t, view, idA, taker, 600)
	putMPTHolding(t, view, idA, validMaker, 0)
	putMPTHolding(t, view, idA, poisonMaker, 0)
	putMPTHolding(t, view, idB, taker, 0)
	putMPTHolding(t, view, idB, validMaker, 1)
	putMPTHolding(t, view, idB, poisonMaker, 34_000_000_000_000_000)

	step := NewBookStep(issueA, issueB, taker, taker, nil, false)
	quality := QualityFromAmounts(
		NewMPTEitherAmount(181, idA),
		NewMPTEitherAmount(1, idB),
	)
	bookDirectory := step.bookBaseKey()
	binary.BigEndian.PutUint64(bookDirectory[24:], quality.Value)
	validKey := putMPTBookOffer(
		t, view, step, validMaker, 1,
		newMPTAmount(181, idA), newMPTAmount(1, idB), bookDirectory,
	)
	poisonKey := putMPTBookOffer(
		t, view, step, poisonMaker, 1,
		newMPTAmount(6_154_000_000_000_000_000, idA),
		newMPTAmount(34_000_000_000_000_000, idB),
		bookDirectory,
	)
	sandbox := NewPaymentSandbox(view)
	takerGets := newMPTAmount(240, idA)
	takerPays := newMPTAmount(2, idB)
	strands, result := ToStrands(sandbox, taker, taker, takerPays, &takerGets, nil, true, true)
	require.Equal(t, ter.TesSUCCESS, result)
	require.Len(t, strands, 1)
	configureStrandsForOfferCrossing(strands, nil, 0, false)
	sendMax := NewMPTEitherAmount(600, idA)
	flow := Flow(sandbox, strands, NewMPTEitherAmount(2, idB), true, nil, &sendMax, nil, true)
	require.Equal(t, ter.TesSUCCESS, flow.Result)
	require.Equal(t, int64(1), flow.Out.MPT)
	require.Equal(t, int64(272), flow.In.MPT)
	require.Contains(t, flow.RemovableOffers, poisonKey)
	require.NotContains(t, flow.RemovableOffers, validKey)
	require.NotNil(t, flow.Sandbox)
	require.NoError(t, flow.Sandbox.Apply(sandbox))
	require.NoError(t, sandbox.ApplyToView(view))

	validData, err := view.Read(keylet.Keylet{Key: validKey})
	require.NoError(t, err)
	require.Nil(t, validData)
	poisonData, err := view.Read(keylet.Keylet{Key: poisonKey})
	require.NoError(t, err)
	require.Nil(t, poisonData)

	outstandingA, balancesA := readMPTAmounts(t, view, idA, taker, validMaker, poisonMaker)
	require.Equal(t, uint64(509), outstandingA)
	require.Equal(t, []uint64{328, 181, 0}, balancesA)
	outstandingB, balancesB := readMPTAmounts(t, view, idB, taker, validMaker, poisonMaker)
	require.Equal(t, uint64(34_000_000_000_000_001), outstandingB)
	require.Equal(t, []uint64{1, 0, 34_000_000_000_000_000}, balancesB)
	for _, owner := range [][20]byte{validMaker, poisonMaker} {
		raw, err := view.Read(keylet.Account(owner))
		require.NoError(t, err)
		account, err := state.ParseAccountRoot(raw)
		require.NoError(t, err)
		require.Equal(t, uint32(2), account.OwnerCount)
	}
}

func TestMPTBookAccumulatorOverflowPreservesEarlierAndLaterLiquidity(t *testing.T) {
	issuerA, issuerB := [20]byte{1}, [20]byte{2}
	taker, recipient := [20]byte{3}, [20]byte{6}
	first, poison, last := [20]byte{4}, [20]byte{5}, [20]byte{7}
	idA, idB := keylet.MakeMPTID(1, issuerA), keylet.MakeMPTID(1, issuerB)
	view := newPaymentMockLedgerView()
	view.rules = amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported).Enable(amendment.FeatureMPTokensV2).Build()
	for _, account := range [][20]byte{issuerA, issuerB, taker, recipient} {
		view.createAccount(account, 100_000_000, 1)
	}
	for _, account := range [][20]byte{first, poison, last} {
		view.createAccount(account, 100_000_000, 3)
	}
	putMPTIssuance(t, view, idA, protocol.MaxMPTokenAmount, 50_000)
	putMPTIssuance(t, view, idB, 5_000_000_000_000_000_001, 0)
	putMPTHolding(t, view, idA, taker, protocol.MaxMPTokenAmount)
	putMPTHolding(t, view, idB, recipient, 0)
	for _, account := range [][20]byte{first, poison, last} {
		putMPTHolding(t, view, idA, account, 0)
	}
	putMPTHolding(t, view, idB, first, 2_500_000_000_000_000_000)
	putMPTHolding(t, view, idB, poison, 2_500_000_000_000_000_000)
	putMPTHolding(t, view, idB, last, 1)
	step := NewBookStep(NewMPTIssue(idA), NewMPTIssue(idB), taker, recipient, nil, false)
	quality := QualityFromAmounts(NewMPTEitherAmount(2, idA), NewMPTEitherAmount(1, idB))
	directory := step.bookBaseKey()
	binary.BigEndian.PutUint64(directory[24:], quality.Value)
	firstKey := putMPTBookOffer(t, view, step, first, 1, newMPTAmount(5_000_000_000_000_000_000, idA), newMPTAmount(2_500_000_000_000_000_000, idB), directory)
	poisonKey := putMPTBookOffer(t, view, step, poison, 1, newMPTAmount(5_000_000_000_000_000_000, idA), newMPTAmount(2_500_000_000_000_000_000, idB), directory)
	lastKey := putMPTBookOffer(t, view, step, last, 1, newMPTAmount(2, idA), newMPTAmount(1, idB), directory)
	sandbox := NewPaymentSandbox(view)
	deliver := newMPTAmount(5_000_000_000_000_000_001, idB)
	send := newMPTAmount(int64(protocol.MaxMPTokenAmount), idA)
	strands, result := ToStrands(sandbox, taker, recipient, deliver, &send, nil, true, false)
	require.Equal(t, ter.TesSUCCESS, result)
	sendMax := ToEitherAmount(send)
	flow := Flow(sandbox, strands, ToEitherAmount(deliver), true, nil, &sendMax, nil, false)
	require.Equal(t, ter.TesSUCCESS, flow.Result)
	require.Equal(t, int64(7_500_000_000_000_000_003), flow.In.MPT)
	require.Equal(t, int64(2_500_000_000_000_000_001), flow.Out.MPT)
	require.Contains(t, flow.RemovableOffers, poisonKey)
	require.NotContains(t, flow.RemovableOffers, firstKey)
	require.NotContains(t, flow.RemovableOffers, lastKey)
	require.NoError(t, flow.Sandbox.Apply(sandbox))
	require.NoError(t, sandbox.ApplyToView(view))
	for _, key := range [][32]byte{firstKey, poisonKey, lastKey} {
		data, err := view.Read(keylet.Keylet{Key: key})
		require.NoError(t, err)
		require.Nil(t, data)
	}
	outstandingA, balancesA := readMPTAmounts(t, view, idA, taker, first, poison, last)
	require.Equal(t, uint64(6_723_372_036_854_775_806), outstandingA)
	require.Equal(t, []uint64{1_723_372_036_854_775_804, 5_000_000_000_000_000_000, 0, 2}, balancesA)
	outstandingB, balancesB := readMPTAmounts(t, view, idB, recipient, first, poison, last)
	require.Equal(t, uint64(5_000_000_000_000_000_001), outstandingB)
	require.Equal(t, []uint64{2_500_000_000_000_000_001, 0, 2_500_000_000_000_000_000, 0}, balancesB)
	for _, owner := range [][20]byte{first, poison, last} {
		raw, err := view.Read(keylet.Account(owner))
		require.NoError(t, err)
		account, err := state.ParseAccountRoot(raw)
		require.NoError(t, err)
		require.Equal(t, uint32(2), account.OwnerCount)
	}
}
