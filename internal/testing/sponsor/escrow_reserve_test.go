package sponsor_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	accountset "github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/testing/escrow"
	mpttest "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	trustsettx "github.com/LeJamon/go-xrpl/internal/tx/trustset"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func newSponsoredXRPEnv(t *testing.T, cleanupEnabled bool) (*jtx.TestEnv, *jtx.Account, *jtx.Account, *jtx.Account, *jtx.Account) {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.EnableFeature("Sponsor")
	if cleanupEnabled {
		env.EnableFeature("fixCleanup3_4_0")
	} else {
		env.DisableFeature("fixCleanup3_4_0")
	}
	owner := jtx.NewAccount("escrow-owner")
	destination := jtx.NewAccount("escrow-destination")
	sponsor := jtx.NewAccount("escrow-sponsor")
	finisher := jtx.NewAccount("escrow-finisher")
	for _, account := range []*jtx.Account{owner, destination, sponsor, finisher} {
		env.FundAmount(account, uint64(jtx.XRP(10_000)))
	}
	env.Close()
	return env, owner, destination, sponsor, finisher
}

func newSponsoredIOUEnv(t *testing.T, cleanupEnabled bool) (*jtx.TestEnv, *jtx.Account, *jtx.Account, *jtx.Account, *jtx.Account, *jtx.Account) {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.EnableFeature("Sponsor")
	env.EnableFeature("TokenEscrow")
	if cleanupEnabled {
		env.EnableFeature("fixCleanup3_4_0")
	} else {
		env.DisableFeature("fixCleanup3_4_0")
	}
	owner := jtx.NewAccount("iou-escrow-owner")
	destination := jtx.NewAccount("iou-escrow-destination")
	issuer := jtx.NewAccount("iou-escrow-issuer")
	sponsor1 := jtx.NewAccount("iou-escrow-sponsor1")
	sponsor2 := jtx.NewAccount("iou-escrow-sponsor2")
	for _, account := range []*jtx.Account{owner, destination, issuer, sponsor1, sponsor2} {
		env.FundAmount(account, uint64(jtx.XRP(10_000)))
	}
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(issuer).AllowTrustLineLocking().Build()))
	env.Close()

	env.Trust(owner, issuer.IOU("USD", 1_000))
	env.Close()
	env.PayIOU(issuer, owner, issuer, "USD", 100)
	env.Close()
	return env, owner, destination, issuer, sponsor1, sponsor2
}

func newSponsoredMPTEnv(t *testing.T, cleanupEnabled bool) (*jtx.TestEnv, *jtx.Account, *jtx.Account, *jtx.Account, *jtx.Account, *jtx.Account, *mpttest.MPTTester) {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.EnableFeature("Sponsor")
	env.EnableFeature("TokenEscrow")
	if cleanupEnabled {
		env.EnableFeature("fixCleanup3_4_0")
	} else {
		env.DisableFeature("fixCleanup3_4_0")
	}
	owner := jtx.NewAccount("mpt-escrow-owner")
	destination := jtx.NewAccount("mpt-escrow-destination")
	issuer := jtx.NewAccount("mpt-escrow-issuer")
	sponsor1 := jtx.NewAccount("mpt-escrow-sponsor1")
	sponsor2 := jtx.NewAccount("mpt-escrow-sponsor2")
	for _, account := range []*jtx.Account{destination, sponsor1, sponsor2} {
		env.FundAmount(account, uint64(jtx.XRP(10_000)))
	}
	mpt := mpttest.NewMPTTester(t, env, issuer, mpttest.MPTInit{Holders: []*jtx.Account{owner}})
	mpt.Create(mpttest.CreateOpts{
		OwnerCount: mpttest.PtrUint32(1),
		Flags:      mpttest.TfMPTCanEscrow | mpttest.TfMPTCanTransfer,
	})
	mpt.Authorize(mpttest.AuthorizeOpts{Account: owner})
	mpt.Pay(issuer, owner, 10_000)
	env.Close()
	return env, owner, destination, issuer, sponsor1, sponsor2, mpt
}

func withReserveSponsor(transaction tx.Transaction, sponsor *jtx.Account) {
	transaction.GetCommon().Sponsor = sponsor.Address
	flags := tx.SpfSponsorReserve
	transaction.GetCommon().SponsorFlags = &flags
}

func withFeeSponsor(transaction tx.Transaction, sponsor *jtx.Account) {
	transaction.GetCommon().Sponsor = sponsor.Address
	flags := tx.SpfSponsorFee
	transaction.GetCommon().SponsorFlags = &flags
}

func effectiveOwnerCount(rootOwnerCount, sponsoredOwnerCount, sponsoringOwnerCount uint32) uint32 {
	return rootOwnerCount - sponsoredOwnerCount + sponsoringOwnerCount
}

func reserveForOneMoreObject(t *testing.T, env *jtx.TestEnv, sponsor *jtx.Account, releaseSponsoredObject bool) uint64 {
	t.Helper()
	root := accountState(t, env, sponsor)
	sponsoringOwnerCount := root.SponsoringOwnerCount
	if releaseSponsoredObject {
		require.Positive(t, sponsoringOwnerCount)
		sponsoringOwnerCount--
	}
	return env.ReserveBase() + (uint64(effectiveOwnerCount(
		root.OwnerCount,
		root.SponsoredOwnerCount,
		sponsoringOwnerCount,
	))+1)*env.ReserveIncrement()
}

func trustLineSponsor(t *testing.T, env *jtx.TestEnv, account, issuer *jtx.Account, currency string) string {
	t.Helper()
	data, err := env.LedgerEntry(keylet.Line(account.ID, issuer.ID, currency))
	require.NoError(t, err)
	line, err := state.ParseRippleState(data)
	require.NoError(t, err)
	if state.CompareAccountIDs(account.ID, issuer.ID) < 0 {
		return line.LowSponsor
	}
	return line.HighSponsor
}

func mptTokenSponsor(t *testing.T, env *jtx.TestEnv, issuanceID string, holder *jtx.Account) string {
	t.Helper()
	id, err := mptutil.DecodeID(issuanceID)
	require.NoError(t, err)
	data, err := env.LedgerEntry(keylet.MPTokenByID(id, holder.ID))
	require.NoError(t, err)
	require.NotNil(t, data)
	token, err := state.ParseMPToken(data)
	require.NoError(t, err)
	return token.Sponsor
}

