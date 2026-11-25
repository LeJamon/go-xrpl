package replaytool

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/statecompare"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

// replayHistoryClient is the subset of the state-compare client needed to
// recover a fee schedule. It deliberately does not require state checkpoints:
// a transaction pack and its ledger snapshot are sufficient to verify a prior
// SetFee, even when the corresponding state pack was not archived.
type replayHistoryClient interface {
	Snapshot(context.Context, uint32) (*statecompare.LedgerSnapshot, error)
	Transactions(context.Context, *statecompare.LedgerSnapshot) ([]statecompare.Transaction, error)
}

type replayGenesisStateClient interface {
	StreamStateEntries(context.Context, *statecompare.LedgerSnapshot, func(statecompare.StateEntry) error) error
}

func readReplayFeeSettings(ctx context.Context, stateMap *shamap.SHAMap, rules *amendment.Rules) (*state.FeeSettings, error) {
	settings, err := ledger.ReadFeeSettings(ctx, stateMap)
	if err != nil {
		return nil, fmt.Errorf("parsing FeeSettings: %w", err)
	}
	if settings != nil && settings.IsUsingModernFees() && (rules == nil || !rules.XRPFeesEnabled()) {
		return nil, fmt.Errorf("%w: XRPFees fields are present before the amendment is enabled", state.ErrInvalidFeeSettings)
	}
	return settings, nil
}

func feeScheduleComplete(settings *state.FeeSettings) bool {
	if settings == nil {
		return false
	}
	if settings.IsUsingModernFees() {
		return settings.HasBaseFeeDrops && settings.HasReserveBaseDrops && settings.HasReserveIncrementDrops
	}
	return settings.HasBaseFee && settings.HasReserveBase && settings.HasReserveIncrement
}

// resolveReplaySeedFees returns the effective schedule for a seed state. A
// persisted checkpoint schedule is authoritative when present; otherwise a
// partial FeeSettings entry must inherit from a verified successful SetFee
// rather than silently replacing omitted fields with defaults.
func resolveReplaySeedFees(
	ctx context.Context,
	client replayHistoryClient,
	snapshot *statecompare.LedgerSnapshot,
	stateMap *shamap.SHAMap,
	inherited drops.Fees,
	checkpointFees *drops.Fees,
) (drops.Fees, error) {
	rules, err := loadRulesFromState(stateMap)
	if err != nil {
		return drops.Fees{}, err
	}
	settings, err := readReplayFeeSettings(ctx, stateMap, rules)
	if err != nil {
		return drops.Fees{}, err
	}
	if checkpointFees != nil {
		inherited = *checkpointFees
	}
	if checkpointFees != nil || feeScheduleComplete(settings) {
		return settings.ApplyTo(inherited), nil
	}
	if snapshot == nil {
		return drops.Fees{}, errors.New("cannot recover partial FeeSettings without a seed snapshot")
	}
	if settings == nil {
		if snapshot.LedgerIndex <= 1 {
			return inherited, nil
		}
		return drops.Fees{}, fmt.Errorf("seed ledger %d has no FeeSettings entry and no inherited fee schedule", snapshot.LedgerIndex)
	}
	if snapshot.LedgerIndex <= 1 {
		return settings.ApplyTo(inherited), nil
	}

	historyFees, err := recoverPriorFeeSchedule(ctx, client, snapshot, settings)
	if err != nil {
		return drops.Fees{}, err
	}
	return settings.ApplyTo(historyFees), nil
}

