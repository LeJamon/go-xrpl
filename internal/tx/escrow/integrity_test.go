package escrow

import (
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
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
	_, result = isIOUDeepFrozen(view, holderID, issuerID, "USD")
	require.Equal(t, ter.TefINTERNAL, result)
}
