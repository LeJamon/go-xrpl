package lending_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/testing/credential"
	"github.com/LeJamon/go-xrpl/internal/testing/depositpreauth"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/engine"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func withdrawalCredentialFields(kind string) map[string]any {
	fields := map[string]any{
		"TransactionType": kind,
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Amount":          "1000000",
		"Fee":             "10",
		"Sequence":        uint32(1),
		"SigningPubKey":   "",
	}
	if kind == "VaultWithdraw" {
		fields["VaultID"] = fxHash
	} else {
		fields["LoanBrokerID"] = fxHash
	}
	return fields
}

func TestWithdrawalCredentialsWireAndPreflight(t *testing.T) {
	maxIDs := make([]string, 8)
	for i := range maxIDs {
		maxIDs[i] = fmt.Sprintf("%064X", i+1)
	}
	for _, kind := range []string{"VaultWithdraw", "LoanBrokerCoverWithdraw"} {
		for _, tc := range []struct {
			name string
			ids  []string
			want ter.Result
		}{
			{"absent", nil, ter.TesSUCCESS},
			{"empty", []string{}, ter.TemMALFORMED},
			{"valid", []string{fxHash}, ter.TesSUCCESS},
			{"maximum", maxIDs, ter.TesSUCCESS},
			{"too many", append(append([]string{}, maxIDs...), strings.Repeat("9", 64)), ter.TemMALFORMED},
			{"duplicate", []string{fxHash, fxHash}, ter.TemMALFORMED},
			{"zero", []string{strings.Repeat("0", 64)}, ter.TemMALFORMED},
			{"malformed", []string{"not a hash"}, ter.TemMALFORMED},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				fields := withdrawalCredentialFields(kind)
				if tc.ids != nil {
					fields["CredentialIDs"] = tc.ids
				}
				data, err := json.Marshal(fields)
				require.NoError(t, err)
				parsed, err := tx.FromJSON(data)
				require.NoError(t, err)
				for _, credentialsEnabled := range []bool{false, true} {
					for _, cleanupEnabled := range []bool{false, true} {
						ids := [][32]byte{amendment.FeatureSingleAssetVault, amendment.FeatureMPTokensV1, amendment.FeatureLendingProtocol}
						if credentialsEnabled {
							ids = append(ids, amendment.FeatureCredentials)
						}
						if cleanupEnabled {
							ids = append(ids, amendment.FeatureFixCleanup3_4_0)
						}
						e := engine.NewEngine(nil, tx.EngineConfig{Rules: amendment.NewRules(ids), SkipSignatureVerification: true})
						want := tc.want
						if tc.ids != nil && (!credentialsEnabled || !cleanupEnabled) {
							want = ter.TemDISABLED
						}
						require.Equal(t, want, e.Preflight(parsed), "Credentials=%v cleanup=%v", credentialsEnabled, cleanupEnabled)
						parsed.GetCommon().Fee = "-1"
						if want != ter.TemDISABLED {
							want = ter.TemBAD_FEE
						}
						require.Equal(t, want, e.Preflight(parsed), "fee precedence")
						parsed.GetCommon().Fee = "10"
					}
				}
				if tc.name == "malformed" {
					return
				}
				blob, err := binarycodec.EncodeBytes(fields)
				require.NoError(t, err)
				parsed, err = tx.ParseFromBinary(blob)
				require.NoError(t, err)
				matches, err := tx.CurrentFieldsMatchRaw(parsed)
				require.NoError(t, err)
				require.True(t, matches)
				parsed.SetRawBytes(nil)
				rebuilt, err := tx.SerializeTransaction(parsed)
				require.NoError(t, err)
				require.Equal(t, blob, rebuilt)
			})
		}
	}
}

