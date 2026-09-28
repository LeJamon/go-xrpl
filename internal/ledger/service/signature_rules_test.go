package service

import (
	"context"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	accounttx "github.com/LeJamon/go-xrpl/internal/tx/account"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"
	"github.com/LeJamon/go-xrpl/internal/tx/sigcache"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func roleRuleService(t *testing.T, cleanup bool) (*Service, *jtx.Account) {
	t.Helper()
	config := DefaultConfig()
	config.Startup.Mode = StartupFresh
	config.GenesisConfig.Amendments = append(config.GenesisConfig.Amendments, amendment.FeatureSponsor)
	if cleanup {
		config.GenesisConfig.Amendments = append(config.GenesisConfig.Amendments, amendment.FeatureFixCleanup3_4_0)
	}
	svc, err := New(config)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	sponsor := jtx.NewAccount("service-role-sponsor")
	transfer := payment.Pay(jtx.MasterAccount(), sponsor, 100_000_000).Sequence(1).Build()
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)
	env.SignWith(transfer, jtx.MasterAccount())
	blob := roleRuleBlob(t, transfer)
	outcome, err := svc.SubmitTransaction(transfer, blob, false)
	require.NoError(t, err)
	require.Equal(t, ter.TesSUCCESS, outcome.Result)
	require.True(t, outcome.Applied)
	_, err = svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	svc.mu.Lock()
	svc.config.Standalone = false
	svc.mu.Unlock()
	require.Equal(t, cleanup, svc.TransactionRules().FixCleanup3_4_0Enabled())
	return svc, sponsor
}

func roleRuleBlob(t *testing.T, transaction tx.Transaction) []byte {
	t.Helper()
	fields, err := transaction.Flatten()
	require.NoError(t, err)
	blobHex, err := binarycodec.Encode(fields)
	require.NoError(t, err)
	blob, err := hex.DecodeString(blobHex)
	require.NoError(t, err)
	return blob
}

func sponsoredRoleRuleBlob(t *testing.T, sponsor *jtx.Account, rules *amendment.Rules) ([]byte, [32]byte) {
	t.Helper()
	master := jtx.MasterAccount()
	transaction := accounttx.NewAccountSet(master.Address)
	transaction.Fee = "20"
	transaction.SetSequence(2)
	transaction.SigningPubKey = master.PublicKeyHex()
	transaction.Sponsor = sponsor.Address
	flags := tx.SpfSponsorFee
	transaction.SponsorFlags = &flags
	var err error
	transaction.SponsorSignature, err = sign.SignSponsorWithRules(transaction, sponsor.PublicKeyHex(), "00"+sponsor.PrivateKeyHex(), rules)
	require.NoError(t, err)
	transaction.TxnSignature, err = sign.SignTransaction(transaction, "00"+master.PrivateKeyHex())
	require.NoError(t, err)
	blob := roleRuleBlob(t, transaction)
	hash, err := tx.ComputeTransactionHash(transaction)
	require.NoError(t, err)
	return blob, hash
}

func oppositeServiceRoleRules(rules *amendment.Rules) *amendment.Rules {
	ids := rules.EnabledIDs()
	if !rules.FixCleanup3_4_0Enabled() {
		return amendment.NewRules(append(ids, amendment.FeatureFixCleanup3_4_0))
	}
	filtered := ids[:0]
	for _, id := range ids {
		if id != amendment.FeatureFixCleanup3_4_0 {
			filtered = append(filtered, id)
		}
	}
	return amendment.NewRules(filtered)
}

func TestServiceRolePrewarmUsesOpenRules(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		t.Run(fmt.Sprint(cleanup), func(t *testing.T) {
			sigcache.Reset()
			svc, sponsor := roleRuleService(t, cleanup)
			rules := svc.TransactionRules()
			valid, validHash := sponsoredRoleRuleBlob(t, sponsor, rules)
			invalid, invalidHash := sponsoredRoleRuleBlob(t, sponsor, oppositeServiceRoleRules(rules))
			svc.PrewarmSignaturesContext(context.Background(), [][]byte{valid, invalid})
			require.True(t, sigcache.VerifiedWithRules(validHash, !cleanup))
			require.False(t, sigcache.VerifiedWithRules(validHash, cleanup))
			require.False(t, sigcache.VerifiedWithRules(invalidHash, false))
			require.False(t, sigcache.VerifiedWithRules(invalidHash, true))
		})
	}
}

