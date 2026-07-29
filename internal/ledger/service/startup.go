package service

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

type StartupMode uint8

const (
	// StartupNormal preserves the configured default startup behavior.
	StartupNormal StartupMode = iota
	// StartupFresh starts from a newly created ledger without network acquisition.
	StartupFresh
	// StartupLoad loads a ledger from local durable storage.
	StartupLoad
	// StartupLoadFile constructs a ledger from expanded JSON state.
	StartupLoadFile
	// StartupReplay loads a target and replays its close from the stored parent.
	StartupReplay
	// StartupNetwork starts from a temporary ledger and requests a network ledger.
	StartupNetwork
)

type StartupConfig struct {
	Mode   StartupMode
	Ledger string
}

func (c StartupConfig) validate() error {
	switch c.Mode {
	case StartupNormal, StartupFresh, StartupNetwork:
		if c.Ledger != "" {
			return fmt.Errorf("startup mode %d does not accept a ledger identifier", c.Mode)
		}
	case StartupLoad:
	case StartupLoadFile:
		if c.Ledger == "" {
			return errors.New("ledger file path cannot be empty")
		}
	case StartupReplay:
	default:
		return fmt.Errorf("unknown startup mode %d", c.Mode)
	}
	return nil
}

type startupSelection struct {
	ledger       *ledger.Ledger
	replay       *inbound.ReplayDelta
	loaded       bool
	validate     bool
	networkState networkLedgerState
}

func (s *Service) selectStartup(ctx context.Context, initial *ledger.Ledger) (startupSelection, error) {
	if err := s.config.Startup.validate(); err != nil {
		return startupSelection{}, err
	}

	mode := s.config.Startup.Mode
	switch mode {
	case StartupNormal:
		if s.config.FastLoad {
			loaded, err := s.loadLatestLedger(ctx)
			if err == nil && loaded != nil {
				return startupSelection{
					ledger:   loaded,
					loaded:   true,
					validate: true,
					networkState: networkLedgerStateFor(
						!s.config.Standalone,
						networkLedgerFastLoadProvisional,
					),
				}, nil
			}
			if err != nil {
				s.logger.Warn("fast load failed; using initial ledger",
					"network_acquisition", !s.config.Standalone,
					"err", err,
				)
			}
		}
		fallbackState := networkLedgerNeeded
		if s.config.FastLoad {
			fallbackState = networkLedgerFastLoadProvisional
		}
		return startupSelection{
			ledger:   initial,
			validate: s.config.Standalone,
			networkState: networkLedgerStateFor(
				!s.config.Standalone,
				fallbackState,
			),
		}, nil
	case StartupFresh:
		return startupSelection{ledger: initial, validate: s.config.Standalone}, nil
	case StartupNetwork:
		return startupSelection{
			ledger:       initial,
			validate:     s.config.Standalone,
			networkState: networkLedgerStateFor(!s.config.Standalone, networkLedgerNeeded),
		}, nil
	}

	selected, err := s.loadExplicitStartup(ctx)
	if err == nil {
		return selected, nil
	}
	if !s.config.FastLoad {
		return startupSelection{}, err
	}

	s.logger.Warn("explicit startup load failed; using initial ledger",
		"mode", mode,
		"ledger", s.config.Startup.Ledger,
		"network_acquisition", !s.config.Standalone,
		"err", err,
	)
	return startupSelection{
		ledger:   initial,
		validate: s.config.Standalone,
		networkState: networkLedgerStateFor(
			!s.config.Standalone,
			networkLedgerFastLoadProvisional,
		),
	}, nil
}

func (s *Service) loadExplicitStartup(ctx context.Context) (startupSelection, error) {
	switch s.config.Startup.Mode {
	case StartupLoad:
		loaded, err := s.loadStartupLedger(ctx, s.config.Startup.Ledger)
		if err != nil {
			return startupSelection{}, fmt.Errorf("load startup ledger %q: %w", s.config.Startup.Ledger, err)
		}
		return startupSelection{ledger: loaded, loaded: true, validate: true}, nil
	case StartupLoadFile:
		loaded, err := s.loadLedgerFile(ctx, s.config.Startup.Ledger, time.Now())
		if err != nil {
			return startupSelection{}, fmt.Errorf("load startup ledger file %q: %w", s.config.Startup.Ledger, err)
		}
		if err := loaded.SetValidated(); err != nil {
			return startupSelection{}, fmt.Errorf("validate startup ledger file: %w", err)
		}
		if err := s.persistValidatedLedger(ctx, loaded, true); err != nil {
			return startupSelection{}, fmt.Errorf("persist startup ledger file: %w", err)
		}
		return startupSelection{ledger: loaded, loaded: true, validate: true}, nil
	case StartupReplay:
		target, err := s.loadStartupLedger(ctx, s.config.Startup.Ledger)
		if err != nil {
			return startupSelection{}, fmt.Errorf("load replay target %q: %w", s.config.Startup.Ledger, err)
		}
		parent, err := s.loadVerifiedStoredLedgerByHash(ctx, target.ParentHash())
		if err != nil {
			return startupSelection{}, fmt.Errorf("load replay parent %x: %w", target.ParentHash(), err)
		}
		replay, err := inbound.NewStoredLedgerReplay(parent, target, nil)
		if err != nil {
			return startupSelection{}, fmt.Errorf("prepare startup replay: %w", err)
		}
		return startupSelection{ledger: parent, replay: replay, loaded: true, validate: true}, nil
	default:
		return startupSelection{}, fmt.Errorf("startup mode %d is not an explicit load mode", s.config.Startup.Mode)
	}
}

