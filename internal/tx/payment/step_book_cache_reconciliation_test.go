package payment

import (
	"encoding/binary"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

type bookWalkFixture struct {
	step         *BookStep
	sb           *PaymentSandbox
	afView       *PaymentSandbox
	removals     map[[32]byte]bool
	primaryKey   [32]byte
	trailingKey  [32]byte
	danglingKey  [32]byte
	directoryKey [32]byte
}

func newBookWalkFixture(
	t *testing.T,
	inIssue, outIssue Issue,
	primaryOwner, trailingOwner [20]byte,
	takerPays, takerGets state.Amount,
	quality Quality,
) *bookWalkFixture {
	t.Helper()

	view := newPaymentMockLedgerView()
	view.createAccount(primaryOwner, 10_000_000_000, 1)
	view.createAccount(trailingOwner, 10_000_000_000, 1)

	var source, destination [20]byte
	copy(source[:], []byte("source12345678901234"))
	copy(destination[:], []byte("destination123456789"))
	step := NewBookStep(inIssue, outIssue, source, destination, nil, false)
	step.fixReducedOffersV2 = true

	directoryKey := step.bookBaseKey()
	binary.BigEndian.PutUint64(directoryKey[24:], quality.Value)
	primaryKey := insertTestBookOffer(t, view, primaryOwner, 1, directoryKey, takerPays, takerGets)
	trailingKey := insertTestBookOffer(t, view, trailingOwner, 1, directoryKey, takerPays, takerGets)
	var danglingKey [32]byte
	copy(danglingKey[:], []byte("dangling-cache-offer-32-bytes-key")[:32])

	directory := &state.DirectoryNode{
		RootIndex:         directoryKey,
		Indexes:           [][32]byte{primaryKey, trailingKey, danglingKey},
		TakerPaysCurrency: keylet.CurrencyBytes(inIssue.Currency),
		TakerPaysIssuer:   inIssue.Issuer,
		TakerGetsCurrency: keylet.CurrencyBytes(outIssue.Currency),
		TakerGetsIssuer:   outIssue.Issuer,
	}
	directoryData, err := state.SerializeDirectoryNode(directory, true)
	require.NoError(t, err)
	view.data[directoryKey] = directoryData

	afView := NewPaymentSandbox(view)
	sb := NewChildSandbox(afView)
	sb.SetTransactionContext([32]byte{}, 1)

	return &bookWalkFixture{
		step:         step,
		sb:           sb,
		afView:       afView,
		removals:     make(map[[32]byte]bool),
		primaryKey:   primaryKey,
		trailingKey:  trailingKey,
		danglingKey:  danglingKey,
		directoryKey: directoryKey,
	}
}

func insertTestBookOffer(
	t *testing.T,
	view *paymentMockLedgerView,
	owner [20]byte,
	sequence uint32,
	directory [32]byte,
	takerPays, takerGets state.Amount,
) [32]byte {
	t.Helper()

	offerKey := keylet.Offer(owner, sequence).Key
	ownerDirectoryKey := keylet.OwnerDir(owner).Key
	ownerDirectory := &state.DirectoryNode{RootIndex: ownerDirectoryKey, Owner: owner}
	if data := view.data[ownerDirectoryKey]; data != nil {
		var err error
		ownerDirectory, err = state.ParseDirectoryNode(data)
		require.NoError(t, err)
	}
	ownerDirectory.Indexes = append(ownerDirectory.Indexes, offerKey)
	ownerDirectoryData, err := state.SerializeDirectoryNode(ownerDirectory, false)
	require.NoError(t, err)
	view.data[ownerDirectoryKey] = ownerDirectoryData

	offer := &state.LedgerOffer{
		Account:       state.EncodeAccountIDSafe(owner),
		Sequence:      sequence,
		TakerPays:     takerPays,
		TakerGets:     takerGets,
		BookDirectory: directory,
	}
	offerData, err := state.SerializeLedgerOffer(offer)
	require.NoError(t, err)
	view.data[offerKey] = offerData
	return offerKey
}

func (f *bookWalkFixture) requireDirectoryIndexes(t *testing.T, view *PaymentSandbox, expected [][32]byte) {
	t.Helper()
	data, err := view.Read(keylet.DirPage(f.directoryKey, 0))
	require.NoError(t, err)
	require.NotNil(t, data)
	directory, err := state.ParseDirectoryNode(data)
	require.NoError(t, err)
	require.Equal(t, expected, directory.Indexes)
}

func TestBookStepFwdCacheReconciliationStopsAtFundedPartialOffer(t *testing.T) {
	var issuer, trailingOwner [20]byte
	copy(issuer[:], []byte("issuer12345678901234"))
	copy(trailingOwner[:], []byte("trailing123456789012"))
	issuerString := state.EncodeAccountIDSafe(issuer)

	inIssue := Issue{Currency: "XRP"}
	outIssue := Issue{Currency: "USD", Issuer: issuer}
	offerIn := NewXRPEitherAmount(100_000_000)
	offerOut := NewIOUEitherAmount(tx.NewIssuedAmountFromFloat64(100, "USD", issuerString))
	quality := QualityFromAmounts(offerIn, offerOut)
	fixture := newBookWalkFixture(
		t,
		inIssue,
		outIssue,
		issuer,
		trailingOwner,
		tx.NewXRPAmount(100_000_000),
		tx.NewIssuedAmountFromFloat64(100, "USD", issuerString),
		quality,
	)

	input := NewXRPEitherAmount(50_000_000)
	cacheOut := NewIOUEitherAmount(tx.NewIssuedAmount(4_999_999_999_999_999, -14, "USD", issuerString))
	_, provisionalOut := quality.CeilInStrictWithNumberContext(offerIn, offerOut, input, false, fixture.sb.NumberContext())
	reverseIn, _ := quality.CeilOutStrict(offerIn, offerOut, cacheOut, true)
	require.Greater(t, provisionalOut.Compare(cacheOut), 0)
	require.Equal(t, 0, reverseIn.Compare(input))
	fixture.step.cache = &bookCache{in: input, out: cacheOut}

	gotIn, gotOut := fixture.step.Fwd(fixture.sb, fixture.afView, fixture.removals, input)

	require.Equal(t, 0, gotIn.Compare(input))
	require.Equal(t, 0, gotOut.Compare(cacheOut))
	require.Equal(t, uint32(1), fixture.step.OffersUsed())
	require.False(t, fixture.removals[fixture.trailingKey])
	require.False(t, fixture.step.PermRemovals()[fixture.trailingKey])

	primaryData, err := fixture.sb.Read(keylet.Keylet{Key: fixture.primaryKey})
	require.NoError(t, err)
	require.NotNil(t, primaryData)
	primary, err := state.ParseLedgerOffer(primaryData)
	require.NoError(t, err)
	require.Equal(t, int64(50_000_000), primary.TakerPays.Drops())
	require.False(t, primary.TakerGets.IsZero())

	expectedIndexes := [][32]byte{fixture.primaryKey, fixture.trailingKey, fixture.danglingKey}
	fixture.requireDirectoryIndexes(t, fixture.sb, expectedIndexes)
	fixture.requireDirectoryIndexes(t, fixture.afView, expectedIndexes)
}

func TestBookStepFwdContinuationControls(t *testing.T) {
	t.Run("full take continues", func(t *testing.T) {
		var issuer, trailingOwner [20]byte
		issuer[19] = 1
		trailingOwner[19] = 2
		issuerString := state.EncodeAccountIDSafe(issuer)
		offerIn := NewXRPEitherAmount(100_000_000)
		offerOut := NewIOUEitherAmount(tx.NewIssuedAmountFromFloat64(100, "USD", issuerString))
		fixture := newBookWalkFixture(
			t,
			Issue{Currency: "XRP"},
			Issue{Currency: "USD", Issuer: issuer},
			issuer,
			trailingOwner,
			tx.NewXRPAmount(100_000_000),
			tx.NewIssuedAmountFromFloat64(100, "USD", issuerString),
			QualityFromAmounts(offerIn, offerOut),
		)

		input := NewXRPEitherAmount(100_000_000)
		fixture.step.Fwd(fixture.sb, fixture.afView, fixture.removals, input)

		require.Greater(t, fixture.step.OffersUsed(), uint32(1))
		require.True(t, fixture.removals[fixture.trailingKey])
	})

	t.Run("ordinary partial take stops", func(t *testing.T) {
		var issuer, trailingOwner [20]byte
		issuer[19] = 1
		trailingOwner[19] = 2
		issuerString := state.EncodeAccountIDSafe(issuer)
		offerIn := NewXRPEitherAmount(100_000_000)
		offerOut := NewIOUEitherAmount(tx.NewIssuedAmountFromFloat64(100, "USD", issuerString))
		fixture := newBookWalkFixture(
			t,
			Issue{Currency: "XRP"},
			Issue{Currency: "USD", Issuer: issuer},
			issuer,
			trailingOwner,
			tx.NewXRPAmount(100_000_000),
			tx.NewIssuedAmountFromFloat64(100, "USD", issuerString),
			QualityFromAmounts(offerIn, offerOut),
		)

		fixture.step.Fwd(fixture.sb, fixture.afView, fixture.removals, NewXRPEitherAmount(50_000_000))

		require.Equal(t, uint32(1), fixture.step.OffersUsed())
		require.False(t, fixture.removals[fixture.trailingKey])
	})

	t.Run("fully consumed reconciliation continues", func(t *testing.T) {
		var issuer, trailingOwner [20]byte
		issuer[19] = 1
		trailingOwner[19] = 2
		issuerString := state.EncodeAccountIDSafe(issuer)
		offerIn := NewIOUEitherAmount(tx.NewIssuedAmount(5_000_000_000_000_000, -96, "USD", issuerString))
		offerOut := NewIOUEitherAmount(tx.NewIssuedAmount(9_000_000_000_000_000, -96, "EUR", issuerString))
		quality := QualityFromAmounts(offerIn, offerOut)
		fixture := newBookWalkFixture(
			t,
			Issue{Currency: "USD", Issuer: issuer},
			Issue{Currency: "EUR", Issuer: issuer},
			issuer,
			trailingOwner,
			tx.NewIssuedAmount(5_000_000_000_000_000, -96, "USD", issuerString),
			tx.NewIssuedAmount(9_000_000_000_000_000, -96, "EUR", issuerString),
			quality,
		)
		input := NewIOUEitherAmount(tx.NewIssuedAmount(4_999_999_999_999_999, -96, "USD", issuerString))
		cacheOut := NewIOUEitherAmount(tx.NewIssuedAmount(8_999_999_999_999_996, -96, "EUR", issuerString))
		_, provisionalOut := quality.CeilInStrictWithNumberContext(offerIn, offerOut, input, false, fixture.sb.NumberContext())
		reverseIn, _ := quality.CeilOutStrict(offerIn, offerOut, cacheOut, true)
		require.Greater(t, provisionalOut.Compare(cacheOut), 0)
		require.Equal(t, 0, reverseIn.Compare(input))
		fixture.step.cache = &bookCache{in: input, out: cacheOut}

		fixture.step.Fwd(fixture.sb, fixture.afView, fixture.removals, input)

		primaryData, err := fixture.sb.Read(keylet.Keylet{Key: fixture.primaryKey})
		require.NoError(t, err)
		require.Nil(t, primaryData)
		require.Greater(t, fixture.step.OffersUsed(), uint32(1))
		require.True(t, fixture.removals[fixture.trailingKey])
	})
}