func TestServiceRoleSubmissionRechecksPublishedRules(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		for _, initiallyValid := range []bool{false, true} {
			for _, rpc := range []bool{false, true} {
				t.Run(fmt.Sprintf("cleanup=%t/initially-valid=%t/rpc=%t", cleanup, initiallyValid, rpc), func(t *testing.T) {
					sigcache.Reset()
					svc, sponsor := roleRuleService(t, cleanup)
					initialRules := svc.TransactionRules()
					finalRules := oppositeServiceRoleRules(initialRules)
					signingRules := finalRules
					if initiallyValid {
						signingRules = initialRules
					}
					blob, hash := sponsoredRoleRuleBlob(t, sponsor, signingRules)
					masterBefore, err := svc.GetAccountInfo(context.Background(), jtx.MasterAccount().Address, "current")
					require.NoError(t, err)
					sponsorBefore, err := svc.GetAccountInfo(context.Background(), sponsor.Address, "current")
					require.NoError(t, err)
					stateBefore, err := svc.openLedgerView.Current().StateMapHash()
					require.NoError(t, err)

					type submission struct {
						code     ter.Result
						fee      uint64
						applied  bool
						metadata *tx.Metadata
						err      error
					}
					done := make(chan submission, 1)
					svc.openLedgerMu.Lock()
					locked := true
					defer func() {
						if locked {
							svc.openLedgerMu.Unlock()
						}
					}()
					go func() {
						if rpc {
							transaction, parseErr := tx.ParseFromBinary(blob)
							if parseErr != nil {
								done <- submission{err: parseErr}
								return
							}
							result, submitErr := svc.SubmitTransaction(transaction, blob, false)
							if submitErr != nil {
								done <- submission{err: submitErr}
								return
							}
							done <- submission{result.Result, result.Fee, result.Applied, result.Metadata, nil}
							return
						}
						result, submitErr := svc.SubmitOpenLedgerTxDetailed(blob, false)
						done <- submission{result.Result, result.Fee, result.Applied, result.Metadata, submitErr}
					}()
					require.Eventually(t, func() bool {
						return svc.openLedgerMu.Snapshot().QueuedIngress == 1
					}, 5*time.Second, time.Millisecond)
					next, err := openledger.New(svc.GetClosedLedger(), openledger.Config{Rules: finalRules, Logger: svc.logger})
					require.NoError(t, err)
					svc.mu.Lock()
					svc.openLedgerView, svc.openLedger = next, next.Current()
					svc.mu.Unlock()
					svc.openLedgerMu.Unlock()
					locked = false
					result := <-done

					masterData, err := svc.openLedgerView.Current().Read(keylet.Account(jtx.MasterAccount().ID))
					require.NoError(t, err)
					masterAfter, err := state.ParseAccountRoot(masterData)
					require.NoError(t, err)
					sponsorData, err := svc.openLedgerView.Current().Read(keylet.Account(sponsor.ID))
					require.NoError(t, err)
					sponsorAfter, err := state.ParseAccountRoot(sponsorData)
					require.NoError(t, err)
					require.Equal(t, masterBefore.Balance, masterAfter.Balance)
					exists, err := svc.openLedgerView.Current().TxExists(hash)
					require.NoError(t, err)
					if initiallyValid {
						if rpc {
							require.NoError(t, result.err)
							require.Equal(t, ter.TemBAD_SIGNATURE, result.code)
						} else {
							require.ErrorIs(t, result.err, txengine.ErrInvalidSignature)
							var verificationErr *SignatureVerificationError
							require.ErrorAs(t, result.err, &verificationErr)
							require.Same(t, finalRules, verificationErr.Rules)
						}
						require.False(t, result.applied)
						require.Zero(t, result.fee)
						require.Nil(t, result.metadata)
						require.False(t, exists)
						require.Equal(t, masterBefore.Sequence, masterAfter.Sequence)
						require.Equal(t, sponsorBefore.Balance, sponsorAfter.Balance)
						stateAfter, err := svc.openLedgerView.Current().StateMapHash()
						require.NoError(t, err)
						require.Equal(t, stateBefore, stateAfter)
					} else {
						require.NoError(t, result.err)
						require.Equal(t, ter.TesSUCCESS, result.code)
						require.True(t, result.applied)
						require.Equal(t, uint64(20), result.fee)
						require.NotNil(t, result.metadata)
						require.True(t, exists)
						require.Equal(t, masterBefore.Sequence+1, masterAfter.Sequence)
						require.Equal(t, sponsorBefore.Balance-20, sponsorAfter.Balance)
					}
				})
			}
		}
	}
}