func requireFeeSequenceOnlyMetadata(t *testing.T, result jtx.TxResult, source *jtx.Account, feePayers ...*jtx.Account) {
	t.Helper()
	require.NotNil(t, result.Metadata)
	allowed := map[string]*jtx.Account{source.Address: source}
	for _, feePayer := range feePayers {
		allowed[feePayer.Address] = feePayer
	}
	require.Len(t, result.Metadata.AffectedNodes, len(allowed))
	seen := make(map[string]struct{}, len(allowed))
	for _, node := range result.Metadata.AffectedNodes {
		require.Equal(t, "ModifiedNode", node.NodeType)
		require.Equal(t, "AccountRoot", node.LedgerEntryType)
		account, ok := node.FinalFields["Account"].(string)
		require.True(t, ok)
		entry, ok := allowed[account]
		require.True(t, ok, "unexpected metadata account %s", account)
		require.NotContains(t, seen, account)
		seen[account] = struct{}{}
		require.Equal(t, fmt.Sprintf("%X", keylet.Account(entry.ID).Key), node.LedgerIndex)
		fields := []string{"Balance"}
		if account == source.Address {
			fields = []string{"Sequence"}
			if len(feePayers) == 0 {
				fields = append(fields, "Balance")
			}
		}
		require.Len(t, node.PreviousFields, len(fields))
		for _, field := range fields {
			require.Contains(t, node.PreviousFields, field)
		}
	}
	require.Contains(t, seen, source.Address)
	for _, feePayer := range feePayers {
		require.Contains(t, seen, feePayer.Address)
	}
}

func snapshotEscrowAccounts(t *testing.T, env *jtx.TestEnv, accounts ...*jtx.Account) map[*jtx.Account]*state.AccountRoot {
	t.Helper()
	snapshot := make(map[*jtx.Account]*state.AccountRoot, len(accounts))
	for _, account := range accounts {
		snapshot[account] = accountState(t, env, account)
	}
	return snapshot
}

func requireEscrowAccountRollback(t *testing.T, env *jtx.TestEnv, before map[*jtx.Account]*state.AccountRoot, result jtx.TxResult, source, feePayer *jtx.Account) {
	t.Helper()
	for account, root := range before {
		expected := *root
		if account == source {
			expected.Sequence++
		}
		if account == feePayer {
			expected.Balance -= result.Fee
		}
		after := accountState(t, env, account)
		expected.PreviousTxnID = after.PreviousTxnID
		expected.PreviousTxnLgrSeq = after.PreviousTxnLgrSeq
		expectedBytes, err := state.SerializeAccountRoot(&expected)
		require.NoError(t, err)
		afterBytes, err := state.SerializeAccountRoot(after)
		require.NoError(t, err)
		require.Equal(t, expectedBytes, afterBytes, "account %s", account.Address)
	}
}

func requireDeletedEscrowMetadata(t *testing.T, result jtx.TxResult, sponsor string) {
	t.Helper()
	require.NotNil(t, result.Metadata)
	for _, node := range result.Metadata.AffectedNodes {
		if node.NodeType == "DeletedNode" && node.LedgerEntryType == "Escrow" {
			require.Equal(t, sponsor, node.FinalFields["Sponsor"])
			return
		}
	}
	t.Fatalf("metadata has no deleted Escrow node")
}

func requireCreatedMetadata(t *testing.T, result jtx.TxResult, entryType string) {
	t.Helper()
	require.NotNil(t, result.Metadata)
	for _, node := range result.Metadata.AffectedNodes {
		if node.NodeType == "CreatedNode" && node.LedgerEntryType == entryType {
			return
		}
	}
	t.Fatalf("metadata has no created %s node", entryType)
}

func TestSponsoredEscrowCancelRecyclesReserveAndChargesTheSelectedFeePayer(t *testing.T) {
	for _, cleanupEnabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("co-signed cleanup=%t", cleanupEnabled), func(t *testing.T) {
			env, owner, destination, sponsor, _ := newSponsoredXRPEnv(t, cleanupEnabled)
			sequence := env.Seq(owner)
			create := escrow.EscrowCreate(owner, destination, jtx.XRP(1_000)).
				FinishTime(env.Now().Add(time.Second)).
				CancelTime(env.Now().Add(2 * time.Second)).
				Build()
			withReserveSponsor(create, sponsor)
			attachSponsorSignature(t, env, create, owner, sponsor)
			jtx.RequireTxSuccess(t, env.SubmitSigned(create))
			env.Close()

			escrowKey := keylet.Escrow(owner.ID, sequence)
			require.True(t, env.LedgerEntryExists(escrowKey))
			gotSponsor, present := objectSponsor(t, env, escrowKey)
			require.True(t, present)
			require.Equal(t, sponsor.Address, gotSponsor)
			require.Equal(t, uint32(1), accountState(t, env, owner).OwnerCount)
			require.Equal(t, uint32(1), accountState(t, env, owner).SponsoredOwnerCount)
			require.Equal(t, uint32(1), accountState(t, env, sponsor).SponsoringOwnerCount)

			// The fee payer must cover its effective reserve and the fee.
			fee := env.BaseFee()
			postObjectReserve := env.ReserveBase() + env.ReserveIncrement()
			setAccountBalance(t, env, sponsor, postObjectReserve+fee-1)
			ownerBefore := accountState(t, env, owner)
			sponsorBefore := accountState(t, env, sponsor)
			ownerBalanceBefore := env.Balance(owner)
			ownerSequenceBefore := env.Seq(owner)

			cancel := escrow.EscrowCancel(owner, owner, sequence).Build()
			withFeeSponsor(cancel, sponsor)
			attachSponsorSignature(t, env, cancel, owner, sponsor)
			failedFee := env.SubmitSigned(cancel)
			jtx.RequireTxFail(t, failedFee, "terINSUF_FEE_B")
			require.Equal(t, ownerSequenceBefore, env.Seq(owner))
			require.Equal(t, ownerBalanceBefore, env.Balance(owner))
			require.Equal(t, postObjectReserve+fee-1, env.Balance(sponsor))
			require.Equal(t, ownerBefore.OwnerCount, accountState(t, env, owner).OwnerCount)
			require.Equal(t, ownerBefore.SponsoredOwnerCount, accountState(t, env, owner).SponsoredOwnerCount)
			require.Equal(t, sponsorBefore.SponsoringOwnerCount, accountState(t, env, sponsor).SponsoringOwnerCount)
			require.True(t, env.LedgerEntryExists(escrowKey))

			setAccountBalance(t, env, sponsor, postObjectReserve+fee)
			cancel = escrow.EscrowCancel(owner, owner, sequence).Build()
			withFeeSponsor(cancel, sponsor)
			attachSponsorSignature(t, env, cancel, owner, sponsor)
			result := env.SubmitSigned(cancel)
			jtx.RequireTxSuccess(t, result)
			requireDeletedEscrowMetadata(t, result, sponsor.Address)
			require.False(t, env.LedgerEntryExists(escrowKey))
			require.Equal(t, ownerBalanceBefore+uint64(jtx.XRP(1_000)), env.Balance(owner))
			require.Equal(t, ownerSequenceBefore+1, env.Seq(owner))
			require.Equal(t, postObjectReserve, env.Balance(sponsor))
			require.Zero(t, accountState(t, env, owner).OwnerCount)
			require.Zero(t, accountState(t, env, owner).SponsoredOwnerCount)
			require.Zero(t, accountState(t, env, sponsor).SponsoringOwnerCount)
		})
	}
}

