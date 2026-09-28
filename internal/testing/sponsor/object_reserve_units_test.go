package sponsor_test

import (
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	checktx "github.com/LeJamon/go-xrpl/internal/tx/check"
	sponsortx "github.com/LeJamon/go-xrpl/internal/tx/sponsor"
	trustsettx "github.com/LeJamon/go-xrpl/internal/tx/trustset"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func synthesizeLegacySignerList(t *testing.T, env *jtx.TestEnv, owner *jtx.Account, ownerCount uint32) {
	t.Helper()

	key := keylet.SignerList(owner.ID)
	data, err := env.LedgerEntry(key)
	require.NoError(t, err)
	fields, err := binarycodec.DecodeBytes(data)
	require.NoError(t, err)
	flags, ok := fields["Flags"].(uint32)
	require.True(t, ok)
	require.NotZero(t, flags&state.LsfOneOwnerCount)
	fields["Flags"] = flags &^ state.LsfOneOwnerCount
	data, err = binarycodec.EncodeBytes(fields)
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Update(key, data))
	signerListData := data

	account := accountState(t, env, owner)
	account.OwnerCount = ownerCount
	data, err = state.SerializeAccountRoot(account)
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Update(keylet.Account(owner.ID), data))

	legacy, err := state.ParseSignerList(signerListData)
	require.NoError(t, err)
	require.Zero(t, legacy.Flags&state.LsfOneOwnerCount)
	require.Equal(t, 3, len(legacy.SignerEntries))
}

func TestSponsorshipObjectReserveUnitsLegacySignerList(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		for _, delta := range []int64{-1, 0, 1} {
			t.Run(fmt.Sprintf("cleanup=%t/delta=%d", cleanup, delta), func(t *testing.T) {
				env, sponsee, _, sponsor, _ := sponsorEnv(t)
				if cleanup {
					env.EnableFeature("fixCleanup3_4_0")
				} else {
					env.DisableFeature("fixCleanup3_4_0")
				}
				env.Close()

				bob := jtx.NewAccount("legacy-bob")
				carol := jtx.NewAccount("legacy-carol")
				dave := jtx.NewAccount("legacy-dave")
				env.Fund(bob, carol, dave)
				env.SetSignerList(sponsee, 1, []jtx.TestSigner{
					{Account: bob, Weight: 1},
					{Account: carol, Weight: 1},
					{Account: dave, Weight: 1},
				})

				const legacyOwnerCount uint32 = 5
				synthesizeLegacySignerList(t, env, sponsee, legacyOwnerCount)
				remaining := int32(legacyOwnerCount)
				require.Equal(t, "tesSUCCESS", setSponsorship(env, sponsor, sponsee, 0, &remaining).Code)
				object := keylet.SignerList(sponsee.ID)
				require.Equal(t, "tesSUCCESS", transferObject(env, sponsee, object, sponsor, sponsortx.SponsorshipTransferFlagCreate).Code)

				gotSponsor, present := objectSponsor(t, env, object)
				require.True(t, present)
				require.Equal(t, sponsor.Address, gotSponsor)
				require.Equal(t, legacyOwnerCount, accountState(t, env, sponsee).OwnerCount)
				require.Equal(t, legacyOwnerCount, accountState(t, env, sponsee).SponsoredOwnerCount)
				require.Equal(t, legacyOwnerCount, accountState(t, env, sponsor).SponsoringOwnerCount)
				require.Zero(t, sponsorshipEntry(t, env, sponsor, sponsee).RemainingOwnerCount)

				required := env.ReserveBase() + uint64(legacyOwnerCount)*env.ReserveIncrement()
				balance := uint64(int64(required) + delta)
				setAccountBalance(t, env, sponsee, balance)
				objectBefore, err := env.LedgerEntry(object)
				require.NoError(t, err)
				budgetBefore, err := env.LedgerEntry(keylet.Sponsorship(sponsor.ID, sponsee.ID))
				require.NoError(t, err)
				sponseeBefore := accountState(t, env, sponsee)
				sponsorBefore := accountState(t, env, sponsor)

				end := sponsortx.NewSponsorshipTransfer(sponsee.Address)
				end.Fee = "10"
				end.ObjectID = hex.EncodeToString(object.Key[:])
				end.SetFlags(sponsortx.SponsorshipTransferFlagEnd)
				result := env.Submit(end)
				failed := cleanup && delta < 0
				if failed {
					require.Equal(t, "tecINSUFFICIENT_RESERVE", result.Code)
					requireAffectedNodes(t, result, "ModifiedNode/AccountRoot")
					objectAfter, err := env.LedgerEntry(object)
					require.NoError(t, err)
					require.Equal(t, objectBefore, objectAfter)
				} else {
					require.Equal(t, "tesSUCCESS", result.Code)
					requireAffectedNodes(t, result,
						"ModifiedNode/AccountRoot", "ModifiedNode/AccountRoot", "ModifiedNode/SignerList")
				}
				gotSponsor, present = objectSponsor(t, env, object)
				if failed {
					require.True(t, present)
					require.Equal(t, sponsor.Address, gotSponsor)
				} else {
					require.False(t, present)
				}

				sponseeAfter := accountState(t, env, sponsee)
				sponsorAfter := accountState(t, env, sponsor)
				require.Equal(t, sponseeBefore.OwnerCount, sponseeAfter.OwnerCount)
				require.Equal(t, sponsorBefore.OwnerCount, sponsorAfter.OwnerCount)
				wantCount := uint32(0)
				if failed {
					wantCount = legacyOwnerCount
				}
				require.Equal(t, wantCount, sponseeAfter.SponsoredOwnerCount)
				require.Equal(t, wantCount, sponsorAfter.SponsoringOwnerCount)
				require.Equal(t, balance-10, sponseeAfter.Balance)
				require.Equal(t, sponsorBefore.Balance, sponsorAfter.Balance)
				require.Equal(t, sponseeBefore.Sequence+1, sponseeAfter.Sequence)
				require.Equal(t, sponsorBefore.Sequence, sponsorAfter.Sequence)
				budgetAfter, err := env.LedgerEntry(keylet.Sponsorship(sponsor.ID, sponsee.ID))
				require.NoError(t, err)
				require.Equal(t, budgetBefore, budgetAfter)
			})
		}
	}
}

