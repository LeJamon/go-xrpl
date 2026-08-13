package engine

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/applystate"
	"github.com/LeJamon/go-xrpl/internal/tx/clawback"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

const clawbackInvariantHolder = "rN7n7otQDd6FczFgLdSqtcsAUxDkw6fzRH"

type clawbackInvariantTx struct {
	*clawback.Clawback
	apply func(*txcore.ApplyContext) ter.Result
}

func (c clawbackInvariantTx) Apply(ctx *txcore.ApplyContext) ter.Result {
	return c.apply(ctx)
}

func newClawbackInvariantEngine(view applystate.AtomicLedgerView) *Engine {
	rules := amendment.NewRulesBuilder().
		FromPreset(amendment.PresetAllSupported).
		Enable(amendment.FeatureMPTokensV1).
		Enable(amendment.FeatureMPTokensV2).
		Build()
	return NewEngine(view, txcore.EngineConfig{
		BaseFee:                   10,
		LedgerSequence:            100,
		Rules:                     rules,
		SkipSignatureVerification: true,
		OpenLedger:                false,
	})
}

func prepareClawbackInvariantTx(c *clawback.Clawback) {
	c.Fee = "10"
	sequence := uint32(1)
	c.Sequence = &sequence
}

func fundClawbackInvariantHolder(t *testing.T, view *recordingBaseView) {
	t.Helper()
	holder, err := state.DecodeAccountID(clawbackInvariantHolder)
	if err != nil {
		t.Fatalf("decode holder: %v", err)
	}
	if err := view.Insert(keylet.Account(holder), mustAccount(t, clawbackInvariantHolder, 1_000_000, 1)); err != nil {
		t.Fatalf("insert holder: %v", err)
	}
}

func serializeClawbackInvariantLine(t *testing.T, holderBalance int64) (keylet.Keylet, []byte) {
	t.Helper()
	holder, err := state.DecodeAccountID(clawbackInvariantHolder)
	if err != nil {
		t.Fatalf("decode holder: %v", err)
	}
	issuer, err := state.DecodeAccountID(recoveryTestAccount)
	if err != nil {
		t.Fatalf("decode issuer: %v", err)
	}

	balance := state.NewIssuedAmountFromValue(holderBalance, 0, "USD", state.AccountOneAddress)
	low, high := clawbackInvariantHolder, recoveryTestAccount
	if state.CompareAccountIDs(holder, issuer) > 0 {
		balance = balance.Negate()
		low, high = high, low
	}
	data, err := state.SerializeRippleState(&state.RippleState{
		Balance:   balance,
		LowLimit:  state.NewIssuedAmountFromValue(0, 0, "USD", low),
		HighLimit: state.NewIssuedAmountFromValue(0, 0, "USD", high),
	})
	if err != nil {
		t.Fatalf("serialize trust line: %v", err)
	}
	return keylet.Line(holder, issuer, "USD"), data
}

func requireClawbackRecovery(t *testing.T, view *recordingBaseView, accountKey keylet.Keylet, result txcore.ApplyResult) {
	t.Helper()
	if result.Result != ter.TecINVARIANT_FAILED || !result.Applied {
		t.Fatalf("result/applied = %s/%v, want tecINVARIANT_FAILED/true", result.Result, result.Applied)
	}
	if result.Fee != 10 || view.destroyed != 10 {
		t.Fatalf("fee/destroyed = %d/%d, want 10/10", result.Fee, view.destroyed)
	}
	account := readRecoveryAccount(t, view, accountKey)
	if account.Balance != 999_990 || account.Sequence != 2 {
		t.Fatalf("payer balance/sequence = %d/%d, want 999990/2", account.Balance, account.Sequence)
	}
	if result.Metadata == nil {
		t.Fatal("fee-claiming invariant failure is missing metadata")
	}
	for _, node := range result.Metadata.AffectedNodes {
		if node.LedgerEntryType == "RippleState" || node.LedgerEntryType == "MPToken" || node.LedgerEntryType == "MPTokenIssuance" {
			t.Fatalf("recovered metadata contains rolled-back %s change", node.LedgerEntryType)
		}
	}
}