func TestSponsoredEscrowCancelPrefundedFeeDoesNotRestoreReserveBudget(t *testing.T) {
	env, owner, destination, sponsor, _ := newSponsoredXRPEnv(t, false)
	remaining := int32(1)
	feeBudget := int64(2 * env.BaseFee())
	require.Equal(t, "tesSUCCESS", setSponsorship(env, sponsor, owner, feeBudget, &remaining).Code)

	sequence := env.Seq(owner)
	create := escrow.EscrowCreate(owner, destination, jtx.XRP(1_000)).
		FinishTime(env.Now().Add(time.Second)).
		CancelTime(env.Now().Add(2 * time.Second)).
		Build()
	withReserveSponsor(create, sponsor)
	jtx.RequireTxSuccess(t, env.Submit(create))
	env.Close()
	require.Equal(t, uint32(0), sponsorshipEntry(t, env, sponsor, owner).RemainingOwnerCount)

	ownerBalanceBefore := env.Balance(owner)
	ownerSequenceBefore := env.Seq(owner)
	sponsorBalanceBefore := env.Balance(sponsor)
	env.Close()
	cancel := escrow.EscrowCancel(owner, owner, sequence).Build()
	withFeeSponsor(cancel, sponsor)
	result := env.Submit(cancel)
	jtx.RequireTxSuccess(t, result)
	requireDeletedEscrowMetadata(t, result, sponsor.Address)

	require.False(t, env.LedgerEntryExists(keylet.Escrow(owner.ID, sequence)))
	require.Equal(t, ownerBalanceBefore+uint64(jtx.XRP(1_000)), env.Balance(owner))
	require.Equal(t, ownerSequenceBefore+1, env.Seq(owner))
	require.Equal(t, sponsorBalanceBefore, env.Balance(sponsor), "pre-funded fees are debited from the sponsorship object")
	entry := sponsorshipEntry(t, env, sponsor, owner)
	require.Zero(t, entry.RemainingOwnerCount)
	require.Equal(t, env.BaseFee(), entry.FeeAmount)
	require.Zero(t, accountState(t, env, owner).OwnerCount)
	require.Zero(t, accountState(t, env, owner).SponsoredOwnerCount)
	require.Zero(t, accountState(t, env, sponsor).SponsoringOwnerCount)
}