func TestSponsorshipObjectReserveUnitsTrustLineSides(t *testing.T) {
	for _, high := range []bool{false, true} {
		for _, cleanup := range []bool{false, true} {
			for _, delta := range []int64{-1, 0, 1} {
				t.Run(fmt.Sprintf("high=%t/cleanup=%t/delta=%d", high, cleanup, delta), func(t *testing.T) {
					env, first, second, sponsor, sponsor2 := sponsorEnv(t)
					if cleanup {
						env.EnableFeature("fixCleanup3_4_0")
					} else {
						env.DisableFeature("fixCleanup3_4_0")
					}
					env.Close()

					holder, issuer := first, second
					if (state.CompareAccountIDs(holder.ID, issuer.ID) > 0) != high {
						holder, issuer = issuer, holder
					}
					lineKey := keylet.Line(holder.ID, issuer.ID, "USD")
					limit := tx.NewIssuedAmountFromFloat64(100, "USD", issuer.Address)
					require.Equal(t, "tesSUCCESS", env.Submit(trustsettx.NewTrustSet(holder.Address, limit)).Code)
					reciprocalLimit := tx.NewIssuedAmountFromFloat64(100, "USD", holder.Address)
					require.Equal(t, "tesSUCCESS", env.Submit(trustsettx.NewTrustSet(issuer.Address, reciprocalLimit)).Code)

					remaining := int32(1)
					require.Equal(t, "tesSUCCESS", setSponsorship(env, sponsor, holder, 0, &remaining).Code)
					remaining2 := int32(1)
					require.Equal(t, "tesSUCCESS", setSponsorship(env, sponsor2, issuer, 0, &remaining2).Code)
					require.Equal(t, "tesSUCCESS", transferObject(env, holder, lineKey, sponsor, sponsortx.SponsorshipTransferFlagCreate).Code)
					require.Equal(t, "tesSUCCESS", transferObject(env, issuer, lineKey, sponsor2, sponsortx.SponsorshipTransferFlagCreate).Code)

					lineData, err := env.LedgerEntry(lineKey)
					require.NoError(t, err)
					line, err := state.ParseRippleState(lineData)
					require.NoError(t, err)
					if high {
						require.Equal(t, sponsor.Address, line.HighSponsor)
						require.Equal(t, sponsor2.Address, line.LowSponsor)
					} else {
						require.Equal(t, sponsor.Address, line.LowSponsor)
						require.Equal(t, sponsor2.Address, line.HighSponsor)
					}
					require.Equal(t, uint32(1), accountState(t, env, holder).SponsoredOwnerCount)
					require.Equal(t, uint32(1), accountState(t, env, sponsor).SponsoringOwnerCount)
					require.Equal(t, uint32(1), accountState(t, env, issuer).SponsoredOwnerCount)
					require.Equal(t, uint32(1), accountState(t, env, sponsor2).SponsoringOwnerCount)

					required := env.ReserveBase() + env.ReserveIncrement()
					balance := uint64(int64(required) + delta)
					setAccountBalance(t, env, holder, balance)
					lineBefore, err := env.LedgerEntry(lineKey)
					require.NoError(t, err)
					budgetBefore, err := env.LedgerEntry(keylet.Sponsorship(sponsor.ID, holder.ID))
					require.NoError(t, err)
					holderBefore := accountState(t, env, holder)
					sponsorBefore := accountState(t, env, sponsor)
					issuerBefore := accountState(t, env, issuer)
					sponsor2Before := accountState(t, env, sponsor2)
					issuerBeforeBytes, err := env.LedgerEntry(keylet.Account(issuer.ID))
					require.NoError(t, err)
					sponsor2BeforeBytes, err := env.LedgerEntry(keylet.Account(sponsor2.ID))
					require.NoError(t, err)
					budget2Before, err := env.LedgerEntry(keylet.Sponsorship(sponsor2.ID, issuer.ID))
					require.NoError(t, err)

					end := sponsortx.NewSponsorshipTransfer(holder.Address)
					end.Fee = "10"
					end.ObjectID = hex.EncodeToString(lineKey.Key[:])
					end.SetFlags(sponsortx.SponsorshipTransferFlagEnd)
					result := env.Submit(end)
					failed := cleanup && delta < 0
					if failed {
						require.Equal(t, "tecINSUFFICIENT_RESERVE", result.Code)
						requireAffectedNodes(t, result, "ModifiedNode/AccountRoot")
						after, err := env.LedgerEntry(lineKey)
						require.NoError(t, err)
						require.Equal(t, lineBefore, after)
					} else {
						require.Equal(t, "tesSUCCESS", result.Code)
						requireAffectedNodes(t, result,
							"ModifiedNode/AccountRoot", "ModifiedNode/AccountRoot", "ModifiedNode/RippleState")
					}

					lineData, err = env.LedgerEntry(lineKey)
					require.NoError(t, err)
					line, err = state.ParseRippleState(lineData)
					require.NoError(t, err)
					if high {
						if failed {
							require.Equal(t, sponsor.Address, line.HighSponsor)
						} else {
							require.Empty(t, line.HighSponsor)
						}
						require.Equal(t, sponsor2.Address, line.LowSponsor)
					} else {
						if failed {
							require.Equal(t, sponsor.Address, line.LowSponsor)
						} else {
							require.Empty(t, line.LowSponsor)
						}
						require.Equal(t, sponsor2.Address, line.HighSponsor)
					}

					holderAfter := accountState(t, env, holder)
					sponsorAfter := accountState(t, env, sponsor)
					issuerAfter := accountState(t, env, issuer)
					require.Equal(t, holderBefore.OwnerCount, holderAfter.OwnerCount)
					require.Equal(t, sponsorBefore.OwnerCount, sponsorAfter.OwnerCount)
					require.Equal(t, issuerBefore.OwnerCount, issuerAfter.OwnerCount)
					wantCount := uint32(0)
					if failed {
						wantCount = 1
					}
					require.Equal(t, wantCount, holderAfter.SponsoredOwnerCount)
					require.Equal(t, wantCount, sponsorAfter.SponsoringOwnerCount)
					require.Equal(t, balance-10, holderAfter.Balance)
					require.Equal(t, sponsorBefore.Balance, sponsorAfter.Balance)
					require.Equal(t, issuerBefore.SponsoredOwnerCount, issuerAfter.SponsoredOwnerCount)
					require.Equal(t, issuerBefore.SponsoringOwnerCount, issuerAfter.SponsoringOwnerCount)
					sponsor2After := accountState(t, env, sponsor2)
					require.Equal(t, sponsor2Before.OwnerCount, sponsor2After.OwnerCount)
					require.Equal(t, sponsor2Before.SponsoringOwnerCount, sponsor2After.SponsoringOwnerCount)
					require.Equal(t, sponsor2Before.Balance, sponsor2After.Balance)
					require.Equal(t, issuerBefore.Balance, issuerAfter.Balance)
					require.Equal(t, holderBefore.Sequence+1, holderAfter.Sequence)
					require.Equal(t, sponsorBefore.Sequence, sponsorAfter.Sequence)
					require.Equal(t, issuerBefore.Sequence, issuerAfter.Sequence)
					require.Equal(t, sponsor2Before.Sequence, sponsor2After.Sequence)
					issuerAfterBytes, err := env.LedgerEntry(keylet.Account(issuer.ID))
					require.NoError(t, err)
					require.Equal(t, issuerBeforeBytes, issuerAfterBytes)
					sponsor2AfterBytes, err := env.LedgerEntry(keylet.Account(sponsor2.ID))
					require.NoError(t, err)
					require.Equal(t, sponsor2BeforeBytes, sponsor2AfterBytes)
					budgetAfter, err := env.LedgerEntry(keylet.Sponsorship(sponsor.ID, holder.ID))
					require.NoError(t, err)
					require.Equal(t, budgetBefore, budgetAfter)
					budget2After, err := env.LedgerEntry(keylet.Sponsorship(sponsor2.ID, issuer.ID))
					require.NoError(t, err)
					require.Equal(t, budget2Before, budget2After)
				})
			}
		}
	}
}

