package credential_test

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	credentialtest "github.com/LeJamon/go-xrpl/internal/testing/credential"
	"github.com/LeJamon/go-xrpl/internal/testing/depositpreauth"
	"github.com/LeJamon/go-xrpl/internal/tx"
	accounttx "github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/LeJamon/go-xrpl/internal/tx/escrow"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/LeJamon/go-xrpl/internal/tx/paychan"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

type credentialWithdrawalFixture struct {
	env                        *jtx.TestEnv
	owner, destination, issuer *jtx.Account
	object                     keylet.Keylet
	build                      func([]string, string) tx.Transaction
}

func newCredentialWithdrawalFixture(t *testing.T, broker bool) credentialWithdrawalFixture {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.EnableFeature("fixCleanup3_4_0")
	env.EnableFeature("SingleAssetVault")
	env.EnableFeature("LendingProtocol")
	owner, destination, issuer := jtx.NewAccount("owner"), jtx.NewAccount("destination"), jtx.NewAccount("issuer")
	env.Fund(owner, destination, issuer)
	env.Close()
	vk := keylet.Vault(owner.ID, env.Seq(owner))
	id := hex.EncodeToString(vk.Key[:])
	create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "XRP"})
	create.Fee = "50000000"
	jtx.RequireTxSuccess(t, env.Submit(create))
	f := credentialWithdrawalFixture{env: env, owner: owner, destination: destination, issuer: issuer, object: vk}
	if broker {
		f.object = keylet.LoanBroker(owner.ID, env.Seq(owner))
		createBroker := lending.NewLoanBrokerSet(owner.Address, id)
		createBroker.Fee = "50000000"
		jtx.RequireTxSuccess(t, env.Submit(createBroker))
		id = hex.EncodeToString(f.object.Key[:])
		jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerCoverDeposit(owner.Address, id, tx.NewXRPAmount(1000000))))
		f.build = func(ids []string, dst string) tx.Transaction {
			w := lending.NewLoanBrokerCoverWithdraw(owner.Address, id, tx.NewXRPAmount(100))
			w.CredentialIDs, w.Destination = ids, dst
			return w
		}
	} else {
		jtx.RequireTxSuccess(t, env.Submit(vault.NewVaultDeposit(owner.Address, id, tx.NewXRPAmount(1000000))))
		f.build = func(ids []string, dst string) tx.Transaction {
			w := vault.NewVaultWithdraw(owner.Address, id, tx.NewXRPAmount(100))
			w.CredentialIDs, w.Destination = ids, dst
			return w
		}
	}
	env.Close()
	return f
}

func (f credentialWithdrawalFixture) issue(t *testing.T, subject *jtx.Account, kind string, accepted bool, expiration uint32) string {
	t.Helper()
	b := credentialtest.CredentialCreateText(f.issuer, subject, kind)
	if expiration != 0 {
		b.Expiration(expiration)
	}
	jtx.RequireTxSuccess(t, f.env.Submit(b.Build()))
	if accepted {
		jtx.RequireTxSuccess(t, f.env.Submit(credentialtest.CredentialAcceptText(subject, f.issuer, kind).Build()))
	}
	k := keylet.Credential(subject.ID, f.issuer.ID, []byte(kind))
	return hex.EncodeToString(k.Key[:])
}