func TestSponsoredIOUEscrowCancelRecreatesHoldingAfterReserveRecycle(t *testing.T) {
	for _, cleanupEnabled := range []bool{false, true} {
		for _, sameSponsor := range []bool{false, true} {
			t.Run(fmt.Sprintf("cleanup=%t/sameSponsor=%t", cleanupEnabled, sameSponsor), func(t *testing.T) {
				env, owner, destination, issuer, sponsor, sponsor2 := newSponsoredIOUEnv(t, cleanupEnabled)
				remaining := int32(2)
				require.Equal(t, "tesSUCCESS", setSponsorship(env, sponsor, owner, 0, &remaining).Code)

				sequence := env.Seq(owner)
				cancelAfter := env.Now().Add(2 * time.Second)
				create := escrow.EscrowCreate(owner, destination, 0).
					IOUAmount(issuer.IOU("USD", 100)).
					FinishTime(env.Now().Add(time.Second)).
					CancelTime(cancelAfter).
					Build()
				withReserveSponsor(create, sponsor)
				jtx.RequireTxSuccess(t, env.Submit(create))
				env.Close()
				require.Equal(t, uint32(1), sponsorshipEntry(t, env, sponsor, owner).RemainingOwnerCount)

				cancelSponsor := sponsor
				if !sameSponsor {
					cancelSponsor = sponsor2
					remaining := int32(1)
					require.Equal(t, "tesSUCCESS", setSponsorship(env, cancelSponsor, owner, 0, &remaining).Code)
				}

				clear := trustsettx.NewTrustSet(owner.Address, issuer.IOU("USD", 0))
				jtx.RequireTxSuccess(t, env.Submit(clear))
				env.Close()
				require.False(t, env.TrustLineExists(owner, issuer, "USD"))
				require.Equal(t, uint32(1), accountState(t, env, owner).OwnerCount)
				require.Equal(t, uint32(1), accountState(t, env, owner).SponsoredOwnerCount)
				require.Equal(t, uint32(1), accountState(t, env, sponsor).SponsoringOwnerCount)

				env.AdvanceTime(5 * time.Second)
				env.Close()
				setAccountBalance(t, env, owner, env.ReserveBase()+env.ReserveIncrement())
				requiredSponsorReserve := reserveForOneMoreObject(t, env, cancelSponsor, sameSponsor)
				setAccountBalance(t, env, cancelSponsor, requiredSponsorReserve-1)
				ownerBefore := accountState(t, env, owner)
				sponsorBefore := accountState(t, env, sponsor)
				cancelSponsorBefore := accountState(t, env, cancelSponsor)
				ownerBalanceBefore := env.Balance(owner)
				ownerSequenceBefore := env.Seq(owner)

				accountsBefore := snapshotEscrowAccounts(t, env, owner, destination, issuer, sponsor, cancelSponsor)
				cancel := escrow.EscrowCancel(owner, owner, sequence).Build()
				withReserveSponsor(cancel, cancelSponsor)
				failed := env.Submit(cancel)
				jtx.RequireTxFail(t, failed, "tecNO_LINE_INSUF_RESERVE")
				requireFeeSequenceOnlyMetadata(t, failed, owner)
				requireEscrowAccountRollback(t, env, accountsBefore, failed, owner, owner)
				require.True(t, failed.Applied)
				require.Equal(t, env.BaseFee(), failed.Fee)
				require.Equal(t, ownerBalanceBefore-failed.Fee, env.Balance(owner))
				require.Equal(t, ownerSequenceBefore+1, env.Seq(owner))
				require.Equal(t, requiredSponsorReserve-1, env.Balance(cancelSponsor))
				require.True(t, env.LedgerEntryExists(keylet.Escrow(owner.ID, sequence)))
				require.Equal(t, ownerBefore.OwnerCount, accountState(t, env, owner).OwnerCount)
				require.Equal(t, ownerBefore.SponsoredOwnerCount, accountState(t, env, owner).SponsoredOwnerCount)
				require.Equal(t, sponsorBefore.SponsoringOwnerCount, accountState(t, env, sponsor).SponsoringOwnerCount)
				require.Equal(t, cancelSponsorBefore.SponsoringOwnerCount, accountState(t, env, cancelSponsor).SponsoringOwnerCount)
				require.Equal(t, uint32(1), sponsorshipEntry(t, env, cancelSponsor, owner).RemainingOwnerCount)
				require.Equal(t, uint32(1), sponsorshipEntry(t, env, sponsor, owner).RemainingOwnerCount)
				if !cleanupEnabled && sameSponsor {
					return
				}

				setAccountBalance(t, env, cancelSponsor, requiredSponsorReserve)
				cancel = escrow.EscrowCancel(owner, owner, sequence).Build()
				withReserveSponsor(cancel, cancelSponsor)
				result := env.Submit(cancel)
				jtx.RequireTxSuccess(t, result)
				requireDeletedEscrowMetadata(t, result, sponsor.Address)
				requireCreatedMetadata(t, result, "RippleState")
				require.Equal(t, env.BaseFee(), result.Fee)
				require.Equal(t, requiredSponsorReserve, env.Balance(cancelSponsor))
				require.False(t, env.LedgerEntryExists(keylet.Escrow(owner.ID, sequence)))
				require.True(t, env.TrustLineExists(owner, issuer, "USD"))
				require.Equal(t, issuer.IOU("USD", 100), *env.IOUBalance(owner, issuer, "USD"))
				require.Equal(t, ownerSequenceBefore+2, env.Seq(owner))
				require.Equal(t, ownerBalanceBefore-2*env.BaseFee(), env.Balance(owner))
				require.Equal(t, uint32(1), accountState(t, env, owner).OwnerCount)
				require.Equal(t, uint32(1), accountState(t, env, owner).SponsoredOwnerCount)
				require.Equal(t, uint32(1), accountState(t, env, cancelSponsor).SponsoringOwnerCount)
				require.Equal(t, cancelSponsor.Address, trustLineSponsor(t, env, owner, issuer, "USD"))
				require.Zero(t, sponsorshipEntry(t, env, cancelSponsor, owner).RemainingOwnerCount)
				if !sameSponsor {
					require.Zero(t, accountState(t, env, sponsor).SponsoringOwnerCount)
					require.Equal(t, uint32(1), sponsorshipEntry(t, env, sponsor, owner).RemainingOwnerCount)
				}
			})
		}
	}
}

func TestDelegatedIOUEscrowCancelUsesSourceReserveAndDelegateFee(t *testing.T) {
	for _, cleanupEnabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("cleanup=%t", cleanupEnabled), func(t *testing.T) {
			env, owner, destination, issuer, sponsor, delegate := newSponsoredIOUEnv(t, cleanupEnabled)
			remaining := int32(2)
			require.Equal(t, "tesSUCCESS", setSponsorship(env, sponsor, owner, 0, &remaining).Code)

			sequence := env.Seq(owner)
			create := escrow.EscrowCreate(owner, destination, 0).
				IOUAmount(issuer.IOU("USD", 100)).
				FinishTime(env.Now().Add(time.Second)).
				CancelTime(env.Now().Add(2 * time.Second)).
				Build()
			withReserveSponsor(create, sponsor)
			attachSponsorSignature(t, env, create, owner, sponsor)
			jtx.RequireTxSuccess(t, env.SubmitSigned(create))
			env.Close()

			clear := trustsettx.NewTrustSet(owner.Address, issuer.IOU("USD", 0))
			jtx.RequireTxSuccess(t, env.Submit(clear))
			env.Close()
			require.False(t, env.TrustLineExists(owner, issuer, "USD"))

			grantDelegatePermission(t, env, owner, delegate, "EscrowCancel")
			env.AdvanceTime(5 * time.Second)
			env.Close()

			// The delegate pays the fee, so the source reserve check uses its
			// pre-fee balance. Start one drop below the boundary to pin rollback.
			requiredSourceReserve := env.ReserveBase() + 2*env.ReserveIncrement()
			setAccountBalance(t, env, owner, requiredSourceReserve-1)
			ownerBefore := accountState(t, env, owner)
			sponsorBefore := accountState(t, env, sponsor)
			ownerBalanceBefore := env.Balance(owner)
			ownerSequenceBefore := env.Seq(owner)
			delegateBalanceBefore := env.Balance(delegate)
			delegateSequenceBefore := env.Seq(delegate)

			accountsBefore := snapshotEscrowAccounts(t, env, owner, destination, issuer, sponsor, delegate)
			buildCancel := func() tx.Transaction {
				cancel := escrow.EscrowCancel(owner, owner, sequence).Build()
				cancel.Delegate = delegate.Address
				return cancel
			}

			failed := env.SubmitSignedWith(buildCancel(), delegate)
			jtx.RequireTxFail(t, failed, "tecNO_LINE_INSUF_RESERVE")
			requireFeeSequenceOnlyMetadata(t, failed, owner, delegate)
			requireEscrowAccountRollback(t, env, accountsBefore, failed, owner, delegate)
			require.True(t, failed.Applied)
			require.Equal(t, env.BaseFee(), failed.Fee)
			require.Equal(t, ownerBalanceBefore, env.Balance(owner))
			require.Equal(t, ownerSequenceBefore+1, env.Seq(owner))
			require.Equal(t, delegateBalanceBefore-failed.Fee, env.Balance(delegate))
			require.Equal(t, delegateSequenceBefore, env.Seq(delegate))
			require.True(t, env.LedgerEntryExists(keylet.Escrow(owner.ID, sequence)))
			require.Equal(t, ownerBefore.OwnerCount, accountState(t, env, owner).OwnerCount)
			require.Equal(t, ownerBefore.SponsoredOwnerCount, accountState(t, env, owner).SponsoredOwnerCount)
			require.Equal(t, sponsorBefore.SponsoringOwnerCount, accountState(t, env, sponsor).SponsoringOwnerCount)
			require.Equal(t, uint32(1), sponsorshipEntry(t, env, sponsor, owner).RemainingOwnerCount)

			if !cleanupEnabled {
				return
			}

			setAccountBalance(t, env, owner, requiredSourceReserve)
			result := env.SubmitSignedWith(buildCancel(), delegate)
			jtx.RequireTxSuccess(t, result)
			requireDeletedEscrowMetadata(t, result, sponsor.Address)
			requireCreatedMetadata(t, result, "RippleState")
			require.Equal(t, env.BaseFee(), result.Fee)
			require.False(t, env.LedgerEntryExists(keylet.Escrow(owner.ID, sequence)))
			require.True(t, env.TrustLineExists(owner, issuer, "USD"))
			require.Equal(t, issuer.IOU("USD", 100), *env.IOUBalance(owner, issuer, "USD"))
			require.Equal(t, requiredSourceReserve, env.Balance(owner))
			require.Equal(t, ownerSequenceBefore+2, env.Seq(owner))
			require.Equal(t, delegateBalanceBefore-2*env.BaseFee(), env.Balance(delegate))
			require.Equal(t, delegateSequenceBefore, env.Seq(delegate))
			require.Equal(t, uint32(2), accountState(t, env, owner).OwnerCount)
			require.Zero(t, accountState(t, env, owner).SponsoredOwnerCount)
			require.Zero(t, accountState(t, env, sponsor).SponsoringOwnerCount)
			require.Empty(t, trustLineSponsor(t, env, owner, issuer, "USD"))
			require.Equal(t, uint32(1), sponsorshipEntry(t, env, sponsor, owner).RemainingOwnerCount)
		})
	}
}