func recoverPriorFeeSchedule(
	ctx context.Context,
	client replayHistoryClient,
	current *statecompare.LedgerSnapshot,
	settings *state.FeeSettings,
) (drops.Fees, error) {
	if client == nil {
		return drops.Fees{}, errors.New("cannot recover partial FeeSettings without an archive client")
	}
	if settings.PreviousTxnLgrSeq != 0 || settings.PreviousTxnID != ([32]byte{}) {
		if settings.PreviousTxnLgrSeq == 0 || settings.PreviousTxnID == ([32]byte{}) {
			return drops.Fees{}, errors.New("partial FeeSettings has an incomplete PreviousTxnID/PreviousTxnLgrSeq history pointer")
		}
		if settings.PreviousTxnLgrSeq > current.LedgerIndex {
			return drops.Fees{}, fmt.Errorf("partial FeeSettings points to future ledger %d from seed ledger %d", settings.PreviousTxnLgrSeq, current.LedgerIndex)
		}
		prior := current
		if settings.PreviousTxnLgrSeq != current.LedgerIndex {
			var err error
			prior, err = client.Snapshot(ctx, settings.PreviousTxnLgrSeq)
			if err != nil {
				return drops.Fees{}, fmt.Errorf("loading FeeSettings history ledger %d: %w", settings.PreviousTxnLgrSeq, err)
			}
			if prior == nil || prior.LedgerIndex != settings.PreviousTxnLgrSeq {
				return drops.Fees{}, fmt.Errorf("FeeSettings history ledger %d returned an invalid snapshot", settings.PreviousTxnLgrSeq)
			}
			if settings.PreviousTxnLgrSeq+1 == current.LedgerIndex {
				if err := validateReplaySnapshotLink(prior, current, current.LedgerIndex); err != nil {
					return drops.Fees{}, fmt.Errorf("validating FeeSettings history link: %w", err)
				}
			}
		}
		return feeScheduleFromReferencedLedger(ctx, client, prior, settings.PreviousTxnID)
	}

	// Older entries may not carry threading fields. Walk the available ledger
	// snapshots, validating each adjacent parent link, until a successful SetFee
	// is found or the verified chain reaches genesis.
	for {
		transactions, err := transactionsOrEmpty(ctx, client, current)
		if err != nil {
			return drops.Fees{}, fmt.Errorf("loading FeeSettings history ledger %d: %w", current.LedgerIndex, err)
		}
		var latestFees drops.Fees
		var latestIndex uint32
		foundSetFee := false
		for _, record := range transactions {
			fees, isSetFee, err := parseSuccessfulSetFee(record)
			if err != nil {
				return drops.Fees{}, fmt.Errorf("validating FeeSettings history transaction %x at ledger %d: %w", record.TxHash, current.LedgerIndex, err)
			}
			if isSetFee && (!foundSetFee || record.TxIndex >= latestIndex) {
				latestFees = fees
				latestIndex = record.TxIndex
				foundSetFee = true
			}
		}
		if foundSetFee {
			return latestFees, nil
		}
		if current.LedgerIndex <= 1 {
			return genesisFeeSchedule(ctx, client, current)
		}
		previous, err := client.Snapshot(ctx, current.LedgerIndex-1)
		if err != nil {
			return drops.Fees{}, fmt.Errorf("loading FeeSettings history ledger %d: %w", current.LedgerIndex-1, err)
		}
		if err := validateReplaySnapshotLink(previous, current, current.LedgerIndex); err != nil {
			return drops.Fees{}, fmt.Errorf("validating FeeSettings history at ledger %d: %w", current.LedgerIndex-1, err)
		}
		current = previous
	}
}

func transactionsOrEmpty(ctx context.Context, client replayHistoryClient, snapshot *statecompare.LedgerSnapshot) ([]statecompare.Transaction, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	transactions, err := client.Transactions(ctx, snapshot)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
	}
	if err != nil && snapshot != nil && snapshot.TransactionCount == 0 && snapshot.TransactionHash == ([32]byte{}) {
		// Empty ledgers may legitimately have no transaction pack. A non-empty
		// transaction root or a non-empty manifest must still propagate the
		// archive error to the caller.
		return nil, nil
	}
	return transactions, err
}