func TestApplyClawbackInvariantRecovery(t *testing.T) {
	t.Run("valid full IOU deletion", func(t *testing.T) {
		view := newRecordingBaseView()
		fundRecoveryAccount(t, view, 1_000_000, 1)
		fundClawbackInvariantHolder(t, view)
		lineKey, before := serializeClawbackInvariantLine(t, 100)
		if err := view.Insert(lineKey, before); err != nil {
			t.Fatalf("insert trust line: %v", err)
		}

		tx := clawback.NewClawback(recoveryTestAccount,
			state.NewIssuedAmountFromValue(100, 0, "USD", clawbackInvariantHolder))
		prepareClawbackInvariantTx(tx)
		result := newClawbackInvariantEngine(view).Apply(clawbackInvariantTx{
			Clawback: tx,
			apply: func(ctx *txcore.ApplyContext) ter.Result {
				if err := ctx.View.Erase(lineKey); err != nil {
					return ter.TefINTERNAL
				}
				return ter.TesSUCCESS
			},
		})
		if result.Result != ter.TesSUCCESS || !result.Applied {
			t.Fatalf("result/applied = %s/%v, want tesSUCCESS/true", result.Result, result.Applied)
		}
		line, err := view.Read(lineKey)
		if err != nil || line != nil {
			t.Fatalf("deleted trust line = %x, err=%v", line, err)
		}
	})

	t.Run("invalid IOU delta", func(t *testing.T) {
		view := newRecordingBaseView()
		accountKey := fundRecoveryAccount(t, view, 1_000_000, 1)
		fundClawbackInvariantHolder(t, view)
		lineKey, before := serializeClawbackInvariantLine(t, 100)
		_, invalidAfter := serializeClawbackInvariantLine(t, 80)
		if err := view.Insert(lineKey, before); err != nil {
			t.Fatalf("insert trust line: %v", err)
		}

		tx := clawback.NewClawback(recoveryTestAccount,
			state.NewIssuedAmountFromValue(10, 0, "USD", clawbackInvariantHolder))
		prepareClawbackInvariantTx(tx)
		result := newClawbackInvariantEngine(view).Apply(clawbackInvariantTx{
			Clawback: tx,
			apply: func(ctx *txcore.ApplyContext) ter.Result {
				if err := ctx.View.Update(lineKey, invalidAfter); err != nil {
					return ter.TefINTERNAL
				}
				return ter.TesSUCCESS
			},
		})
		requireClawbackRecovery(t, view, accountKey, result)
		after, err := view.Read(lineKey)
		if err != nil || !bytes.Equal(after, before) {
			t.Fatalf("trust line was not rolled back: err=%v", err)
		}
	})

	t.Run("invalid MPT delta", func(t *testing.T) {
		view := newRecordingBaseView()
		accountKey := fundRecoveryAccount(t, view, 1_000_000, 1)
		issuer, err := state.DecodeAccountID(recoveryTestAccount)
		if err != nil {
			t.Fatalf("decode issuer: %v", err)
		}
		holder, err := state.DecodeAccountID(clawbackInvariantHolder)
		if err != nil {
			t.Fatalf("decode holder: %v", err)
		}
		issuanceID := keylet.MakeMPTID(1, issuer)
		issuanceKey := keylet.MPTIssuance(issuanceID)
		tokenKey := keylet.MPToken(issuanceKey.Key, holder)
		beforeIssuance, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{
			Issuer: issuer, Sequence: 1, OutstandingAmount: 100,
		})
		if err != nil {
			t.Fatalf("serialize issuance: %v", err)
		}
		invalidIssuance, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{
			Issuer: issuer, Sequence: 1, OutstandingAmount: 80,
		})
		if err != nil {
			t.Fatalf("serialize invalid issuance: %v", err)
		}
		beforeToken, err := state.SerializeMPToken(&state.MPTokenData{
			Account: holder, MPTokenIssuanceID: issuanceID, MPTAmount: 100,
		})
		if err != nil {
			t.Fatalf("serialize token: %v", err)
		}
		invalidToken, err := state.SerializeMPToken(&state.MPTokenData{
			Account: holder, MPTokenIssuanceID: issuanceID, MPTAmount: 80,
		})
		if err != nil {
			t.Fatalf("serialize invalid token: %v", err)
		}
		if err := view.Insert(issuanceKey, beforeIssuance); err != nil {
			t.Fatalf("insert issuance: %v", err)
		}
		if err := view.Insert(tokenKey, beforeToken); err != nil {
			t.Fatalf("insert token: %v", err)
		}

		tx := clawback.NewMPTokenClawback(recoveryTestAccount, clawbackInvariantHolder,
			state.NewMPTAmountWithIssuanceID(10, recoveryTestAccount, hex.EncodeToString(issuanceID[:])))
		prepareClawbackInvariantTx(tx)
		result := newClawbackInvariantEngine(view).Apply(clawbackInvariantTx{
			Clawback: tx,
			apply: func(ctx *txcore.ApplyContext) ter.Result {
				if err := ctx.View.Update(tokenKey, invalidToken); err != nil {
					return ter.TefINTERNAL
				}
				if err := ctx.View.Update(issuanceKey, invalidIssuance); err != nil {
					return ter.TefINTERNAL
				}
				return ter.TesSUCCESS
			},
		})
		requireClawbackRecovery(t, view, accountKey, result)
		afterToken, err := view.Read(tokenKey)
		if err != nil || !bytes.Equal(afterToken, beforeToken) {
			t.Fatalf("MPToken was not rolled back: err=%v", err)
		}
		afterIssuance, err := view.Read(issuanceKey)
		if err != nil || !bytes.Equal(afterIssuance, beforeIssuance) {
			t.Fatalf("MPTokenIssuance was not rolled back: err=%v", err)
		}
	})
}