func TestDelegatedIOUEscrowFinishUsesSourceReserveAndDelegateFee(t *testing.T) {
	for _, cleanupEnabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("cleanup=%t", cleanupEnabled), func(t *testing.T) {
			env, owner, destination, issuer, sponsor, delegate := newSponsoredIOUEnv(t, cleanupEnabled)
			remaining := int32(2)
			require.Equal(t, "tesSUCCESS", setSponsorship(env, sponsor, owner, 0, &remaining).Code)

			sequence := env.Seq(owner)
			create := escrow.EscrowCreate(owner, destination, 0).
				IOUAmount(issuer.IOU("USD", 100)).
				FinishTime(env.Now().Add(time.Second)).
				Build()
			withReserveSponsor(create, sponsor)
			attachSponsorSignature(t, env, create, owner, sponsor)
			jtx.RequireTxSuccess(t, env.SubmitSigned(create))
			env.Close()

			clear := trustsettx.NewTrustSet(owner.Address, issuer.IOU("USD", 0))
			jtx.RequireTxSuccess(t, env.Submit(clear))
			env.Close()
			require.False(t, env.TrustLineExists(owner, issuer, "USD"))

			grantDelegatePermission(t, env, destination, delegate, "EscrowFinish")
			env.AdvanceTime(5 * time.Second)
			env.Close()

			destinationBefore := accountState(t, env, destination)
			requiredSourceReserve := env.ReserveBase() +
				uint64(destinationBefore.OwnerCount+1)*env.ReserveIncrement()
			setAccountBalance(t, env, destination, requiredSourceReserve-1)
			ownerBefore := accountState(t, env, owner)
			sponsorBefore := accountState(t, env, sponsor)
			destinationBalanceBefore := env.Balance(destination)
			destinationSequenceBefore := env.Seq(destination)
			delegateBalanceBefore := env.Balance(delegate)
			delegateSequenceBefore := env.Seq(delegate)

			accountsBefore := snapshotEscrowAccounts(t, env, owner, destination, issuer, sponsor, delegate)
			buildFinish := func() tx.Transaction {
				finish := escrow.EscrowFinish(destination, owner, sequence).Build()
				finish.Delegate = delegate.Address
				return finish
			}

			failed := env.SubmitSignedWith(buildFinish(), delegate)
			jtx.RequireTxFail(t, failed, "tecNO_LINE_INSUF_RESERVE")
			requireFeeSequenceOnlyMetadata(t, failed, destination, delegate)
			requireEscrowAccountRollback(t, env, accountsBefore, failed, destination, delegate)
			require.True(t, failed.Applied)
			require.Equal(t, env.BaseFee(), failed.Fee)
			require.Equal(t, destinationBalanceBefore, env.Balance(destination))
			require.Equal(t, destinationSequenceBefore+1, env.Seq(destination))
			require.Equal(t, delegateBalanceBefore-failed.Fee, env.Balance(delegate))
			require.Equal(t, delegateSequenceBefore, env.Seq(delegate))
			require.True(t, env.LedgerEntryExists(keylet.Escrow(owner.ID, sequence)))
			require.Equal(t, ownerBefore.OwnerCount, accountState(t, env, owner).OwnerCount)
			require.Equal(t, ownerBefore.SponsoredOwnerCount, accountState(t, env, owner).SponsoredOwnerCount)
			require.Equal(t, sponsorBefore.SponsoringOwnerCount, accountState(t, env, sponsor).SponsoringOwnerCount)
			require.False(t, env.TrustLineExists(destination, issuer, "USD"))
			require.Equal(t, uint32(1), sponsorshipEntry(t, env, sponsor, owner).RemainingOwnerCount)

			setAccountBalance(t, env, destination, requiredSourceReserve)
			result := env.SubmitSignedWith(buildFinish(), delegate)
			jtx.RequireTxSuccess(t, result)
			requireDeletedEscrowMetadata(t, result, sponsor.Address)
			requireCreatedMetadata(t, result, "RippleState")
			require.Equal(t, env.BaseFee(), result.Fee)
			require.False(t, env.LedgerEntryExists(keylet.Escrow(owner.ID, sequence)))
			require.True(t, env.TrustLineExists(destination, issuer, "USD"))
			require.Equal(t, issuer.IOU("USD", 100), *env.IOUBalance(destination, issuer, "USD"))
			require.Equal(t, requiredSourceReserve, env.Balance(destination))
			require.Equal(t, destinationSequenceBefore+2, env.Seq(destination))
			require.Equal(t, delegateBalanceBefore-2*env.BaseFee(), env.Balance(delegate))
			require.Equal(t, delegateSequenceBefore, env.Seq(delegate))
			require.Zero(t, accountState(t, env, owner).OwnerCount)
			require.Zero(t, accountState(t, env, owner).SponsoredOwnerCount)
			require.Zero(t, accountState(t, env, sponsor).SponsoringOwnerCount)
			require.Equal(t, uint32(2), accountState(t, env, destination).OwnerCount)
			require.Zero(t, accountState(t, env, destination).SponsoredOwnerCount)
			require.Empty(t, trustLineSponsor(t, env, destination, issuer, "USD"))
			require.Equal(t, uint32(1), sponsorshipEntry(t, env, sponsor, owner).RemainingOwnerCount)
		})
	}
}

