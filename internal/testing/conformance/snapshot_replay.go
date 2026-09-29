package conformance

import (
	"errors"
	"fmt"

	ledgerheader "github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

func runSnapshotReplay(fixture snapshotFixture, parent, closed loadedSnapshotLedger) error {
	response := &message.ReplayDeltaResponse{
		LedgerHash:   closed.Header.Hash[:],
		LedgerHeader: ledgerheader.AddRaw(closed.Header, false),
	}
	for i, transaction := range fixture.Closed.Transactions {
		txBlob, err := decodeSnapshotBytes("replay transaction", transaction.TxBlob)
		if err != nil {
			return err
		}
		metaBlob, err := decodeSnapshotBytes("replay metadata", transaction.MetaBlob)
		if err != nil {
			return err
		}
		leaf, err := snapshotTxLeaf(txBlob, metaBlob)
		if err != nil {
			return fmt.Errorf("replay leaf %d: %w", i, err)
		}
		response.Transactions = append(response.Transactions, leaf)
	}
	replay := inbound.NewReplayDelta(closed.Header.Hash, 1, parent.Ledger, nil)
	if err := replay.GotResponse(response); err != nil {
		return fmt.Errorf("verify response: %w", err)
	}
	if replay.IsComplete() {
		return errors.New("replay completed before transaction execution")
	}
	derived, err := replay.Apply(tx.EngineConfig{
		BaseFee:                   uint64(parent.Fees.Base),
		ReserveBase:               uint64(parent.Fees.Reserve),
		ReserveIncrement:          uint64(parent.Fees.Increment),
		NetworkID:                 fixture.NetworkID,
		Rules:                     parent.EffectiveRules,
		SkipSignatureVerification: false,
	})
	if err != nil {
		return fmt.Errorf("apply verified response: %w", err)
	}
	if !replay.IsComplete() {
		return errors.New("successful replay did not complete")
	}
	if err := assertSnapshotLedger(derived, closed); err != nil {
		return err
	}
	result, err := replay.Result()
	if err != nil {
		return fmt.Errorf("verified result: %w", err)
	}
	if err := assertSnapshotLedger(result, closed); err != nil {
		return fmt.Errorf("verified result: %w", err)
	}
	if err := assertSnapshotLedger(parent.Ledger, parent); err != nil {
		return fmt.Errorf("parent changed during replay: %w", err)
	}
	return nil
}
