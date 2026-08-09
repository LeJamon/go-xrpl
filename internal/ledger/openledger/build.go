package openledger

import (
	"errors"
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/protocol"
)

type BuildConfig struct {
	CloseTime     time.Time
	CloseFlags    uint8
	CanonicalSalt *[32]byte
	Apply         ApplyConfig
}

type BuildResult struct {
	Ledger  *ledger.Ledger
	Retries []PendingTx
}

func BuildClosedLedger(parent *ledger.Ledger, transactions []PendingTx, cfg BuildConfig) (BuildResult, error) {
	if parent == nil {
		return BuildResult{}, errors.New("openledger.BuildClosedLedger: parent is nil")
	}
	if parent.IsOpen() {
		return BuildResult{}, errors.New("openledger.BuildClosedLedger: parent must be closed")
	}

	pending := append([]PendingTx(nil), transactions...)
	salt := cfg.CanonicalSalt
	if salt == nil {
		computed, err := ComputeSalt(pending)
		if err != nil {
			return BuildResult{}, err
		}
		salt = &computed
	}
	CanonicalSort(pending, *salt)

	child, err := ledger.NewOpenForBuild(parent, cfg.CloseTime)
	if err != nil {
		return BuildResult{}, fmt.Errorf("openledger.BuildClosedLedger: create child: %w", err)
	}
	if protocol.IsFlagLedger(child.Sequence()) {
		if err := child.UpdateNegativeUNL(); err != nil {
			return BuildResult{}, fmt.Errorf("openledger.BuildClosedLedger: update negative UNL: %w", err)
		}
	}

	applyCfg := cfg.Apply
	applyCfg.Mode = BuildLedgerMode
	applyCfg.LedgerSequence = child.Sequence()
	applyCfg.ParentCloseTime = protocol.ToRippleTime(child.ParentCloseTime())
	applyCfg.ApplicationCloseTime = protocol.ToRippleTime(child.CloseTime())
	applyCfg.ApplicationCloseTimeSet = true
	var retries []PendingTx
	if err := ApplyTxs(child, pending, &retries, applyCfg); err != nil {
		return BuildResult{}, fmt.Errorf("openledger.BuildClosedLedger: apply transactions: %w", err)
	}
	if err := child.Close(cfg.CloseTime, cfg.CloseFlags); err != nil {
		return BuildResult{}, fmt.Errorf("openledger.BuildClosedLedger: close ledger: %w", err)
	}
	return BuildResult{Ledger: child, Retries: retries}, nil
}