func TestSponsoredEscrowFinishReserveSponsorMatrix(t *testing.T) {
	cases := []struct {
		name            string
		sameSponsor     bool
		prefunded       bool
		createPrefunded bool
	}{
		{name: "same reserve sponsor co-signed", sameSponsor: true},
		{name: "same reserve sponsor pre-funded", sameSponsor: true, prefunded: true, createPrefunded: true},
		{name: "different reserve sponsor co-signed"},
		{name: "different reserve sponsor pre-funded", prefunded: true},
		{name: "same reserve sponsor co-signed to pre-funded", sameSponsor: true, prefunded: true},
		{name: "same reserve sponsor pre-funded to co-signed", sameSponsor: true, createPrefunded: true},
		{name: "different reserve sponsor pre-funded to co-signed", createPrefunded: true},
		{name: "different reserve sponsor pre-funded to pre-funded", prefunded: true, createPrefunded: true},
	}

	for _, cleanupEnabled := range []bool{false, true} {
		for _, testCase := range cases {
			t.Run(fmt.Sprintf("cleanup=%t/%s", cleanupEnabled, testCase.name), func(t *testing.T) {
				env, owner, destination, issuer, sponsor1, sponsor2 := newSponsoredIOUEnv(t, cleanupEnabled)
				sequence := env.Seq(owner)
				if testCase.createPrefunded {
					remaining := int32(1)
					require.Equal(t, "tesSUCCESS", setSponsorship(env, sponsor1, owner, 0, &remaining).Code)
				}
				create := escrow.EscrowCreate(owner, destination, 0).
					IOUAmount(issuer.IOU("USD", 100)).
					FinishTime(env.Now().Add(time.Second)).
					Build()
				withReserveSponsor(create, sponsor1)
				if testCase.createPrefunded {
					jtx.RequireTxSuccess(t, env.Submit(create))
				} else {
					attachSponsorSignature(t, env, create, owner, sponsor1)
					jtx.RequireTxSuccess(t, env.SubmitSigned(create))
				}
				env.Close()
				if testCase.createPrefunded {
					require.Zero(t, sponsorshipEntry(t, env, sponsor1, owner).RemainingOwnerCount)
				}

				finishSponsor := sponsor2
				if testCase.sameSponsor {
					finishSponsor = sponsor1
				}
				if testCase.prefunded {
					remaining := int32(1)
					require.Equal(t, "tesSUCCESS", setSponsorship(env, finishSponsor, destination, 0, &remaining).Code)
				}
				require.True(t, env.LedgerEntryExists(keylet.Escrow(owner.ID, sequence)))
				ownerBefore := accountState(t, env, owner)
				sponsor1Before := accountState(t, env, sponsor1)
				finishSponsorBefore := accountState(t, env, finishSponsor)
				destinationBalanceBefore := env.Balance(destination)
				destinationSequenceBefore := env.Seq(destination)
				var budgetBefore uint32
				if testCase.prefunded {
					budgetBefore = sponsorshipEntry(t, env, finishSponsor, destination).RemainingOwnerCount
				}

				required := reserveForOneMoreObject(t, env, finishSponsor, testCase.sameSponsor)
				setAccountBalance(t, env, finishSponsor, required-1)
				finishSponsorBalanceBefore := env.Balance(finishSponsor)
				accountsBefore := snapshotEscrowAccounts(t, env, owner, destination, issuer, sponsor1, finishSponsor)
				finish := escrow.EscrowFinish(destination, owner, sequence).Build()
				withReserveSponsor(finish, finishSponsor)
				var failed jtx.TxResult
				if testCase.prefunded {
					failed = env.Submit(finish)
					jtx.RequireTxFail(t, failed, "tecNO_LINE_INSUF_RESERVE")
				} else {
					attachSponsorSignature(t, env, finish, destination, finishSponsor)
					failed = env.SubmitSigned(finish)
					jtx.RequireTxFail(t, failed, "tecNO_LINE_INSUF_RESERVE")
				}

				requireFeeSequenceOnlyMetadata(t, failed, destination)
				requireEscrowAccountRollback(t, env, accountsBefore, failed, destination, destination)
				require.True(t, failed.Applied)
				require.Equal(t, env.BaseFee(), failed.Fee)
				require.Equal(t, destinationBalanceBefore-failed.Fee, env.Balance(destination))
				require.Equal(t, destinationSequenceBefore+1, env.Seq(destination))
				require.Equal(t, finishSponsorBalanceBefore, env.Balance(finishSponsor))
				require.True(t, env.LedgerEntryExists(keylet.Escrow(owner.ID, sequence)))
				require.Equal(t, ownerBefore.OwnerCount, accountState(t, env, owner).OwnerCount)
				require.Equal(t, ownerBefore.SponsoredOwnerCount, accountState(t, env, owner).SponsoredOwnerCount)
				require.Equal(t, sponsor1Before.SponsoringOwnerCount, accountState(t, env, sponsor1).SponsoringOwnerCount)
				require.Equal(t, finishSponsorBefore.SponsoringOwnerCount, accountState(t, env, finishSponsor).SponsoringOwnerCount)
				if testCase.createPrefunded {
					require.Zero(t, sponsorshipEntry(t, env, sponsor1, owner).RemainingOwnerCount)
				}
				if testCase.prefunded {
					require.Equal(t, budgetBefore, sponsorshipEntry(t, env, finishSponsor, destination).RemainingOwnerCount)
				}

				setAccountBalance(t, env, finishSponsor, required)
				finish = escrow.EscrowFinish(destination, owner, sequence).Build()
				withReserveSponsor(finish, finishSponsor)
				var result jtx.TxResult
				if testCase.prefunded {
					result = env.Submit(finish)
					jtx.RequireTxSuccess(t, result)
				} else {
					attachSponsorSignature(t, env, finish, destination, finishSponsor)
					result = env.SubmitSigned(finish)
					jtx.RequireTxSuccess(t, result)
				}

				requireDeletedEscrowMetadata(t, result, sponsor1.Address)
				requireCreatedMetadata(t, result, "RippleState")
				require.Equal(t, env.BaseFee(), result.Fee)
				require.Equal(t, required, env.Balance(finishSponsor))
				require.Equal(t, destinationBalanceBefore-2*env.BaseFee(), env.Balance(destination))
				require.Equal(t, destinationSequenceBefore+2, env.Seq(destination))
				require.False(t, env.LedgerEntryExists(keylet.Escrow(owner.ID, sequence)))
				require.Equal(t, uint32(1), accountState(t, env, owner).OwnerCount)
				require.Zero(t, accountState(t, env, owner).SponsoredOwnerCount)
				if testCase.sameSponsor {
					require.Equal(t, uint32(1), accountState(t, env, sponsor1).SponsoringOwnerCount)
				} else {
					require.Zero(t, accountState(t, env, sponsor1).SponsoringOwnerCount)
				}
				require.Equal(t, uint32(1), accountState(t, env, destination).OwnerCount)
				require.Equal(t, uint32(1), accountState(t, env, destination).SponsoredOwnerCount)
				require.Equal(t, uint32(1), accountState(t, env, finishSponsor).SponsoringOwnerCount)
				require.True(t, env.TrustLineExists(destination, issuer, "USD"))
				require.Equal(t, finishSponsor.Address, trustLineSponsor(t, env, destination, issuer, "USD"))
				if testCase.createPrefunded {
					require.Zero(t, sponsorshipEntry(t, env, sponsor1, owner).RemainingOwnerCount)
				}
				if testCase.prefunded {
					require.Zero(t, sponsorshipEntry(t, env, finishSponsor, destination).RemainingOwnerCount)
				}
			})
		}
	}
}

