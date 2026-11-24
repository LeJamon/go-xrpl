package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

type oneShotSettingsReadError struct {
	shamap.Family
	leaf   [32]byte
	err    error
	failed atomic.Bool
}

func (f *oneShotSettingsReadError) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	if hash == f.leaf && f.failed.CompareAndSwap(false, true) {
		return nil, f.err
	}
	return f.Family.Fetch(ctx, hash)
}

func TestConsensusCloseParentSettingsReadError(t *testing.T) {
	all.RegisterAll()
	control := newConsensusParentGuardService(t)
	genesis := control.GetClosedLedger()
	updateTime := genesis.CloseTime().Add(time.Second)
	updated, err := ledger.NewOpen(genesis, updateTime)
	require.NoError(t, err)
	amendments, err := pseudo.SerializeAmendmentsSLE(&pseudo.AmendmentsSLE{
		Amendments: [][32]byte{amendment.FeatureXRPFees},
	})
	require.NoError(t, err)
	require.NoError(t, updated.Insert(keylet.Amendments(), amendments))
	fees, err := state.SerializeFeeSettings(&state.FeeSettings{
		XRPFeesMode:           true,
		BaseFeeDrops:          20,
		ReserveBaseDrops:      20_000_000,
		ReserveIncrementDrops: 2_000_000,
	})
	require.NoError(t, err)
	require.NoError(t, updated.Update(keylet.Fees(), fees))
	require.NoError(t, updated.Close(updateTime, 0))

	parent, backing := coldAcceptanceParent(t, updated)
	require.NoError(t, control.SwitchToPreferredLedger(parent))
	closeTime := parent.CloseTime().Add(time.Second)
	nextSeq := parent.Sequence() + 1
	setFee := pseudo.NewSetFee()
	setFee.LedgerSequence = &nextSeq
	setFee.BaseFeeDrops = "30"
	setFee.ReserveBaseDrops = "20000000"
	setFee.ReserveIncrementDrops = "2000000"
	blob, err := pseudo.EncodePseudoTx(setFee)
	require.NoError(t, err)
	agreed := [][]byte{blob}
	_, err = control.AcceptConsensusResult(t.Context(), parent, agreed, nil, closeTime, true)
	require.NoError(t, err)
	want := control.GetClosedLedger()
	require.EqualValues(t, 1, want.TxCount(), "the parent enables modern SetFee")

	for _, tc := range []struct {
		name string
		key  keylet.Keylet
		data []byte
	}{
		{"amendments", keylet.Amendments(), amendments},
		{"fees after a schedule change", keylet.Fees(), fees},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newConsensusParentGuardService(t)
			coldState, err := shamap.NewFromRootHash(shamap.TypeState, parent.Header().AccountHash, backing.Family)
			require.NoError(t, err)
			cold, err := ledger.NewFromHeader(parent.Header(), coldState, shamap.New(shamap.TypeTransaction), parent.Fees())
			require.NoError(t, err)
			require.NoError(t, svc.SwitchToPreferredLedger(cold))
			prefix := protocol.HashPrefixLeafNode()
			fault := &oneShotSettingsReadError{
				Family: backing.Family,
				leaf:   sha512half.Sum(prefix[:], tc.data, tc.key.Key[:]),
				err:    errors.New("transient settings read error"),
			}
			cold.SetStateMapFamily(fault)

			_, err = svc.AcceptConsensusResult(t.Context(), cold, agreed, nil, closeTime, true)
			require.ErrorIs(t, err, fault.err)
			require.Same(t, cold, svc.GetClosedLedger())

			_, err = svc.AcceptConsensusResult(t.Context(), cold, agreed, nil, closeTime, true)
			require.NoError(t, err)
			require.Equal(t, want.Hash(), svc.GetClosedLedger().Hash())
		})
	}
}

func TestConsensusCloseFeeSettingsLookup(t *testing.T) {
	foreign, err := pseudo.SerializeAmendmentsSLE(&pseudo.AmendmentsSLE{
		Amendments: [][32]byte{amendment.FeatureXRPFees},
	})
	require.NoError(t, err)
	for _, tc := range []struct {
		name    string
		leaf    []byte
		wantErr bool
	}{
		{name: "absent"},
		{name: "different entry type", leaf: foreign},
		{name: "unreadable type", leaf: make([]byte, 16), wantErr: true},
		{name: "malformed different entry", leaf: foreign[:len(foreign)-1], wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newConsensusParentGuardService(t)
			genesis := svc.GetClosedLedger()
			closeTime := genesis.CloseTime().Add(time.Second)
			parent, err := ledger.NewOpen(genesis, closeTime)
			require.NoError(t, err)
			if tc.leaf == nil {
				require.NoError(t, parent.Erase(keylet.Fees()))
			} else {
				require.NoError(t, parent.Update(keylet.Fees(), tc.leaf))
			}
			require.NoError(t, parent.Close(closeTime, 0))
			require.NoError(t, svc.SwitchToPreferredLedger(parent))
			_, err = svc.AcceptConsensusResult(t.Context(), parent, nil, nil, closeTime.Add(time.Second), true)
			if tc.wantErr {
				require.ErrorContains(t, err, "read parent fee settings")
				require.Same(t, parent, svc.GetClosedLedger())
				return
			}
			require.NoError(t, err)
			require.Equal(t, parent.Hash(), svc.GetClosedLedger().ParentHash())
		})
	}
}

func TestConsensusCloseFeeSettingsActivation(t *testing.T) {
	svc := newConsensusParentGuardService(t)
	genesis := svc.GetClosedLedger()
	closeTime := genesis.CloseTime().Add(time.Second)
	parent, err := ledger.NewOpen(genesis, closeTime)
	require.NoError(t, err)
	fees, err := state.SerializeFeeSettings(&state.FeeSettings{
		XRPFeesMode:           true,
		BaseFeeDrops:          23,
		ReserveBaseDrops:      20_000_000,
		ReserveIncrementDrops: 2_000_000,
	})
	require.NoError(t, err)
	require.NoError(t, parent.Update(keylet.Fees(), fees))
	require.NoError(t, parent.Close(closeTime, 0))
	require.False(t, parent.Rules().XRPFeesEnabled())
	require.NoError(t, svc.SwitchToPreferredLedger(parent))
	nextClose := closeTime.Add(time.Second)
	_, err = svc.AcceptConsensusResult(t.Context(), parent, nil, nil, nextClose, true)
	require.ErrorIs(t, err, state.ErrInvalidFeeSettings)
	require.Same(t, parent, svc.GetClosedLedger())

	enabled, err := ledger.NewOpen(parent, nextClose)
	require.NoError(t, err)
	amendments, err := pseudo.SerializeAmendmentsSLE(&pseudo.AmendmentsSLE{
		Amendments: [][32]byte{amendment.FeatureXRPFees},
	})
	require.NoError(t, err)
	require.NoError(t, enabled.Insert(keylet.Amendments(), amendments))
	require.NoError(t, enabled.Close(nextClose, 0))
	// The validated ledger can lag a closed parent that has activated XRPFees.
	open, err := ledger.NewOpenWithRules(enabled, nextClose.Add(time.Second), parent.Rules())
	require.NoError(t, err)
	base, reserve, increment, err := FeesFromLedgerStrict(open)
	require.NoError(t, err)
	require.EqualValues(t, 23, base)
	require.EqualValues(t, 20_000_000, reserve)
	require.EqualValues(t, 2_000_000, increment)
}