func TestSponsorshipObjectReserveUnitsAmendmentGate(t *testing.T) {
	for _, object := range []bool{false, true} {
		for _, cleanup := range []bool{false, true} {
			t.Run(fmt.Sprintf("object=%t/cleanup=%t", object, cleanup), func(t *testing.T) {
				env, sponsee, destination, sponsor, _ := sponsorEnv(t)
				var objectKey keylet.Keylet
				if object {
					sequence := env.Seq(sponsee)
					require.Equal(t, "tesSUCCESS", env.Submit(checktx.NewCheckCreate(
						sponsee.Address, destination.Address, tx.NewXRPAmount(1))).Code)
					objectKey = keylet.Check(sponsee.ID, sequence)
				}

				env.DisableFeature("Sponsor")
				if cleanup {
					env.EnableFeature("fixCleanup3_4_0")
				} else {
					env.DisableFeature("fixCleanup3_4_0")
				}
				env.Close()

				objectBefore := []byte(nil)
				var err error
				if object {
					objectBefore, err = env.LedgerEntry(objectKey)
					require.NoError(t, err)
				}
				before, err := env.LedgerEntry(keylet.Account(sponsee.ID))
				require.NoError(t, err)
				transfer := sponsortx.NewSponsorshipTransfer(sponsee.Address)
				transfer.Fee = "10"
				transfer.SetFlags(sponsortx.SponsorshipTransferFlagCreate)
				transfer.Sponsor = sponsor.Address
				reserve := tx.SpfSponsorReserve
				transfer.SponsorFlags = &reserve
				if object {
					transfer.ObjectID = hex.EncodeToString(objectKey.Key[:])
				} else {
					transfer.SponsorSignature = &tx.SponsorSignature{}
				}

				result := env.Submit(transfer)
				require.Equal(t, "temDISABLED", result.Code)
				after, err := env.LedgerEntry(keylet.Account(sponsee.ID))
				require.NoError(t, err)
				require.Equal(t, before, after)
				if object {
					objectAfter, err := env.LedgerEntry(objectKey)
					require.NoError(t, err)
					require.Equal(t, objectBefore, objectAfter)
				}
			})
		}
	}
}