func TestSponsoredMPTEscrowFinishRecreatesHoldingWithReserveSponsor(t *testing.T) {
	for _, cleanupEnabled := range []bool{false, true} {
		for _, prefunded := range []bool{false, true} {
			for _, sameSponsor := range []bool{false, true} {
				name := "different reserve sponsor"
				if sameSponsor {
					name = "same reserve sponsor"
				}
				t.Run(fmt.Sprintf("cleanup=%t/prefunded=%t/%s", cleanupEnabled, prefunded, name), func(t *testing.T) {
					env, owner, destination, issuer, sponsor1, sponsor2, mpt := newSponsoredMPTEnv(t, cleanupEnabled)
					sequence := env.Seq(owner)
					if prefunded {
						remaining := int32(1)
						require.Equal(t, "tesSUCCESS", setSponsorship(env, sponsor1, owner, 0, &remaining).Code)
					}
					create := escrow.EscrowCreate(owner, destination, 0).
						MPTAmount(mpt.MPTAmount(100)).
						FinishTime(env.Now().Add(time.Second)).
						Build()
					withReserveSponsor(create, sponsor1)
					if prefunded {
						jtx.RequireTxSuccess(t, env.Submit(create))
						require.Zero(t, sponsorshipEntry(t, env, sponsor1, owner).RemainingOwnerCount)
					} else {
						attachSponsorSignature(t, env, create, owner, sponsor1)
						jtx.RequireTxSuccess(t, env.SubmitSigned(create))
					}
					env.Close()

					finishSponsor := sponsor2
					if sameSponsor {
						finishSponsor = sponsor1
					}
					if prefunded {
						remaining := int32(1)
						require.Equal(t, "tesSUCCESS", setSponsorship(env, finishSponsor, destination, 0, &remaining).Code)
					}
					issuanceID, err := mptutil.DecodeID(mpt.IssuanceID())
					require.NoError(t, err)
					holdingKey := keylet.MPTokenByID(issuanceID, destination.ID)
					require.False(t, env.LedgerEntryExists(holdingKey))
					required := reserveForOneMoreObject(t, env, finishSponsor, sameSponsor)
					setAccountBalance(t, env, finishSponsor, required-1)
					finishSponsorBalanceBefore := env.Balance(finishSponsor)
					ownerBefore := accountState(t, env, owner)
					sponsor1Before := accountState(t, env, sponsor1)
					finishSponsorBefore := accountState(t, env, finishSponsor)
					destinationBalanceBefore := env.Balance(destination)
					destinationSequenceBefore := env.Seq(destination)

					accountsBefore := snapshotEscrowAccounts(t, env, owner, destination, issuer, sponsor1, finishSponsor)
					finish := escrow.EscrowFinish(destination, owner, sequence).Build()
					withReserveSponsor(finish, finishSponsor)
					var failed jtx.TxResult
					if prefunded {
						failed = env.Submit(finish)
					} else {
						attachSponsorSignature(t, env, finish, destination, finishSponsor)
						failed = env.SubmitSigned(finish)
					}
					jtx.RequireTxFail(t, failed, "tecINSUFFICIENT_RESERVE")
					requireFeeSequenceOnlyMetadata(t, failed, destination)
					requireEscrowAccountRollback(t, env, accountsBefore, failed, destination, destination)
					require.True(t, failed.Applied)
					require.Equal(t, env.BaseFee(), failed.Fee)
					require.Equal(t, destinationBalanceBefore-failed.Fee, env.Balance(destination))
					require.Equal(t, destinationSequenceBefore+1, env.Seq(destination))
					require.Equal(t, finishSponsorBalanceBefore, env.Balance(finishSponsor))
					require.True(t, env.LedgerEntryExists(keylet.Escrow(owner.ID, sequence)))
					require.False(t, env.LedgerEntryExists(holdingKey))
					require.Equal(t, ownerBefore.OwnerCount, accountState(t, env, owner).OwnerCount)
					require.Equal(t, ownerBefore.SponsoredOwnerCount, accountState(t, env, owner).SponsoredOwnerCount)
					require.Equal(t, sponsor1Before.SponsoringOwnerCount, accountState(t, env, sponsor1).SponsoringOwnerCount)
					require.Equal(t, finishSponsorBefore.SponsoringOwnerCount, accountState(t, env, finishSponsor).SponsoringOwnerCount)
					mpt.RequireMPTokenAmount(owner, 9_900)
					mpt.RequireMPTokenAmount(destination, 0)
					require.Equal(t, uint64(100), mpt.HolderLockedAmount(owner))
					require.Zero(t, mpt.HolderLockedAmount(destination))
					require.Equal(t, uint64(100), mpt.IssuanceLockedAmount())
					require.Equal(t, uint64(10_000), mpt.IssuanceOutstandingAmount())
					if prefunded {
						require.Zero(t, sponsorshipEntry(t, env, sponsor1, owner).RemainingOwnerCount)
						require.Equal(t, uint32(1), sponsorshipEntry(t, env, finishSponsor, destination).RemainingOwnerCount)
					}

					setAccountBalance(t, env, finishSponsor, required)
					finish = escrow.EscrowFinish(destination, owner, sequence).Build()
					withReserveSponsor(finish, finishSponsor)
					var result jtx.TxResult
					if prefunded {
						result = env.Submit(finish)
					} else {
						attachSponsorSignature(t, env, finish, destination, finishSponsor)
						result = env.SubmitSigned(finish)
					}
					jtx.RequireTxSuccess(t, result)
					requireDeletedEscrowMetadata(t, result, sponsor1.Address)
					requireCreatedMetadata(t, result, "MPToken")
					require.Equal(t, env.BaseFee(), result.Fee)
					require.Equal(t, required, env.Balance(finishSponsor))
					require.Equal(t, destinationBalanceBefore-2*env.BaseFee(), env.Balance(destination))
					require.Equal(t, destinationSequenceBefore+2, env.Seq(destination))
					require.False(t, env.LedgerEntryExists(keylet.Escrow(owner.ID, sequence)))
					require.True(t, env.LedgerEntryExists(holdingKey))
					require.Equal(t, finishSponsor.Address, mptTokenSponsor(t, env, mpt.IssuanceID(), destination))
					mpt.RequireMPTokenAmount(owner, 9_900)
					mpt.RequireMPTokenAmount(destination, 100)
					require.Zero(t, mpt.HolderLockedAmount(owner))
					require.Zero(t, mpt.HolderLockedAmount(destination))
					require.Zero(t, mpt.IssuanceLockedAmount())
					require.Equal(t, uint64(10_000), mpt.IssuanceOutstandingAmount())
					if prefunded {
						require.Zero(t, sponsorshipEntry(t, env, sponsor1, owner).RemainingOwnerCount)
						require.Zero(t, sponsorshipEntry(t, env, finishSponsor, destination).RemainingOwnerCount)
					}
					require.Equal(t, uint32(1), accountState(t, env, owner).OwnerCount)
					require.Zero(t, accountState(t, env, owner).SponsoredOwnerCount)
					require.Equal(t, uint32(1), accountState(t, env, destination).OwnerCount)
					require.Equal(t, uint32(1), accountState(t, env, destination).SponsoredOwnerCount)
					require.Equal(t, uint32(1), accountState(t, env, finishSponsor).SponsoringOwnerCount)
					if sameSponsor {
						require.Equal(t, uint32(1), accountState(t, env, sponsor1).SponsoringOwnerCount)
					} else {
						require.Zero(t, accountState(t, env, sponsor1).SponsoringOwnerCount)
					}
				})
			}
		}
	}
}