func TestCleanupCredentialWithdrawals(t *testing.T) {
	for _, broker := range []bool{false, true} {
		t.Run(fmt.Sprintf("broker=%v", broker), func(t *testing.T) {
			f := newCredentialWithdrawalFixture(t, broker)
			env := f.env
			env.EnableDepositAuth(f.destination)
			expiry := env.NowRipple() + 1000
			valid := f.issue(t, f.owner, "withdraw", true, expiry)
			unaccepted := f.issue(t, f.owner, "unaccepted", false, 0)
			wrongSubject := f.issue(t, f.destination, "wrong-subject", true, 0)
			unauthorized := f.issue(t, f.owner, "unauthorized", true, 0)
			jtx.RequireTxSuccess(t, env.Submit(depositpreauth.AuthCredentials(f.destination, []depositpreauth.AuthorizeCredentials{{Issuer: f.issuer, CredTypeText: "withdraw"}}).Build()))
			env.Close()
			for _, tc := range []struct {
				name string
				ids  []string
				want string
			}{
				{"absent", nil, "tecNO_PERMISSION"},
				{"empty", []string{}, "temMALFORMED"},
				{"duplicate", []string{valid, strings.ToUpper(valid)}, "temMALFORMED"},
				{"too many", []string{valid, valid, valid, valid, valid, valid, valid, valid, valid}, "temMALFORMED"},
				{"zero", []string{strings.Repeat("0", 64)}, "temMALFORMED"},
				{"missing", []string{strings.Repeat("F", 64)}, "tecBAD_CREDENTIALS"},
				{"unaccepted", []string{unaccepted}, "tecBAD_CREDENTIALS"},
				{"wrong subject", []string{wrongSubject}, "tecBAD_CREDENTIALS"},
				{"not authorized", []string{unauthorized}, "tecNO_PERMISSION"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					before, err := env.LedgerEntry(f.object)
					require.NoError(t, err)
					balance, seq, dstBalance := env.Balance(f.owner), env.Seq(f.owner), env.Balance(f.destination)
					r := env.Submit(f.build(tc.ids, f.destination.Address))
					jtx.RequireTxFail(t, r, tc.want)
					after, err := env.LedgerEntry(f.object)
					require.NoError(t, err)
					require.Equal(t, before, after)
					require.Equal(t, dstBalance, env.Balance(f.destination))
					if strings.HasPrefix(tc.want, "tec") {
						require.Equal(t, seq+1, env.Seq(f.owner))
						require.Equal(t, balance-10, env.Balance(f.owner))
					} else {
						require.Equal(t, seq, env.Seq(f.owner))
						require.Equal(t, balance, env.Balance(f.owner))
					}
				})
			}
			before := env.Balance(f.destination)
			r := env.Submit(f.build([]string{valid}, f.destination.Address))
			jtx.RequireTxSuccess(t, r)
			require.Equal(t, before+100, env.Balance(f.destination))
			require.NotNil(t, r.Metadata)
			field := "AssetsTotal"
			if broker {
				field = "CoverAvailable"
			}
			found := false
			for _, node := range r.Metadata.AffectedNodes {
				if strings.EqualFold(node.LedgerIndex, hex.EncodeToString(f.object.Key[:])) {
					found = true
					require.Equal(t, "ModifiedNode", node.NodeType)
					require.Equal(t, "1000000", fmt.Sprint(node.PreviousFields[field]))
					require.Equal(t, "999900", fmt.Sprint(node.FinalFields[field]))
				}
			}
			require.True(t, found)

			env.CloseToParentCloseTime(expiry)
			jtx.RequireTxSuccess(t, env.Submit(f.build([]string{valid}, f.destination.Address)))
			env.CloseToParentCloseTime(expiry + 1)
			// A self-withdrawal validates credentials but does not expire them.
			jtx.RequireTxSuccess(t, env.Submit(f.build([]string{valid}, "")))
			ck := keylet.Credential(f.owner.ID, f.issuer.ID, []byte("withdraw"))
			jtx.RequireLedgerEntryExists(t, env, ck)
			beforeObject, err := env.LedgerEntry(f.object)
			require.NoError(t, err)
			balance, seq, dstBalance := env.Balance(f.owner), env.Seq(f.owner), env.Balance(f.destination)
			r = env.Submit(f.build([]string{valid}, f.destination.Address))
			jtx.RequireTxFail(t, r, "tecEXPIRED")
			jtx.RequireLedgerEntryNotExists(t, env, ck)
			deleted := false
			for _, node := range r.Metadata.AffectedNodes {
				if strings.EqualFold(node.LedgerIndex, hex.EncodeToString(ck.Key[:])) {
					deleted = true
					require.Equal(t, "DeletedNode", node.NodeType)
					require.Equal(t, "Credential", node.LedgerEntryType)
				}
			}
			require.True(t, deleted)

			afterObject, err := env.LedgerEntry(f.object)
			require.NoError(t, err)
			require.Equal(t, beforeObject, afterObject)
			require.Equal(t, dstBalance, env.Balance(f.destination))
			require.Equal(t, seq+1, env.Seq(f.owner))
			require.Equal(t, balance-10, env.Balance(f.owner))
			jtx.RequireOwnerDirectoryContains(t, env, f.owner, ck.Key, false)
			jtx.RequireOwnerDirectoryContains(t, env, f.issuer, ck.Key, false)
		})
	}
}

