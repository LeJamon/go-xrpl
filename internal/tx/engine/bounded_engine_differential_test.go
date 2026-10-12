package engine

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/overlay"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

type boundedEngineBaseView struct {
	*recordingBaseView
	cfg overlay.Config
}

func (v *boundedEngineBaseView) ExecutionJournalConfig() (overlay.Config, bool) {
	return v.cfg, true
}

type offerMutationTx struct {
	*txcore.BaseTx
	key    keylet.Keylet
	result ter.Result
}

func (t offerMutationTx) Apply(ctx *txcore.ApplyContext) ter.Result {
	data, err := ctx.View.Read(t.key)
	if err != nil {
		return ter.TefINTERNAL
	}
	offer, err := state.ParseLedgerOffer(data)
	if err != nil {
		return ter.TefINTERNAL
	}
	offer.TakerGets = state.NewXRPAmountFromInt(40)
	data, err = state.SerializeLedgerOffer(offer)
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := ctx.View.Update(t.key, data); err != nil {
		return ter.TefINTERNAL
	}
	return t.result
}

type oversizedJournalTx struct {
	*txcore.BaseTx
}

func (oversizedJournalTx) Apply(ctx *txcore.ApplyContext) ter.Result {
	// The bounded view must preserve this local admission failure as an
	// operational Cause even though the handler itself returns a protocol result.
	_ = ctx.View.Insert(keylet.Keylet{Key: [32]byte{0x99}}, bytes.Repeat([]byte{0xa5}, 32<<10))
	return ter.TesSUCCESS
}

