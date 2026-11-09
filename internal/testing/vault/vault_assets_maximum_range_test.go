package vault_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	mpttest "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func newAssetsMaximumEnv(t *testing.T, fix350 bool) *jtx.TestEnv {
	t.Helper()
	env := newVaultEnv(t)
	if fix350 {
		env.EnableFeature("fixCleanup3_5_0")
	} else {
		env.DisableFeature("fixCleanup3_5_0")
	}
	env.Close()
	return env
}

func fix350Name(fix350 bool) string {
	if fix350 {
		return "with_fixCleanup3_5_0"
	}
	return "without_fixCleanup3_5_0"
}

func submitVaultCreateWithMaximum(env *jtx.TestEnv, owner *jtx.Account, asset tx.Asset, maximum string) (jtx.TxResult, keylet.Keylet) {
	key := keylet.Vault(owner.AccountID(), env.Seq(owner))
	create := vault.NewVaultCreate(owner.Address, asset)
	create.Common.Fee = createFee
	create.AssetsMaximum = &maximum
	return env.Submit(create), key
}

func vaultIDFromKey(key keylet.Keylet) string {
	return strings.ToUpper(hex.EncodeToString(key.Key[:]))
}

func requireAssetsMaximum(t *testing.T, env *jtx.TestEnv, key keylet.Keylet, want string) {
	t.Helper()
	info, err := vault.ReadVaultLending(env.Ledger(), key)
	require.NoError(t, err)
	require.NotNil(t, info)
	got, err := state.ParseXRPLNumber(info.AssetsMaximum, state.MantissaScaleLarge, state.RoundToNearest)
	require.NoError(t, err)
	expected, err := state.ParseXRPLNumber(want, state.MantissaScaleLarge, state.RoundToNearest)
	require.NoError(t, err)
	require.Zero(t, got.Cmp(expected), "AssetsMaximum = %s, want %s", info.AssetsMaximum, want)
}

// Reference: rippled VaultValidation_test.cpp testAssetsMaximumOutOfRange.
func TestVaultAssetsMaximumOutOfRange(t *testing.T) {
	for _, fix350 := range []bool{false, true} {
		t.Run(fix350Name(fix350), func(t *testing.T) {
			env := newAssetsMaximumEnv(t, fix350)
			issuer := jtx.NewAccount("issuer")
			owner := jtx.NewAccount("owner")
			env.Fund(issuer, owner)

			// Without the fix, rounding an out-of-range AssetsMaximum to the
			// vault asset throws.
			createErr, setErr := ter.TefEXCEPTION.String(), ter.TefEXCEPTION.String()
			if fix350 {
				createErr, setErr = ter.TemMALFORMED.String(), ter.TecPRECISION_LOSS.String()
			}

			token := mpttest.NewMPTTester(t, env, issuer, mpttest.MPTInit{Holders: []*jtx.Account{owner}})
			token.Create(mpttest.CreateOpts{Flags: mpttest.TfMPTCanTransfer})
			token.Authorize(mpttest.AuthorizeOpts{Account: owner})

			cases := []struct {
				name     string
				asset    tx.Asset
				maxValid string
				overMax  string
			}{
				{"XRP", tx.Asset{Currency: "XRP"}, "100000000000000000", "100000000000000001"},
				{"MPT", tx.Asset{MPTIssuanceID: token.IssuanceID()}, "9223372036854775807", "1e19"},
				{"IOU", tx.Asset{Currency: "USD", Issuer: issuer.Address}, "9999999999999999e80", "1e96"},
			}
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					result, key := submitVaultCreateWithMaximum(env, owner, c.asset, c.overMax)
					require.Equal(t, createErr, result.Code)
					env.Close()
					require.False(t, env.LedgerEntryExists(key))

					result, key = submitVaultCreateWithMaximum(env, owner, c.asset, c.maxValid)
					jtx.RequireTxSuccess(t, result)
					env.Close()

					set := vault.NewVaultSet(owner.Address, vaultIDFromKey(key))
					overMax := c.overMax
					set.AssetsMaximum = &overMax
					require.Equal(t, setErr, env.Submit(set).Code)
					env.Close()
					requireAssetsMaximum(t, env, key, c.maxValid)
				})
			}
		})
	}
}

// Reference: rippled VaultScale_test.cpp testAssetsMaximum, the cases whose
// result depends on fixCleanup3_5_0.
func TestVaultAssetsMaximumScale(t *testing.T) {
	for _, fix350 := range []bool{false, true} {
		t.Run(fix350Name(fix350), func(t *testing.T) {
			env := newAssetsMaximumEnv(t, fix350)
			issuer := jtx.NewAccount("issuer")
			owner := jtx.NewAccount("owner")
			env.Fund(issuer, owner)

			outOfRangeErr := ter.TefEXCEPTION.String()
			if fix350 {
				outOfRangeErr = ter.TemMALFORMED.String()
			}
			const (
				maxInt64        = "9223372036854775807"
				initialXRP      = "100000000000000000"
				initialXRPPlus1 = "100000000000000001"
			)

			t.Run("XRP", func(t *testing.T) {
				xrp := tx.Asset{Currency: "XRP"}
				for _, maximum := range []string{maxInt64, initialXRPPlus1} {
					result, key := submitVaultCreateWithMaximum(env, owner, xrp, maximum)
					require.Equal(t, outOfRangeErr, result.Code, "AssetsMaximum %s", maximum)
					env.Close()
					require.False(t, env.LedgerEntryExists(key))
				}
				result, _ := submitVaultCreateWithMaximum(env, owner, xrp, initialXRP)
				jtx.RequireTxSuccess(t, result)
				env.Close()
			})

			t.Run("MPT", func(t *testing.T) {
				token := mpttest.NewMPTTester(t, env, issuer, mpttest.MPTInit{Holders: []*jtx.Account{owner}})
				token.Create(mpttest.CreateOpts{
					Flags: mpttest.TfMPTCanClawback | mpttest.TfMPTCanTransfer | mpttest.TfMPTCanLock,
				})
				token.Authorize(mpttest.AuthorizeOpts{Account: owner})
				token.Pay(issuer, owner, 100_000)
				mpt := tx.Asset{MPTIssuanceID: token.IssuanceID()}
				for _, maximum := range []string{maxInt64, initialXRPPlus1, initialXRP} {
					result, _ := submitVaultCreateWithMaximum(env, owner, mpt, maximum)
					jtx.RequireTxSuccess(t, result)
					env.Close()
				}
			})

			t.Run("IOU", func(t *testing.T) {
				env.Trust(owner, tx.NewIssuedAmountFromFloat64(1_000, "IOU", issuer.Address))
				env.PayIOU(issuer, owner, issuer, "IOU", 200)
				iou := tx.Asset{Currency: "IOU", Issuer: issuer.Address}
				for _, maximum := range []string{maxInt64, initialXRPPlus1, initialXRP} {
					result, _ := submitVaultCreateWithMaximum(env, owner, iou, maximum)
					jtx.RequireTxSuccess(t, result)
					env.Close()
				}

				// An IOU cannot exceed the maximum exponent.
				result, key := submitVaultCreateWithMaximum(env, owner, iou, "1000000000000000e81")
				require.Equal(t, outOfRangeErr, result.Code)
				env.Close()
				require.False(t, env.LedgerEntryExists(key))
			})
		})
	}
}