func genesisFeeSchedule(ctx context.Context, client replayHistoryClient, snapshot *statecompare.LedgerSnapshot) (drops.Fees, error) {
	streamer, ok := client.(replayGenesisStateClient)
	if !ok {
		return drops.Fees{}, errors.New("verified FeeSettings history reached genesis without an available genesis state pack")
	}
	stateMap := shamap.New(shamap.TypeState)
	if err := streamer.StreamStateEntries(ctx, snapshot, func(entry statecompare.StateEntry) error {
		return stateMap.Put(entry.Index, entry.Data)
	}); err != nil {
		return drops.Fees{}, fmt.Errorf("loading genesis FeeSettings state: %w", err)
	}
	if err := verifyStateRoot(stateMap, snapshot.AccountHash, snapshot.LedgerIndex); err != nil {
		return drops.Fees{}, fmt.Errorf("verifying genesis FeeSettings state: %w", err)
	}
	rules, err := loadRulesFromState(stateMap)
	if err != nil {
		return drops.Fees{}, fmt.Errorf("loading genesis amendment rules: %w", err)
	}
	settings, err := readReplayFeeSettings(ctx, stateMap, rules)
	if err != nil {
		return drops.Fees{}, fmt.Errorf("loading genesis FeeSettings: %w", err)
	}
	if settings == nil || !feeScheduleComplete(settings) {
		return drops.Fees{}, errors.New("genesis FeeSettings state is absent or incomplete")
	}
	return settings.Fees(), nil
}

func feeScheduleFromReferencedLedger(
	ctx context.Context,
	client replayHistoryClient,
	snapshot *statecompare.LedgerSnapshot,
	wantHash [32]byte,
) (drops.Fees, error) {
	transactions, err := client.Transactions(ctx, snapshot)
	if err != nil {
		return drops.Fees{}, fmt.Errorf("loading referenced SetFee transaction: %w", err)
	}
	for _, record := range transactions {
		if record.TxHash != wantHash {
			continue
		}
		fees, isSetFee, err := parseSuccessfulSetFee(record)
		if err != nil {
			return drops.Fees{}, err
		}
		if !isSetFee {
			return drops.Fees{}, fmt.Errorf("referenced transaction %x is not a SetFee", wantHash)
		}
		return fees, nil
	}
	return drops.Fees{}, fmt.Errorf("ledger %d does not contain referenced SetFee transaction %x", snapshot.LedgerIndex, wantHash)
}

func parseSuccessfulSetFee(record statecompare.Transaction) (drops.Fees, bool, error) {
	fields, err := binarycodec.DecodeBytesAllowOverCap(record.TxBlob)
	if err != nil {
		return drops.Fees{}, false, err
	}
	txType, ok := fields["TransactionType"].(string)
	if !ok {
		return drops.Fees{}, false, errors.New("transaction is missing TransactionType")
	}
	if txType != protocol.TxTypeFee.String() {
		return drops.Fees{}, false, nil
	}

	all.RegisterAll()
	parsed, err := txcore.ParseFromBinary(record.TxBlob)
	if err != nil {
		return drops.Fees{}, true, fmt.Errorf("parsing SetFee transaction: %w", err)
	}
	metadata, err := binarycodec.DecodeBytes(record.MetaBlob)
	if err != nil {
		return drops.Fees{}, true, fmt.Errorf("decoding transaction metadata: %w", err)
	}
	result, ok := metadata["TransactionResult"].(string)
	if !ok {
		return drops.Fees{}, true, errors.New("transaction metadata is missing TransactionResult")
	}
	if result != ter.TesSUCCESS.String() {
		return drops.Fees{}, false, nil
	}
	setFee, ok := parsed.(*pseudo.SetFee)
	if !ok {
		return drops.Fees{}, true, errors.New("SetFee transaction has an unexpected parsed type")
	}
	fees, err := setFee.FeeSchedule()
	if err != nil {
		return drops.Fees{}, true, err
	}
	return fees, true, nil
}
