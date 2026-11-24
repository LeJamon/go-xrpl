package service

import (
	"context"
	"errors"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// EngineConfigForReplay returns the shared transaction-engine settings for a
// verified replay parent. The parent is the source of both the amendment rules
// and the historical fee schedule; a transient fee-settings read must reach
// the caller before replay intent is recorded.
func (s *Service) EngineConfigForReplay(ctx context.Context, parent *ledger.Ledger) (tx.EngineConfig, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if parent == nil {
		return tx.EngineConfig{}, errors.New("replay transition parent is required")
	}
	baseFee, reserveBase, reserveIncrement, err := readFeesFromLedgerContext(ctx, parent)
	if err != nil {
		return tx.EngineConfig{}, err
	}
	return tx.EngineConfig{
		BaseFee:                   baseFee,
		ReserveBase:               reserveBase,
		ReserveIncrement:          reserveIncrement,
		NetworkID:                 s.config.NetworkID,
		SkipSignatureVerification: false,
		Logger:                    s.config.Logger,
		Rules:                     parent.Rules(),
	}, nil
}
