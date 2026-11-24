package service

import (
	"context"
	"errors"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// Call EngineConfigForReplay before recording replay intent so a transient
// fee-settings read can be retried.
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