func differentialOffer(t *testing.T, view interface {
	Insert(keylet.Keylet, []byte) error
}) keylet.Keylet {
	t.Helper()
	accountID, err := state.DecodeAccountID(recoveryTestAccount)
	if err != nil {
		t.Fatal(err)
	}
	key := keylet.Offer(accountID, 2)
	data, err := state.SerializeLedgerOffer(&state.LedgerOffer{
		Account:           recoveryTestAccount,
		Sequence:          2,
		TakerPays:         state.NewXRPAmountFromInt(100),
		TakerGets:         state.NewXRPAmountFromInt(50),
		BookDirectory:     [32]byte{1},
		BookNode:          1,
		OwnerNode:         1,
		PreviousTxnID:     [32]byte{0xaa},
		PreviousTxnLgrSeq: 90,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := view.Insert(key, data); err != nil {
		t.Fatal(err)
	}
	return key
}

func assertDifferentialApply(
	t *testing.T,
	legacy, bounded txcore.ApplyResult,
	legacyView, boundedView *recordingBaseView,
	accountKey keylet.Keylet,
) {
	t.Helper()
	if legacy.Result != bounded.Result || legacy.Applied != bounded.Applied || legacy.Fee != bounded.Fee {
		t.Fatalf("result differs: legacy=(%s,%t,%d), bounded=(%s,%t,%d)",
			legacy.Result, legacy.Applied, legacy.Fee, bounded.Result, bounded.Applied, bounded.Fee)
	}
	if (legacy.Cause == nil) != (bounded.Cause == nil) {
		t.Fatalf("cause presence differs: legacy=%v bounded=%v", legacy.Cause, bounded.Cause)
	}
	if legacy.Cause != nil && legacy.Cause.Error() != bounded.Cause.Error() {
		t.Fatalf("cause differs: legacy=%v bounded=%v", legacy.Cause, bounded.Cause)
	}
	if !reflect.DeepEqual(legacy.Metadata, bounded.Metadata) {
		t.Fatalf("metadata differs:\nlegacy=%+v\nbounded=%+v", legacy.Metadata, bounded.Metadata)
	}
	legacyAccount, err := legacyView.Read(accountKey)
	if err != nil {
		t.Fatal(err)
	}
	boundedAccount, err := boundedView.Read(accountKey)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(legacyAccount, boundedAccount) {
		t.Fatalf("account bytes differ: legacy=%x bounded=%x", legacyAccount, boundedAccount)
	}
	if !reflect.DeepEqual(legacyView.data, boundedView.data) {
		t.Fatalf("ledger data differs: legacy=%v bounded=%v", legacyView.data, boundedView.data)
	}
	if legacyView.destroyed != boundedView.destroyed || legacyView.outsideAdjustments != boundedView.outsideAdjustments {
		t.Fatalf("fee accounting differs: legacy destroyed/outside=%d/%d bounded=%d/%d",
			legacyView.destroyed, legacyView.outsideAdjustments,
			boundedView.destroyed, boundedView.outsideAdjustments)
	}
}

func TestBoundedEngineMatchesLegacyExecution(t *testing.T) {
	tests := []struct {
		name         string
		balance      uint64
		withOffer    bool
		tx           func(keylet.Keylet) txcore.Transaction
		wantThreaded bool
	}{
		{
			name:    "success",
			balance: 1_000_000,
			tx: func(keylet.Keylet) txcore.Transaction {
				return successfulRecoveryTx{recoveryTx(10, 1)}
			},
		},
		{
			name:    "fee-only tec",
			balance: 5,
			tx: func(keylet.Keylet) txcore.Transaction {
				return recoveryTx(10, 1)
			},
		},
		{
			name:      "rollback",
			balance:   1_000_000,
			withOffer: true,
			tx: func(key keylet.Keylet) txcore.Transaction {
				return offerMutationTx{BaseTx: recoveryTx(10, 1), key: key, result: ter.TecUNFUNDED_PAYMENT}
			},
		},
		{
			name:         "metadata threading",
			balance:      1_000_000,
			withOffer:    true,
			wantThreaded: true,
			tx: func(key keylet.Keylet) txcore.Transaction {
				return offerMutationTx{BaseTx: recoveryTx(10, 1), key: key, result: ter.TesSUCCESS}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			legacyView := newRecordingBaseView()
			boundedBase := &boundedEngineBaseView{
				recordingBaseView: newRecordingBaseView(),
				cfg:               overlay.Config{MaxMemoryBytes: 8 << 10, MaxDiskBytes: 16 << 20, SpillDir: t.TempDir()},
			}
			legacyKey := fundRecoveryAccount(t, legacyView, tc.balance, 1)
			fundRecoveryAccount(t, boundedBase, tc.balance, 1)
			legacyOffer, boundedOffer := keylet.Keylet{}, keylet.Keylet{}
			if tc.withOffer {
				legacyOffer = differentialOffer(t, legacyView)
				boundedOffer = differentialOffer(t, boundedBase)
			}

			legacyResult := recoveryEngine(legacyView, txcore.TapNONE).Apply(tc.tx(legacyOffer))
			boundedResult := recoveryEngine(boundedBase, txcore.TapNONE).Apply(tc.tx(boundedOffer))
			assertDifferentialApply(t, legacyResult, boundedResult, legacyView, boundedBase.recordingBaseView, legacyKey)
			if tc.wantThreaded {
				if boundedResult.Metadata == nil {
					t.Fatal("bounded metadata is nil")
				}
				found := false
				for _, node := range boundedResult.Metadata.AffectedNodes {
					if node.NodeType == "ModifiedNode" && node.PreviousTxnID != "" {
						found = true
						break
					}
				}
				if !found {
					t.Fatal("metadata did not retain threaded PreviousTxnID")
				}
			}
		})
	}
}

func TestBoundedEnginePreservesJournalResourceCause(t *testing.T) {
	view := &boundedEngineBaseView{
		recordingBaseView: newRecordingBaseView(),
		cfg:               overlay.Config{MaxMemoryBytes: 8 << 10},
	}
	accountKey := fundRecoveryAccount(t, view, 1_000_000, 1)
	result := recoveryEngine(view, txcore.TapNONE).Apply(oversizedJournalTx{BaseTx: recoveryTx(10, 1)})
	if result.Result != ter.TefEXCEPTION || result.Applied {
		t.Fatalf("result=(%s,%t), want tefEXCEPTION and not applied", result.Result, result.Applied)
	}
	if !errors.Is(result.Cause, overlay.ErrSpillDisabled) {
		t.Fatalf("cause=%v, want ErrSpillDisabled", result.Cause)
	}
	account := readRecoveryAccount(t, view, accountKey)
	if account.Balance != 1_000_000 || account.Sequence != 1 {
		t.Fatalf("account balance/sequence=%d/%d, want 1000000/1", account.Balance, account.Sequence)
	}
}