func TestSponsoredSelfEscrowFinishUsesPreFeeBalanceAfterReserveRecycle(t *testing.T) {
	env, owner, _, issuer, sponsor, _ := newSponsoredIOUEnv(t, false)
	remaining := int32(1)
	require.Equal(t, "tesSUCCESS", setSponsorship(env, sponsor, owner, 0, &remaining).Code)

	sequence := env.Seq(owner)
	create := escrow.EscrowCreate(owner, owner, 0).
		IOUAmount(issuer.IOU("USD", 100)).
		FinishTime(env.Now().Add(time.Second)).
		Build()
	withReserveSponsor(create, sponsor)
	jtx.RequireTxSuccess(t, env.Submit(create))
	env.Close()

	clear := trustsettx.NewTrustSet(owner.Address, issuer.IOU("USD", 0))
	jtx.RequireTxSuccess(t, env.Submit(clear))
	env.Close()
	require.False(t, env.TrustLineExists(owner, issuer, "USD"))
	require.Equal(t, uint32(1), accountState(t, env, owner).OwnerCount)
	require.Equal(t, uint32(1), accountState(t, env, owner).SponsoredOwnerCount)

	// Reserve checks use the source's pre-fee balance after escrow recycling.
	// One drop above the one-owner reserve is sufficient.
	setAccountBalance(t, env, owner, env.ReserveBase()+env.ReserveIncrement()+1)
	result := env.Submit(escrow.EscrowFinish(owner, owner, sequence).Build())
	jtx.RequireTxSuccess(t, result)
	requireDeletedEscrowMetadata(t, result, sponsor.Address)
	requireCreatedMetadata(t, result, "RippleState")

	require.False(t, env.LedgerEntryExists(keylet.Escrow(owner.ID, sequence)))
	require.True(t, env.TrustLineExists(owner, issuer, "USD"))
	require.Equal(t, issuer.IOU("USD", 100), *env.IOUBalance(owner, issuer, "USD"))
	require.Equal(t, uint32(1), accountState(t, env, owner).OwnerCount)
	require.Zero(t, accountState(t, env, owner).SponsoredOwnerCount)
	require.Zero(t, accountState(t, env, sponsor).SponsoringOwnerCount)
	require.Zero(t, sponsorshipEntry(t, env, sponsor, owner).RemainingOwnerCount)
}
