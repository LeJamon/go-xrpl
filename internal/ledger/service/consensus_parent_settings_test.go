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
