package engine

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/amm"
	"github.com/LeJamon/go-xrpl/internal/tx/applystate"
	"github.com/LeJamon/go-xrpl/internal/tx/invariants"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/LeJamon/go-xrpl/protocol"
)

func TestMPTAuthorizationIssuerThroughAdapter(t *testing.T) {
	transaction := amm.NewAMMClawback(recoveryTestAccount, clawbackInvariantHolder,
		txcore.Asset{Currency: "USD", Issuer: recoveryTestAccount}, txcore.Asset{Currency: "XRP"})
	adapted := wrapTxForInvariants(transaction)
	require.True(t, adapted.(invariants.HolderFieldProvider).HasHolder())
	issuer, holder := [20]byte{1}, [20]byte{2}
	id := keylet.MakeMPTID(1, issuer)
	data, err := state.SerializeMPToken(&state.MPTokenData{Account: holder, MPTokenIssuanceID: id})
	require.NoError(t, err)
	changes := []invariants.InvariantEntry{{EntryType: entry.TypeMPToken, After: data}}
	for _, mptV2 := range []bool{false, true} {
		builder := amendment.NewRulesBuilder().Enable(amendment.FeatureLendingProtocol).
			Enable(amendment.FeatureFixCleanup3_4_0)
		if mptV2 {
			builder.Enable(amendment.FeatureMPTokensV2)
		} else {
			builder.Disable(amendment.FeatureMPTokensV2)
		}
		v := invariants.CheckInvariants(adapted, invariants.TesSUCCESS, 0, 0, changes, newMockBaseView(), builder.Build())
		if mptV2 {
			require.Nil(t, v)
		} else {
			require.NotNil(t, v)
			require.Equal(t, "ValidMPTIssuance", v.Name)
			require.Contains(t, v.Message, "submitted by issuer")
		}
	}
	parsed := txcore.NewBaseTx(protocol.TxTypeAMMClawback, recoveryTestAccount)
	parsed.SetPresentFields(map[string]bool{"Holder": true})
	require.True(t, wrapTxForInvariants(parsed).(invariants.HolderFieldProvider).HasHolder())
}

type mptAuthorizationMutationTx struct {
	*txcore.BaseTx
	mutate func(txcore.LedgerView)
}

func (tx mptAuthorizationMutationTx) Apply(ctx *txcore.ApplyContext) ter.Result {
	tx.mutate(ctx.View)
	return ter.TesSUCCESS
}

func TestMPTAuthorizationInvariantRecovery(t *testing.T) {
	for _, tc := range []struct {
		txType  protocol.TxType
		count   int
		deleted bool
	}{
		{protocol.TxTypeLoanSet, 3, false},
		{protocol.TxTypeVaultWithdraw, 2, false},
		{protocol.TxTypeLoanSet, 1, true},
		{protocol.TxTypeVaultWithdraw, 2, true},
	} {
		for _, persistent := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/deleted=%t/persistent=%t", tc.txType, tc.deleted, persistent), func(t *testing.T) {
				view := newRecordingBaseView()
				accountKey := fundRecoveryAccount(t, view, 1_000_000, 1)
				issuer, holder := [20]byte{1}, [20]byte{2}
				keys := make([]keylet.Keylet, tc.count)
				data := make([][]byte, tc.count)
				for i := range keys {
					id := keylet.MakeMPTID(uint32(i+1), issuer)
					keys[i] = keylet.MPTokenByID(id, holder)
					var err error
					data[i], err = state.SerializeMPToken(&state.MPTokenData{Account: holder, MPTokenIssuanceID: id})
					require.NoError(t, err)
					if tc.deleted {
						require.NoError(t, view.Insert(keys[i], data[i]))
					}
				}
				mutate := func(table txcore.LedgerView) {
					for i, key := range keys {
						if tc.deleted {
							require.NoError(t, table.Erase(key))
						} else {
							require.NoError(t, table.Insert(key, data[i]))
						}
					}
				}
				rules := amendment.NewRulesBuilder().
					Enable(amendment.FeatureLendingProtocol).
					Enable(amendment.FeatureFixCleanup3_4_0).Build()
				engine := NewEngine(view, txcore.EngineConfig{
					BaseFee: 10, LedgerSequence: 100, Rules: rules,
					SkipSignatureVerification: true,
				})
				base := txcore.NewBaseTx(tc.txType, recoveryTestAccount)
				base.Fee = "10"
				sequence := uint32(1)
				base.Sequence = &sequence
				transaction := mptAuthorizationMutationTx{BaseTx: base, mutate: mutate}

				probe := applystate.NewApplyStateTable(view, [32]byte{}, 100, rules)
				mutate(probe)
				violation := invariants.CheckInvariants(wrapTxForInvariants(transaction), invariants.TesSUCCESS, 0, 10, probe.CollectEntries(), probe, rules)
				require.NotNil(t, violation)
				require.Equal(t, "ValidMPTIssuance", violation.Name)
				require.Contains(t, violation.Message, "bad number of mptokens")

				retryChecked := false
				engine.SetInvariantViolationHookForTest(func(result ter.Result, table *applystate.ApplyStateTable) *InvariantViolationValue {
					require.Equal(t, ter.TecINVARIANT_FAILED, result)
					retryChecked = true
					if !persistent {
						return nil
					}
					mutate(table)
					v := invariants.CheckInvariants(wrapTxForInvariants(transaction), invariants.Result(result), 10, 10, table.CollectEntries(), table, rules)
					require.NotNil(t, v)
					require.Equal(t, "ValidMPTIssuance", v.Name)
					return v
				})
				result := engine.Apply(transaction)
				require.True(t, retryChecked)
				account := readRecoveryAccount(t, view, accountKey)
				if persistent {
					require.Equal(t, ter.TefINVARIANT_FAILED, result.Result)
					require.False(t, result.Applied)
					require.EqualValues(t, 0, view.destroyed)
					require.EqualValues(t, 1_000_000, account.Balance)
					require.EqualValues(t, 1, account.Sequence)
				} else {
					require.Equal(t, ter.TecINVARIANT_FAILED, result.Result)
					require.True(t, result.Applied)
					require.EqualValues(t, 10, result.Fee)
					require.EqualValues(t, 10, view.destroyed)
					require.EqualValues(t, 999_990, account.Balance)
					require.EqualValues(t, 2, account.Sequence)
					require.NotNil(t, result.Metadata)
					require.Len(t, result.Metadata.AffectedNodes, 1)
					require.Equal(t, "AccountRoot", result.Metadata.AffectedNodes[0].LedgerEntryType)
				}
				require.Zero(t, account.OwnerCount)
				for i, key := range keys {
					after, err := view.Read(key)
					require.NoError(t, err)
					if tc.deleted {
						require.Equal(t, data[i], after)
					} else {
						require.Nil(t, after)
					}
				}
			})
		}
	}
}