func TestCredentialWithdrawalFeatureGatesAndWire(t *testing.T) {
	for _, broker := range []bool{false, true} {
		for _, credentials := range []bool{false, true} {
			for _, cleanup := range []bool{false, true} {
				t.Run(fmt.Sprintf("broker=%v/credentials=%v/cleanup=%v", broker, credentials, cleanup), func(t *testing.T) {
					f := newCredentialWithdrawalFixture(t, broker)
					if !credentials {
						f.env.DisableFeature("Credentials")
					}
					if !cleanup {
						f.env.DisableFeature("fixCleanup3_4_0")
					}
					f.env.Close()
					for _, ids := range [][]string{{}, {strings.Repeat("0", 64)}, {strings.Repeat("A", 64)}} {
						txn := f.build(ids, "")
						r := f.env.Submit(txn)
						want := "temDISABLED"
						if credentials && cleanup {
							want = "tecBAD_CREDENTIALS"
							if len(ids) == 0 || ids[0] == strings.Repeat("0", 64) {
								want = "temMALFORMED"
							}
						}
						jtx.RequireTxFail(t, r, want)
						flat, err := txn.Flatten()
						require.NoError(t, err)
						blob, err := binarycodec.Encode(flat)
						require.NoError(t, err)
						decoded, err := binarycodec.Decode(blob)
						require.NoError(t, err)
						require.Contains(t, decoded, "CredentialIDs")
						data, err := json.Marshal(decoded)
						require.NoError(t, err)
						parsed, err := tx.ParseJSON(data)
						require.NoError(t, err)
						parsedFlat, err := parsed.Flatten()
						require.NoError(t, err)
						require.Contains(t, parsedFlat, "CredentialIDs")
					}
					jtx.RequireTxSuccess(t, f.env.Submit(f.build(nil, "")))
				})
			}
		}
	}
}

func TestPinnedCredentialsBlockPseudoAccountDeletion(t *testing.T) {
	for _, broker := range []bool{false, true} {
		for _, cleanup := range []bool{false, true} {
			t.Run(fmt.Sprintf("broker=%v/cleanup=%v", broker, cleanup), func(t *testing.T) {
				f := newCredentialWithdrawalFixture(t, broker)
				if !cleanup {
					f.env.DisableFeature("fixCleanup3_4_0")
					f.env.Close()
				}
				raw, err := f.env.LedgerEntry(f.object)
				require.NoError(t, err)
				obj, err := binarycodec.DecodeBytes(raw)
				require.NoError(t, err)
				pseudo := jtx.NewAccountWithAddress("pseudo", obj["Account"].(string))
				f.env.DisableFeature("fixCleanup3_3_0")
				f.env.Close()
				f.issue(t, pseudo, "pinned", false, 0)
				f.env.EnableFeature("fixCleanup3_3_0")
				f.env.Close()
				id := hex.EncodeToString(f.object.Key[:])
				var deletion tx.Transaction
				if broker {
					deletion = lending.NewLoanBrokerDelete(f.owner.Address, id)
				} else {
					jtx.RequireTxSuccess(t, f.env.Submit(vault.NewVaultWithdraw(f.owner.Address, id, tx.NewXRPAmount(1000000))))
					deletion = vault.NewVaultDelete(f.owner.Address, id)
				}
				before, err := f.env.LedgerEntry(f.object)
				require.NoError(t, err)
				ck := keylet.Credential(pseudo.ID, f.issuer.ID, []byte("pinned"))
				balance, seq := f.env.Balance(f.owner), f.env.Seq(f.owner)
				result := f.env.Submit(deletion)
				jtx.RequireTxFail(t, result, "tecHAS_OBLIGATIONS")
				after, err := f.env.LedgerEntry(f.object)
				require.NoError(t, err)
				require.Equal(t, before, after)
				require.Equal(t, balance-10, f.env.Balance(f.owner))
				require.Equal(t, seq+1, f.env.Seq(f.owner))
				jtx.RequireLedgerEntryExists(t, f.env, ck)
				jtx.RequireLedgerEntryExists(t, f.env, keylet.Account(pseudo.ID))
				jtx.RequireOwnerDirectoryContains(t, f.env, pseudo, ck.Key, true)
			})
		}
	}
}

