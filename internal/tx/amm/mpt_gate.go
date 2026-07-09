package amm

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// requireMPTokensV2 rejects an AMM transaction that references an MPT asset or
// amount unless the MPTokensV2 amendment is enabled, mirroring the
// checkExtraFeatures gate every AMM transactor carries in rippled 3.2.0. It runs
// at the checkExtraFeatures position (before the common preflight), so an MPT
// field without the amendment surfaces temDISABLED ahead of any other tem code.
func requireMPTokensV2(rules *amendment.Rules, mptPresent bool) error {
	if mptPresent && !rules.MPTokensV2Enabled() {
		return ter.Errorf(ter.TemDISABLED, "MPT assets require MPTokensV2 amendment")
	}
	return nil
}

// amountIsMPT reports whether an optional amount field is an MPT.
func amountIsMPT(a *tx.Amount) bool {
	return a != nil && a.IsMPT()
}
