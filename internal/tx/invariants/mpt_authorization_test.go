package invariants

import (
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/protocol"
)

func TestValidMPTIssuance_AuthorizationCaps(t *testing.T) {
	type counts struct{ created, deleted int }
	oneChange := []counts{{0, 0}, {1, 0}, {0, 1}}
	created := mptInvariantEntry(t, addrHolderA, addrIssuer, false)
	deleted := mptInvariantEntry(t, addrHolderB, addrIssuer, true)
	for flags := 0; flags < 16; flags++ {
		lending, cleanup := flags&1 != 0, flags&2 != 0
		v11, mptV2 := flags&4 != 0, flags&8 != 0
		name := fmt.Sprintf("lending=%t/cleanup=%t/v11=%t/mptV2=%t", lending, cleanup, v11, mptV2)
		t.Run(name, func(t *testing.T) {
			builder := amendment.NewRulesBuilder().Enable(amendment.FeatureSingleAssetVault)
			features := map[[32]byte]bool{
				amendment.FeatureLendingProtocol:     lending,
				amendment.FeatureFixCleanup3_4_0:     cleanup,
				amendment.FeatureLendingProtocolV1_1: v11,
				amendment.FeatureMPTokensV2:          mptV2,
			}
			for feature, enabled := range features {
				if enabled {
					builder.Enable(feature)
				} else {
					builder.Disable(feature)
				}
			}
			rules := builder.Build()
			for feature, enabled := range features {
				require.Equal(t, enabled, rules.Enabled(feature))
			}
			for _, tc := range []struct {
				txType  TxType
				allowed []counts
			}{
				{protocol.TxTypeLoanSet, []counts{{0, 0}, {1, 0}, {2, 0}}},
				{protocol.TxTypeVaultWithdraw, []counts{{0, 0}, {1, 0}, {0, 1}, {1, 1}}},
				{protocol.TxTypeLoanBrokerSet, oneChange},
				{protocol.TxTypeLoanBrokerDelete, oneChange},
				{protocol.TxTypeLoanBrokerCoverWithdraw, oneChange},
				{protocol.TxTypeLoanPay, oneChange},
				{protocol.TxTypeVaultDeposit, oneChange},
				{protocol.TxTypeEscrowFinish, oneChange},
				{protocol.TxTypeAMMWithdraw, oneChange},
				{protocol.TxTypeAMMClawback, oneChange},
				{protocol.TxTypeMPTokenAuthorize, []counts{{1, 0}, {0, 1}}},
			} {
				t.Run(tc.txType.String(), func(t *testing.T) {
					for c := 0; c <= 3; c++ {
						for d := 0; d <= 3; d++ {
							entries := append(slices.Repeat([]InvariantEntry{created}, c), slices.Repeat([]InvariantEntry{deleted}, d)...)
							for _, issuer := range []bool{false, true} {
								transaction := holderTx{stubTx: stubTx{txType: tc.txType}, hasHolder: issuer}
								allowed := !lending || slices.Contains(oneChange, counts{c, d})
								if lending && cleanup {
									allowed = slices.Contains(tc.allowed, counts{c, d})
								}
								if issuer {
									allowed = c == 0 && d == 0
								} else if tc.txType == protocol.TxTypeMPTokenAuthorize {
									allowed = slices.Contains(tc.allowed, counts{c, d})
								}
								if mptV2 && (tc.txType == protocol.TxTypeAMMWithdraw || tc.txType == protocol.TxTypeAMMClawback) {
									allowed = c <= 2 && d <= 2 && !(issuer && tc.txType == protocol.TxTypeAMMWithdraw && c > 0)
								}
								for _, result := range []Result{TesSUCCESS, TecINCOMPLETE, Result(ter.TecINVARIANT_FAILED)} {
									wantPass := allowed
									if result != TesSUCCESS && !(result == TecINCOMPLETE && mptV2) {
										wantPass = c == 0 && d == 0
									}
									v := checkValidMPTIssuance(transaction, result, entries, stubView{}, rules)
									require.Equal(t, wantPass, v == nil, "created=%d deleted=%d issuer=%t result=%d: %v", c, d, issuer, result, v)
									if v != nil {
										require.Equal(t, "ValidMPTIssuance", v.Name)
									}
								}
							}
						}
					}
				})
			}
		})
	}
}

func TestValidMPTIssuance_AuthorizationRejectsIssuanceChanges(t *testing.T) {
	rules := amendment.NewRulesBuilder().
		Enable(amendment.FeatureLendingProtocol).
		Enable(amendment.FeatureFixCleanup3_4_0).Build()
	for _, txType := range []TxType{protocol.TxTypeLoanSet, protocol.TxTypeVaultWithdraw} {
		for _, deleted := range []bool{false, true} {
			v := checkValidMPTIssuance(stubTx{txType: txType}, TesSUCCESS,
				[]InvariantEntry{mptIssuanceInvariantEntry(t, nil, deleted)}, stubView{}, rules)
			require.NotNil(t, v, "%s deleted=%t", txType, deleted)
			require.Equal(t, "ValidMPTIssuance", v.Name)
		}
		v := checkValidMPTIssuance(stubTx{txType: txType}, TesSUCCESS,
			[]InvariantEntry{mptInvariantEntry(t, addrIssuer, addrIssuer, false)}, stubView{}, rules)
		require.NotNil(t, v, "%s issuer self-holding", txType)
		require.Equal(t, "MPToken created for the MPT issuer", v.Message)
	}
}

func TestValidMPTIssuance_AMMCreateHoldingCap(t *testing.T) {
	created := mptInvariantEntry(t, addrHolderA, addrIssuer, false)
	for count := 0; count <= 3; count++ {
		v := checkValidMPTIssuance(stubTx{txType: TypeAMMCreate}, TesSUCCESS,
			slices.Repeat([]InvariantEntry{created}, count), stubView{}, amendment.AllSupportedRules())
		if count <= 2 {
			require.Nil(t, v)
		} else {
			require.NotNil(t, v)
			require.Equal(t, "ValidMPTIssuance", v.Name)
		}
	}
}