func (s *Service) loadStartupLedger(ctx context.Context, id string) (*ledger.Ledger, error) {
	if id == "" || strings.EqualFold(id, "latest") {
		if s.relationalDB == nil {
			return nil, errors.New("relational database is required to load the latest ledger")
		}
		info, err := s.relationalDB.Ledger().GetNewestLedgerInfo(ctx)
		if err != nil {
			return nil, err
		}
		return s.loadStartupLedgerInfo(ctx, info)
	}

	if len(id) == 64 {
		raw, err := hex.DecodeString(id)
		if err != nil {
			return nil, fmt.Errorf("invalid ledger hash: %w", err)
		}
		var hash [32]byte
		copy(hash[:], raw)
		return s.loadVerifiedStoredLedgerByHash(ctx, hash)
	}

	sequence, err := strconv.ParseUint(id, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("invalid ledger sequence %q: %w", id, err)
	}
	if s.relationalDB == nil {
		return nil, errors.New("relational database is required to load a ledger by sequence")
	}
	info, err := s.relationalDB.Ledger().GetLedgerInfoBySeq(ctx, relationaldb.LedgerIndex(sequence))
	if err != nil {
		return nil, err
	}
	return s.loadStartupLedgerInfo(ctx, info)
}

func (s *Service) loadStartupLedgerInfo(ctx context.Context, info *relationaldb.LedgerInfo) (*ledger.Ledger, error) {
	if info == nil {
		return nil, ErrLedgerNotFound
	}
	loaded, err := s.loadVerifiedStoredLedgerByHash(ctx, [32]byte(info.Hash))
	if err != nil {
		return nil, err
	}
	if !storedHeaderMatchesInfo(loaded.Header(), info) {
		return nil, fmt.Errorf("stored ledger %d does not match relational metadata", info.Sequence)
	}
	return loaded, nil
}

func (s *Service) loadVerifiedStoredLedgerByHash(ctx context.Context, hash [32]byte) (*ledger.Ledger, error) {
	loaded, err := s.loadStoredLedgerByHash(ctx, hash)
	if err != nil {
		return nil, err
	}
	if loaded == nil {
		return nil, ErrLedgerNotFound
	}
	h := loaded.Header()
	if h.AccountHash == ([32]byte{}) {
		return nil, errors.New("stored ledger has an empty state root")
	}
	if err := s.verifyStoredSHAMap(ctx, h.AccountHash, shamap.TypeState); err != nil {
		return nil, fmt.Errorf("verify stored state tree: %w", err)
	}
	if h.TxHash != ([32]byte{}) {
		if err := s.verifyStoredSHAMap(ctx, h.TxHash, shamap.TypeTransaction); err != nil {
			return nil, fmt.Errorf("verify stored transaction tree: %w", err)
		}
	}
	if err := loaded.SetValidated(); err != nil {
		return nil, fmt.Errorf("mark stored ledger validated: %w", err)
	}
	return loaded, nil
}

func (s *Service) installLoadedStartupLocked(loaded, genesisLedger *ledger.Ledger) {
	s.closedLedger = loaded
	s.validatedLedger = loaded
	s.validatedSignTime = loaded.CloseTime()
	s.publishedLedgerSeq = loaded.Sequence()
	s.havePublished = true
	s.ledgerEventMu.Lock()
	s.ledgerEventFrontierSeq = loaded.Sequence()
	s.ledgerEventFrontierHash = loaded.Hash()
	s.ledgerEventHaveFrontier = true
	s.ledgerEventMu.Unlock()
	s.completeMu.Lock()
	s.completedLedgers.add(loaded.Sequence())
	s.completeLedgerHashes[loaded.Sequence()] = loaded.Hash()
	s.completeMu.Unlock()
	if loaded.Sequence() != genesisLedger.Sequence() {
		s.deleteHistoryLocked(genesisLedger.Sequence())
	}
	s.putHistoryLocked(loaded)
	s.collectTransactionResultsLocked(loaded, loaded.Sequence(), loaded.Hash())
	loadedHash := loaded.Hash()
	s.logger.Info("Loaded startup ledger", "sequence", loaded.Sequence(), "hash", fmt.Sprintf("%x", loadedHash[:8]))
}

func (s *Service) stageStartupReplayLocked() error {
	if s.startupReplay == nil {
		return nil
	}
	for _, replayTx := range s.startupReplay.OrderedTxs() {
		if err := s.openLedger.AddTransaction(replayTx.Hash, replayTx.TxBytes); err != nil {
			return fmt.Errorf("stage replay transaction %x: %w", replayTx.Hash[:8], err)
		}
		var insertErr error
		if !s.openLedgerView.Modify(func(view *ledger.Ledger) bool {
			insertErr = view.AddTransaction(replayTx.Hash, replayTx.TxBytes)
			return insertErr == nil
		}) {
			if insertErr == nil {
				insertErr = errors.New("open ledger view rejected replay transaction")
			}
			return fmt.Errorf("stage replay transaction %x in open view: %w", replayTx.Hash[:8], insertErr)
		}
	}
	return nil
}

func (s *Service) applyStartupReplayLocked() (bool, error) {
	if s.startupReplay == nil {
		return false, nil
	}
	parent := s.startupReplay.Parent()
	if parent == nil || s.closedLedger == nil ||
		parent.Sequence() != s.closedLedger.Sequence() ||
		parent.Hash() != s.closedLedger.Hash() {
		s.logger.Warn("startup replay canceled after closed-ledger change")
		s.startupReplay = nil
		return false, nil
	}
	replayed, err := s.startupReplay.Apply(s.EngineConfigForReplay(s.closedLedger))
	if err != nil {
		return true, fmt.Errorf("apply startup replay: %w", err)
	}
	s.openLedger = replayed
	return true, nil
}
