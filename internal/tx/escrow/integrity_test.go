package escrow

import (
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

type faultView struct {
	*mapView
	readErrors   map[[32]byte]error
	updateErrors map[[32]byte]error
}

func newFaultView() *faultView {
	return &faultView{
		mapView:      newMapView(),
		readErrors:   make(map[[32]byte]error),
		updateErrors: make(map[[32]byte]error),
	}
}

func (v *faultView) Read(k keylet.Keylet) ([]byte, error) {
	if err := v.readErrors[k.Key]; err != nil {
		return nil, err
	}
	return v.mapView.Read(k)
}

func (v *faultView) Update(k keylet.Keylet, data []byte) error {
	if err := v.updateErrors[k.Key]; err != nil {
		return err
	}
	return v.mapView.Update(k, data)
}

func TestReadDestinationForEscrowDistinguishesAbsenceAndCorruption(t *testing.T) {
	var destination [20]byte
	destination[0] = 1
	key := keylet.Account(destination)

	view := newFaultView()
	_, result := readDestinationForEscrow(view, destination)
	require.Equal(t, ter.TecNO_DST, result)

	view.readErrors[key.Key] = errors.New("read failed")
	_, result = readDestinationForEscrow(view, destination)
	require.Equal(t, ter.TefINTERNAL, result)

	delete(view.readErrors, key.Key)
	require.NoError(t, view.Insert(key, []byte{0xff}))
	_, result = readDestinationForEscrow(view, destination)
	require.Equal(t, ter.TefINTERNAL, result)
}

func TestMapDirInsertErrorOnlyReportsDirectoryFull(t *testing.T) {
	require.Equal(t, ter.TecDIR_FULL, mapDirInsertError(state.ErrDirFull))
	require.Equal(t, ter.TecDIR_FULL, mapDirInsertError(errors.Join(errors.New("wrapped"), state.ErrDirFull)))
	require.Equal(t, ter.TefINTERNAL, mapDirInsertError(errors.New("write failed")))
}

func TestAdjustOwnerCountViaViewFailsClosed(t *testing.T) {
	var accountID [20]byte
	accountID[0] = 2
	accountKey := keylet.Account(accountID)

	t.Run("missing account", func(t *testing.T) {
		require.Equal(t, ter.TefBAD_LEDGER, adjustOwnerCountViaView(newFaultView(), accountID, 1))
	})

	t.Run("read error", func(t *testing.T) {
		view := newFaultView()
		view.readErrors[accountKey.Key] = errors.New("read failed")
		require.Equal(t, ter.TefINTERNAL, adjustOwnerCountViaView(view, accountID, 1))
	})

	t.Run("corrupt account", func(t *testing.T) {
		view := newFaultView()
		require.NoError(t, view.Insert(accountKey, []byte{0xff}))
		require.Equal(t, ter.TefINTERNAL, adjustOwnerCountViaView(view, accountID, 1))
	})

	t.Run("underflow", func(t *testing.T) {
		view := newFaultView()
		seedAccountForEscrow(t, view.mapView, accountID, 0)
		require.Equal(t, ter.TefBAD_LEDGER, adjustOwnerCountViaView(view, accountID, -1))
		require.Equal(t, uint32(0), ownerCountOf(t, view.mapView, accountID))
	})

	t.Run("update error", func(t *testing.T) {
		view := newFaultView()
		seedAccountForEscrow(t, view.mapView, accountID, 1)
		view.updateErrors[accountKey.Key] = errors.New("update failed")
		require.Equal(t, ter.TefINTERNAL, adjustOwnerCountViaView(view, accountID, -1))
		require.Equal(t, uint32(1), ownerCountOf(t, view.mapView, accountID))
	})
}

func TestResyncSelfOwnerCountFailsClosed(t *testing.T) {
	var accountID [20]byte
	accountID[0] = 7
	accountKey := keylet.Account(accountID)
	view := newFaultView()
	ctx := &tx.ApplyContext{View: view, AccountID: accountID, Account: &state.AccountRoot{OwnerCount: 1}}

	require.Equal(t, ter.TefBAD_LEDGER, resyncSelfOwnerCount(ctx))
	view.readErrors[accountKey.Key] = errors.New("read failed")
	require.Equal(t, ter.TefINTERNAL, resyncSelfOwnerCount(ctx))

	delete(view.readErrors, accountKey.Key)
	require.NoError(t, view.Insert(accountKey, []byte{0xff}))
	require.Equal(t, ter.TefINTERNAL, resyncSelfOwnerCount(ctx))

	require.NoError(t, view.Erase(accountKey))
	seedAccountForEscrow(t, view.mapView, accountID, 4)
	require.Equal(t, ter.TesSUCCESS, resyncSelfOwnerCount(ctx))
	require.Equal(t, uint32(4), ctx.Account.OwnerCount)
}

func TestComputeMPTTransferFeeFailsClosedOnIssuanceLookup(t *testing.T) {
	const issuanceID = "0000000100000000000000000000000000000000000000AB"
	issuanceKey, err := mptIssuanceKeyFromHex(issuanceID)
	require.NoError(t, err)
	context := state.NewNumberContext(state.MantissaScaleLarge, true)
	var sender, receiver [20]byte
	sender[0] = 3
	receiver[0] = 4

	view := newFaultView()
	_, _, result := computeMPTTransferFee(view, parityRate, issuanceID, sender, receiver, 100, context)
	require.Equal(t, ter.TecOBJECT_NOT_FOUND, result)

	view.readErrors[issuanceKey.Key] = errors.New("read failed")
	_, _, result = computeMPTTransferFee(view, parityRate, issuanceID, sender, receiver, 100, context)
	require.Equal(t, ter.TefINTERNAL, result)

	delete(view.readErrors, issuanceKey.Key)
	require.NoError(t, view.Insert(issuanceKey, []byte{0xff}))
	_, _, result = computeMPTTransferFee(view, parityRate, issuanceID, sender, receiver, 100, context)
	require.Equal(t, ter.TefINTERNAL, result)
}

func TestIOUStateHelpersFailClosed(t *testing.T) {
	var issuerID, holderID [20]byte
	issuerID[0] = 5
	holderID[0] = 6
	issuerKey := keylet.Account(issuerID)

	view := newFaultView()
	_, result := getIOUTransferRate(view, issuerID)
	require.Equal(t, ter.TecNO_ISSUER, result)
	_, result = isIOUFrozen(view, holderID, issuerID, "USD")
	require.Equal(t, ter.TecNO_ISSUER, result)

	view.readErrors[issuerKey.Key] = errors.New("read failed")
	_, result = getIOUTransferRate(view, issuerID)
	require.Equal(t, ter.TefINTERNAL, result)
	_, result = isIOUFrozen(view, holderID, issuerID, "USD")
	require.Equal(t, ter.TefINTERNAL, result)

	delete(view.readErrors, issuerKey.Key)
	seedAccountForEscrow(t, view.mapView, issuerID, 0)
	lineKey := keylet.Line(holderID, issuerID, "USD")
	view.readErrors[lineKey.Key] = errors.New("read failed")
	_, result = isIOUFrozen(view, holderID, issuerID, "USD")
	require.Equal(t, ter.TefINTERNAL, result)
	_, result = isIOUDeepFrozen(view, holderID, issuerID, "USD")
	require.Equal(t, ter.TefINTERNAL, result)

	delete(view.readErrors, lineKey.Key)
	require.NoError(t, view.Insert(lineKey, []byte{0xff}))
	_, result = isIOUFrozen(view, holderID, issuerID, "USD")
	require.Equal(t, ter.TefINTERNAL, result)
	_, result = isIOUDeepFrozen(view, holderID, issuerID, "USD")
	require.Equal(t, ter.TefINTERNAL, result)
}