func TestCleanupZeroCredentialPreflightConsumers(t *testing.T) {
	owner, destination := jtx.NewAccount("owner"), jtx.NewAccount("destination")
	ids := []string{strings.Repeat("0", 64)}
	p := payment.NewPayment(owner.Address, destination.Address, tx.NewXRPAmount(100))
	p.CredentialIDs = ids
	d := accounttx.NewAccountDelete(owner.Address, destination.Address)
	d.CredentialIDs = ids
	c := paychan.NewPaymentChannelClaim(owner.Address, strings.Repeat("A", 64))
	c.CredentialIDs = ids
	e := escrow.NewEscrowFinish(owner.Address, owner.Address, 1)
	e.CredentialIDs = ids
	for _, txn := range []tx.Transaction{p, d, c, e} {
		t.Run(fmt.Sprint(txn.TxType()), func(t *testing.T) {
			common := txn.GetCommon()
			common.Fee = "10"
			seq := uint32(1)
			common.Sequence = &seq
			for _, cleanup := range []bool{false, true} {
				builder := amendment.NewRulesBuilder().Enable(amendment.FeatureCredentials)
				if cleanup {
					builder.Enable(amendment.FeatureFixCleanup3_4_0)
				}
				rules := builder.Build()
				var err error
				if stage, ok := txn.(tx.SigValidatedPreflighter); ok {
					err = stage.PreflightSigValidated(rules)
				} else {
					err = txn.(tx.RulesAwarePreflighter).PreflightWithRules(rules)
				}
				if cleanup {
					require.ErrorContains(t, err, "temMALFORMED")
				} else {
					require.NoError(t, err)
				}
			}
		})
	}
}

func TestCredentialWithdrawalDestinationPrecedence(t *testing.T) {
	for _, broker := range []bool{false, true} {
		t.Run(fmt.Sprintf("broker=%v", broker), func(t *testing.T) {
			f := newCredentialWithdrawalFixture(t, broker)
			f.env.EnableRequireDest(f.destination)
			f.env.EnableDepositAuth(f.destination)
			valid := f.issue(t, f.owner, "withdraw", true, 0)
			f.env.Close()
			jtx.RequireTxFail(t, f.env.Submit(f.build([]string{strings.Repeat("F", 64)}, f.destination.Address)), "tecBAD_CREDENTIALS")
			jtx.RequireTxFail(t, f.env.Submit(f.build([]string{valid}, f.destination.Address)), "tecDST_TAG_NEEDED")
			txn := f.build([]string{valid}, f.destination.Address)
			tag := uint32(1)
			if w, ok := txn.(*vault.VaultWithdraw); ok {
				w.DestinationTag = &tag
			} else {
				txn.(*lending.LoanBrokerCoverWithdraw).DestinationTag = &tag
			}
			jtx.RequireTxFail(t, f.env.Submit(txn), "tecNO_PERMISSION")
			jtx.RequireTxFail(t, f.env.Submit(f.build([]string{strings.Repeat("F", 64)}, jtx.NewAccount("missing").Address)), "tecBAD_CREDENTIALS")
			jtx.RequireTxFail(t, f.env.Submit(f.build([]string{valid}, jtx.NewAccount("missing").Address)), "tecNO_DST")
			if !broker {
				raw, err := f.env.LedgerEntry(f.object)
				require.NoError(t, err)
				fields, err := binarycodec.DecodeBytes(raw)
				require.NoError(t, err)
				pseudo := fields["Account"].(string)
				jtx.RequireTxFail(t, f.env.Submit(f.build([]string{strings.Repeat("F", 64)}, pseudo)), "tecBAD_CREDENTIALS")
				jtx.RequireTxFail(t, f.env.Submit(f.build([]string{valid}, pseudo)), "tecPSEUDO_ACCOUNT")
			}
		})
	}
}
