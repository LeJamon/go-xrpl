package txq

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/stretchr/testify/require"
)

func TestSeqProxyForPreservesTicketFieldPresence(t *testing.T) {
	zero := uint32(0)
	sequence := uint32(7)

	tests := []struct {
		name        string
		common      *tx.Common
		want        SeqProxy
		wantPresent bool
	}{
		{
			name: "ticket zero is still a ticket",
			common: &tx.Common{
				Sequence:       &zero,
				TicketSequence: &zero,
			},
			want:        NewSeqProxyTicket(0),
			wantPresent: true,
		},
		{
			name: "nonzero sequence takes precedence",
			common: &tx.Common{
				Sequence:       &sequence,
				TicketSequence: &zero,
			},
			want:        NewSeqProxySequence(sequence),
			wantPresent: true,
		},
		{
			name: "sequence zero without ticket remains sequence",
			common: &tx.Common{
				Sequence: &zero,
			},
			want:        NewSeqProxySequence(0),
			wantPresent: true,
		},
		{
			name:        "missing sequence fields are absent",
			common:      &tx.Common{},
			want:        SeqProxy{},
			wantPresent: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, present := seqProxyFor(test.common)
			require.Equal(t, test.want, got)
			require.Equal(t, test.wantPresent, present)
		})
	}
}

func TestApplyPresentZeroTicketReturnsNoTicket(t *testing.T) {
	q := mustNew(makeAdmissionConfig())
	account := [20]byte{9}
	ctx := &stubApplyCtx{
		seq:        1,
		balance:    1_000_000_000,
		exists:     true,
		baseFee:    10,
		txInLedger: 100,
		preflight:  ter.TesSUCCESS,
		preclaim:   ter.TesSUCCESS,
	}

	transaction := &engineAuditZeroTicketTx{seqTx: seqTx{fee: "10"}}
	result := q.Apply(ctx, transaction, [32]byte{0xD2}, account)

	require.Equal(t, ter.TefNO_TICKET, result.Result)
	require.False(t, result.Applied)
	require.False(t, result.Queued)
	require.Zero(t, q.Size())
}

func TestApplyDryRunNeverQueuesBelowDirectApplyFee(t *testing.T) {
	q := mustNew(makeAdmissionConfig())
	ctx := &stubApplyCtx{
		seq:        1,
		balance:    1_000_000_000,
		exists:     true,
		baseFee:    10,
		txInLedger: 100,
		flags:      tx.TapDRY_RUN,
		preflight:  ter.TesSUCCESS,
		preclaim:   ter.TesSUCCESS,
	}

	transaction := account.NewAccountSet("rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh")
	transaction.SetSequence(1)
	transaction.Fee = "10"
	result := q.Apply(ctx, transaction, [32]byte{}, [20]byte{})

	require.Equal(t, ter.TelCAN_NOT_QUEUE, result.Result)
	require.False(t, result.Applied)
	require.False(t, result.Queued)
	require.Zero(t, q.Size())
}

func TestApplyMissingAccountDryRunUsesCannotQueue(t *testing.T) {
	for _, test := range []struct {
		name   string
		flags  tx.ApplyFlags
		result ter.Result
	}{
		{name: "dry run", flags: tx.TapDRY_RUN, result: ter.TelCAN_NOT_QUEUE},
		{name: "submission", result: ter.TerNO_ACCOUNT},
	} {
		t.Run(test.name, func(t *testing.T) {
			q := mustNew(makeAdmissionConfig())
			ctx := &stubApplyCtx{
				seq:        1,
				balance:    1_000_000_000,
				exists:     false,
				baseFee:    10,
				txInLedger: 100,
				flags:      test.flags,
				preflight:  ter.TesSUCCESS,
				preclaim:   ter.TesSUCCESS,
			}
			transaction := account.NewAccountSet("rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh")
			transaction.SetSequence(1)
			transaction.Fee = "10"
			result := q.Apply(ctx, transaction, [32]byte{}, [20]byte{})

			require.Equal(t, test.result, result.Result)
			require.False(t, result.Applied)
			require.False(t, result.Queued)
			require.Zero(t, q.Size())
		})
	}
}

type engineAuditZeroTicketTx struct{ seqTx }

func (m *engineAuditZeroTicketTx) GetCommon() *tx.Common {
	common := m.seqTx.GetCommon()
	zero := uint32(0)
	common.Sequence = &zero
	common.TicketSequence = &zero
	return common
}