func TestWithdrawalCredentialAuthorization(t *testing.T) {
	for _, kind := range []string{"VaultWithdraw", "LoanBrokerCoverWithdraw"} {
		for _, tc := range []struct {
			name           string
			missing        bool
			omitted        bool
			wrongSubject   bool
			unaccepted     bool
			expired        bool
			self           bool
			accountPreauth bool
			noPreauth      bool
			want           string
		}{
			{name: "credential preauth", want: jtx.TesSUCCESS},
			{name: "credentials omitted", omitted: true, want: jtx.TecNO_PERMISSION},
			{name: "account preauth without credentials", omitted: true, accountPreauth: true, want: jtx.TesSUCCESS},
			{name: "no preauth", noPreauth: true, want: jtx.TecNO_PERMISSION},
			{name: "missing", missing: true, want: ter.TecBAD_CREDENTIALS.String()},
			{name: "wrong subject", wrongSubject: true, want: ter.TecBAD_CREDENTIALS.String()},
			{name: "unaccepted", unaccepted: true, want: ter.TecBAD_CREDENTIALS.String()},
			{name: "expired", expired: true, want: jtx.TecEXPIRED},
			{name: "account preauth missing", accountPreauth: true, missing: true, want: ter.TecBAD_CREDENTIALS.String()},
			{name: "account preauth expired", accountPreauth: true, expired: true, want: jtx.TecEXPIRED},
			{name: "self missing", self: true, missing: true, want: ter.TecBAD_CREDENTIALS.String()},
			{name: "self expired", self: true, expired: true, want: jtx.TesSUCCESS},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				env := newLendingEnv(t)
				env.EnableFeature("Credentials")
				env.EnableFeature("fixCleanup3_4_0")
				env.Close()
				owner, issuer, destination := jtx.NewAccount("owner"), jtx.NewAccount("issuer"), jtx.NewAccount("destination")
				env.FundAmount(owner, 10_000_000_000)
				env.Fund(issuer, destination)
				vaultSeq := env.Seq(owner)
				vid := setupXRPVault(t, env, owner, 2_000_000_000)
				objectKey := keylet.Vault(owner.AccountID(), vaultSeq)
				id := vid
				if kind == "LoanBrokerCoverWithdraw" {
					seq := env.Seq(owner)
					jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerSet(owner.Address, vid)))
					id = brokerID(owner, seq)
					objectKey = keylet.LoanBroker(owner.AccountID(), seq)
					jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerCoverDeposit(owner.Address, id, tx.NewXRPAmount(500_000_000))))
				}
				jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(destination).DepositAuth().Build()))
				if tc.accountPreauth {
					jtx.RequireTxSuccess(t, env.Submit(depositpreauth.Auth(destination, owner).Build()))
				} else if !tc.noPreauth {
					jtx.RequireTxSuccess(t, env.Submit(depositpreauth.AuthCredentials(destination, []depositpreauth.AuthorizeCredentials{
						depositpreauth.AuthorizeCredentialText(issuer, "withdraw"),
					}).Build()))
				}
				subject := owner
				if tc.wrongSubject {
					subject = destination
				}
				credKey := keylet.Credential(subject.AccountID(), issuer.AccountID(), []byte("withdraw"))
				if !tc.missing {
					create := credential.CredentialCreateText(issuer, subject, "withdraw")
					if tc.expired {
						create.Expiration(env.NowRipple() + 100)
					}
					jtx.RequireTxSuccess(t, env.Submit(create.Build()))
					if !tc.unaccepted {
						jtx.RequireTxSuccess(t, env.Submit(credential.CredentialAcceptText(subject, issuer, "withdraw").Build()))
					}
					if tc.expired {
						env.CloseToParentCloseTime(env.NowRipple() + 200)
					}
				}
				credentialIDs := []string{fmt.Sprintf("%X", credKey.Key)}
				if tc.omitted {
					credentialIDs = nil
				}
				dst := destination
				if tc.self {
					dst = owner
				}
				const amount = 100_000_000
				var withdraw tx.Transaction
				if kind == "VaultWithdraw" {
					w := vault.NewVaultWithdraw(owner.Address, id, tx.NewXRPAmount(amount))
					w.CredentialIDs, w.Destination = credentialIDs, dst.Address
					withdraw = w
				} else {
					w := lending.NewLoanBrokerCoverWithdraw(owner.Address, id, tx.NewXRPAmount(amount))
					w.CredentialIDs, w.Destination = credentialIDs, dst.Address
					withdraw = w
				}
				withdraw.GetCommon().Fee = "10"
				objectBefore, err := env.LedgerEntry(objectKey)
				require.NoError(t, err)
				ownerBefore, dstBefore := env.Balance(owner), env.Balance(dst)
				seqBefore, countBefore := env.Seq(owner), env.OwnerCount(owner)
				result := env.Submit(withdraw)
				require.Equal(t, tc.want, result.Code)
				require.Equal(t, seqBefore+1, env.Seq(owner))
				if tc.want == jtx.TesSUCCESS {
					if tc.self {
						require.Equal(t, ownerBefore+amount-10, env.Balance(owner))
					} else {
						require.Equal(t, ownerBefore-10, env.Balance(owner))
						require.Equal(t, dstBefore+amount, env.Balance(dst))
					}
				} else {
					objectAfter, err := env.LedgerEntry(objectKey)
					require.NoError(t, err)
					require.Equal(t, objectBefore, objectAfter, "withdrawal state must roll back")
					for _, node := range result.Metadata.AffectedNodes {
						switch node.LedgerEntryType {
						case "AccountRoot", "DirectoryNode", "Credential":
						default:
							t.Errorf("failed withdrawal changed %s", node.LedgerEntryType)
						}
					}
					require.Equal(t, ownerBefore-10, env.Balance(owner))
					if !tc.self {
						require.Equal(t, dstBefore, env.Balance(dst))
					}
				}
				deleted := tc.want == jtx.TecEXPIRED
				require.Equal(t, !tc.missing && !deleted, env.LedgerEntryExists(credKey))
				if deleted {
					require.Equal(t, countBefore-1, env.OwnerCount(owner))
					jtx.RequireOwnerDirectoryContains(t, env, issuer, credKey.Key, false)
					jtx.RequireOwnerDirectoryContains(t, env, owner, credKey.Key, false)
				} else {
					require.Equal(t, countBefore, env.OwnerCount(owner))
				}
			})
		}
	}
}
