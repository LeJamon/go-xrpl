package sponsor_test

import (
	"encoding/hex"
	"sort"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	accounttx "github.com/LeJamon/go-xrpl/internal/tx/account"
	checktx "github.com/LeJamon/go-xrpl/internal/tx/check"
	delegatetx "github.com/LeJamon/go-xrpl/internal/tx/delegate"
	paymenttx "github.com/LeJamon/go-xrpl/internal/tx/payment"
	signtx "github.com/LeJamon/go-xrpl/internal/tx/sign"
	sponsortx "github.com/LeJamon/go-xrpl/internal/tx/sponsor"
	trustsettx "github.com/LeJamon/go-xrpl/internal/tx/trustset"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func sponsorEnv(t *testing.T) (*jtx.TestEnv, *jtx.Account, *jtx.Account, *jtx.Account, *jtx.Account) {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.EnableFeature("Sponsor")
	sponsee := jtx.NewAccount("sponsee")
	destination := jtx.NewAccount("destination")
	sponsor1 := jtx.NewAccount("sponsor1")
	sponsor2 := jtx.NewAccount("sponsor2")
	env.FundAmount(sponsee, uint64(jtx.XRP(2_000)))
	env.FundAmount(destination, uint64(jtx.XRP(2_000)))
	env.FundAmount(sponsor1, uint64(jtx.XRP(2_000)))
	env.FundAmount(sponsor2, uint64(jtx.XRP(2_000)))
	env.Close()
	return env, sponsee, destination, sponsor1, sponsor2
}

func setSponsorship(
	env *jtx.TestEnv,
	sponsor, sponsee *jtx.Account,
	feeDeltaDrops int64,
	remainingDelta *int32,
) jtx.TxResult {
	txn := sponsortx.NewSponsorshipSet(sponsor.Address)
	txn.Sponsee = sponsee.Address
	if feeDeltaDrops != 0 {
		amount := tx.NewXRPAmount(feeDeltaDrops)
		txn.FeeAmountDelta = &amount
	}
	txn.RemainingOwnerCountDelta = remainingDelta
	return env.Submit(txn)
}

func setFeeSponsorship(
	env *jtx.TestEnv,
	sponsor, sponsee *jtx.Account,
	feeDrops, maxFeeDrops uint64,
) jtx.TxResult {
	txn := sponsortx.NewSponsorshipSet(sponsor.Address)
	txn.Sponsee = sponsee.Address
	feeAmount := tx.NewXRPAmount(int64(feeDrops))
	txn.FeeAmountDelta = &feeAmount
	if maxFeeDrops != 0 {
		maxFee := tx.NewXRPAmount(int64(maxFeeDrops))
		txn.MaxFee = &maxFee
	}
	return env.Submit(txn)
}

func grantDelegatePermission(
	t *testing.T,
	env *jtx.TestEnv,
	source, delegate *jtx.Account,
	permission string,
) {
	t.Helper()
	env.EnableFeature("PermissionDelegationV1_1")
	env.Close()
	transaction := delegatetx.NewDelegateSet(source.Address)
	transaction.Authorize = delegate.Address
	transaction.Permissions = []delegatetx.Permission{{
		Permission: delegatetx.PermissionData{PermissionValue: permission},
	}}
	require.Equal(t, "tesSUCCESS", env.SubmitSignedWith(transaction, source).Code)
	env.Close()
}

func signingPrivateKey(account *jtx.Account) string {
	prefix := "00"
	if account.IsEd25519() {
		prefix = "ED"
	}
	return prefix + account.PrivateKeyHex()
}

// attachSponsorSignature fills every ordinary signed field first, then signs
// the same STX projection as the transaction's top-level account.
func attachSponsorSignature(
	t *testing.T,
	env *jtx.TestEnv,
	transaction tx.Transaction,
	source, signer *jtx.Account,
) {
	t.Helper()
	attachSponsorSignatureFor(t, env, transaction, source, source, signer)
}

func attachSponsorSignatureFor(
	t *testing.T,
	env *jtx.TestEnv,
	transaction tx.Transaction,
	source, transactionSigner, sponsorSigner *jtx.Account,
) {
	t.Helper()
	common := transaction.GetCommon()
	if common.Sequence == nil && common.TicketSequence == nil {
		sequence := env.Seq(source)
		common.Sequence = &sequence
	}
	if common.Fee == "" {
		common.Fee = "10"
	}
	common.SigningPubKey = transactionSigner.PublicKeyHex()
	signature, err := signtx.SignSponsor(
		transaction,
		sponsorSigner.PublicKeyHex(),
		signingPrivateKey(sponsorSigner),
	)
	require.NoError(t, err)
	common.SponsorSignature = signature
}

func attachSponsorMultiSignature(
	t *testing.T,
	env *jtx.TestEnv,
	transaction tx.Transaction,
	source *jtx.Account,
	signers ...*jtx.Account,
) {
	t.Helper()
	common := transaction.GetCommon()
	if common.Sequence == nil && common.TicketSequence == nil {
		sequence := env.Seq(source)
		common.Sequence = &sequence
	}
	common.SigningPubKey = source.PublicKeyHex()

	wrappers := make([]tx.SignerWrapper, 0, len(signers))
	for _, signer := range signers {
		signature, err := signtx.SignTransactionForMultiSignTarget(
			transaction,
			signer.Address,
			signingPrivateKey(signer),
		)
		require.NoError(t, err)
		wrappers = append(wrappers, tx.SignerWrapper{Signer: tx.Signer{
			Account:       signer.Address,
			SigningPubKey: signer.PublicKeyHex(),
			TxnSignature:  signature,
		}})
	}
	sort.Slice(wrappers, func(i, j int) bool {
		left, leftErr := state.DecodeAccountID(wrappers[i].Signer.Account)
		right, rightErr := state.DecodeAccountID(wrappers[j].Signer.Account)
		require.NoError(t, leftErr)
		require.NoError(t, rightErr)
		return string(left[:]) < string(right[:])
	})
	common.SponsorSignature = &tx.SponsorSignature{Signers: wrappers}
}

func sponsorshipEntry(t *testing.T, env *jtx.TestEnv, sponsor, sponsee *jtx.Account) *state.SponsorshipData {
	t.Helper()
	data, err := env.LedgerEntry(keylet.Sponsorship(sponsor.ID, sponsee.ID))
	require.NoError(t, err)
	require.NotNil(t, data)
	entry, err := state.ParseSponsorship(data)
	require.NoError(t, err)
	return entry
}

func objectSponsor(t *testing.T, env *jtx.TestEnv, object keylet.Keylet) (string, bool) {
	t.Helper()
	data, err := env.LedgerEntry(object)
	require.NoError(t, err)
	fields, err := binarycodec.DecodeBytes(data)
	require.NoError(t, err)
	sponsor, ok := fields["Sponsor"].(string)
	return sponsor, ok
}

func transferObject(env *jtx.TestEnv, account *jtx.Account, object keylet.Keylet, sponsor *jtx.Account, operation uint32) jtx.TxResult {
	txn := sponsortx.NewSponsorshipTransfer(account.Address)
	txn.ObjectID = hex.EncodeToString(object.Key[:])
	txn.SetFlags(operation)
	if sponsor != nil {
		txn.Sponsor = sponsor.Address
		flags := tx.SpfSponsorReserve
		txn.SponsorFlags = &flags
	}
	return env.Submit(txn)
}

func accountState(t *testing.T, env *jtx.TestEnv, account *jtx.Account) *state.AccountRoot {
	t.Helper()
	data, err := env.LedgerEntry(keylet.Account(account.ID))
	require.NoError(t, err)
	require.NotNil(t, data)
	root, err := state.ParseAccountRoot(data)
	require.NoError(t, err)
	return root
}

func setAccountBalance(t *testing.T, env *jtx.TestEnv, account *jtx.Account, balance uint64) {
	t.Helper()
	root := accountState(t, env, account)
	root.Balance = balance
	data, err := state.SerializeAccountRoot(root)
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Update(keylet.Account(account.ID), data))
}

func setPseudoAccount(t *testing.T, env *jtx.TestEnv, account *jtx.Account) {
	t.Helper()
	root := accountState(t, env, account)
	root.AMMID[0] = 1
	data, err := state.SerializeAccountRoot(root)
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Update(keylet.Account(account.ID), data))
}

func requireAffectedNodes(t *testing.T, result jtx.TxResult, want ...string) {
	t.Helper()
	require.NotNil(t, result.Metadata)
	got := make([]string, 0, len(result.Metadata.AffectedNodes))
	for _, node := range result.Metadata.AffectedNodes {
		got = append(got, node.NodeType+"/"+node.LedgerEntryType)
	}
	sort.Strings(got)
	sort.Strings(want)
	require.Equal(t, want, got)
}

func ownerDirectoryContains(t *testing.T, env *jtx.TestEnv, owner *jtx.Account, object [32]byte) bool {
	t.Helper()
	found := false
	err := state.DirForEach(env.Ledger(), keylet.OwnerDir(owner.ID), func(item [32]byte) error {
		if item == object {
			found = true
		}
		return nil
	})
	require.NoError(t, err)
	return found
}

func TestSponsorshipSetCreateUpdateDelete(t *testing.T) {
	env, sponsee, _, sponsor, _ := sponsorEnv(t)

	initialBalance := env.Balance(sponsor)
	remaining := int32(3)
	created := setSponsorship(env, sponsor, sponsee, 1_000, &remaining)
	require.Equal(t, "tesSUCCESS", created.Code)
	requireAffectedNodes(t, created,
		"CreatedNode/DirectoryNode",
		"CreatedNode/DirectoryNode",
		"CreatedNode/Sponsorship",
		"ModifiedNode/AccountRoot",
	)
	sponsorshipKey := keylet.Sponsorship(sponsor.ID, sponsee.ID)
	require.True(t, ownerDirectoryContains(t, env, sponsor, sponsorshipKey.Key))
	require.True(t, ownerDirectoryContains(t, env, sponsee, sponsorshipKey.Key))

	entry := sponsorshipEntry(t, env, sponsor, sponsee)
	require.True(t, entry.HasFeeAmount)
	require.Equal(t, uint64(1_000), entry.FeeAmount)
	require.Equal(t, uint32(3), entry.RemainingOwnerCount)
	require.Equal(t, uint32(1), env.OwnerCount(sponsor))
	require.Equal(t, initialBalance-1_000-10, env.Balance(sponsor))

	updatedFee := tx.NewXRPAmount(-600)
	updatedRemaining := int32(-1)
	update := sponsortx.NewSponsorshipSet(sponsor.Address)
	update.Sponsee = sponsee.Address
	update.FeeAmountDelta = &updatedFee
	update.RemainingOwnerCountDelta = &updatedRemaining
	update.SetFlags(sponsortx.SponsorshipSetFlagRequireSignForReserve)
	updated := env.Submit(update)
	require.Equal(t, "tesSUCCESS", updated.Code)
	requireAffectedNodes(t, updated,
		"ModifiedNode/AccountRoot",
		"ModifiedNode/Sponsorship",
	)

	entry = sponsorshipEntry(t, env, sponsor, sponsee)
	require.Equal(t, uint64(400), entry.FeeAmount)
	require.Equal(t, uint32(2), entry.RemainingOwnerCount)
	require.NotZero(t, entry.Flags)
	require.Equal(t, initialBalance-400-20, env.Balance(sponsor))

	remove := sponsortx.NewSponsorshipSet(sponsee.Address)
	remove.CounterpartySponsor = sponsor.Address
	remove.SetFlags(sponsortx.SponsorshipSetFlagDelete)
	deleted := env.Submit(remove)
	require.Equal(t, "tesSUCCESS", deleted.Code)
	requireAffectedNodes(t, deleted,
		"DeletedNode/DirectoryNode",
		"DeletedNode/DirectoryNode",
		"DeletedNode/Sponsorship",
		"ModifiedNode/AccountRoot",
		"ModifiedNode/AccountRoot",
	)
	require.False(t, env.LedgerEntryExists(keylet.Sponsorship(sponsor.ID, sponsee.ID)))
	require.False(t, ownerDirectoryContains(t, env, sponsor, sponsorshipKey.Key))
	require.False(t, ownerDirectoryContains(t, env, sponsee, sponsorshipKey.Key))
	require.Equal(t, uint32(0), env.OwnerCount(sponsor))
	require.Equal(t, initialBalance-20, env.Balance(sponsor), "sponsee pays the delete fee; prefund is refunded")
}

func TestSponsorshipSetDeltaBounds(t *testing.T) {
	const maxInt32 = int32(1<<31 - 1)

	t.Run("count overflow is rejected", func(t *testing.T) {
		env, sponsee, _, sponsor, _ := sponsorEnv(t)
		max := maxInt32
		require.Equal(t, "tesSUCCESS", setSponsorship(env, sponsor, sponsee, 0, &max).Code)
		require.Equal(t, uint32(maxInt32), sponsorshipEntry(t, env, sponsor, sponsee).RemainingOwnerCount)

		require.Equal(t, "tesSUCCESS", setSponsorship(env, sponsor, sponsee, 0, &max).Code)
		require.Equal(t, uint32(maxInt32)*2, sponsorshipEntry(t, env, sponsor, sponsee).RemainingOwnerCount)

		overflow := int32(2)
		require.Equal(t, "tecLIMIT_EXCEEDED", setSponsorship(env, sponsor, sponsee, 0, &overflow).Code)
		require.Equal(t, uint32(maxInt32)*2, sponsorshipEntry(t, env, sponsor, sponsee).RemainingOwnerCount)
	})

	t.Run("count underflow clamps when fee budget remains", func(t *testing.T) {
		env, sponsee, _, sponsor, _ := sponsorEnv(t)
		remaining := int32(10)
		require.Equal(t, "tesSUCCESS", setSponsorship(env, sponsor, sponsee, 100, &remaining).Code)

		underflow := int32(-20)
		require.Equal(t, "tesSUCCESS", setSponsorship(env, sponsor, sponsee, 0, &underflow).Code)
		entry := sponsorshipEntry(t, env, sponsor, sponsee)
		require.Equal(t, uint32(0), entry.RemainingOwnerCount)
		require.True(t, entry.HasFeeAmount)
		require.Equal(t, uint64(100), entry.FeeAmount)
	})

	t.Run("count underflow is rejected when budget empties", func(t *testing.T) {
		env, sponsee, _, sponsor, _ := sponsorEnv(t)
		remaining := int32(10)
		require.Equal(t, "tesSUCCESS", setSponsorship(env, sponsor, sponsee, 0, &remaining).Code)

		underflow := int32(-20)
		require.Equal(t, "tecNO_PERMISSION", setSponsorship(env, sponsor, sponsee, 0, &underflow).Code)
		require.Equal(t, uint32(10), sponsorshipEntry(t, env, sponsor, sponsee).RemainingOwnerCount)
	})
}

func TestSponsorshipSetPreclaimMatrix(t *testing.T) {
	env, sponsee, _, sponsor, _ := sponsorEnv(t)
	missing := jtx.NewAccount("missing-sponsor-party")
	remaining := int32(1)

	require.Equal(t, "tecNO_DST", setSponsorship(env, sponsor, missing, 0, &remaining).Code)

	missingSponsor := sponsortx.NewSponsorshipSet(sponsee.Address)
	missingSponsor.CounterpartySponsor = missing.Address
	missingSponsor.SetFlags(sponsortx.SponsorshipSetFlagDelete)
	require.Equal(t, "tecNO_DST", env.Submit(missingSponsor).Code)

	missingObject := sponsortx.NewSponsorshipSet(sponsor.Address)
	missingObject.Sponsee = sponsee.Address
	missingObject.SetFlags(sponsortx.SponsorshipSetFlagDelete)
	require.Equal(t, "tecNO_ENTRY", env.Submit(missingObject).Code)

	require.Equal(t, "temREDUNDANT", setSponsorship(env, sponsor, sponsee, 0, nil).Code)
	require.False(t, env.LedgerEntryExists(keylet.Sponsorship(sponsor.ID, sponsee.ID)))

	setPseudoAccount(t, env, sponsee)
	require.Equal(t, "tecPSEUDO_ACCOUNT", setSponsorship(env, sponsor, sponsee, 0, &remaining).Code)
	require.False(t, env.LedgerEntryExists(keylet.Sponsorship(sponsor.ID, sponsee.ID)))

	pseudoSponsorEnv, ordinarySponsee, _, pseudoSponsor, _ := sponsorEnv(t)
	setPseudoAccount(t, pseudoSponsorEnv, pseudoSponsor)
	deleteFromSponsee := sponsortx.NewSponsorshipSet(ordinarySponsee.Address)
	deleteFromSponsee.CounterpartySponsor = pseudoSponsor.Address
	deleteFromSponsee.SetFlags(sponsortx.SponsorshipSetFlagDelete)
	require.Equal(t, "tecPSEUDO_ACCOUNT", pseudoSponsorEnv.Submit(deleteFromSponsee).Code)
	require.False(t, pseudoSponsorEnv.LedgerEntryExists(keylet.Sponsorship(pseudoSponsor.ID, ordinarySponsee.ID)))
}

func TestSponsorshipTransferObjectCreateReassignEnd(t *testing.T) {
	env, sponsee, destination, sponsor1, sponsor2 := sponsorEnv(t)

	checkSequence := env.Seq(sponsee)
	createCheck := checktx.NewCheckCreate(sponsee.Address, destination.Address, tx.NewXRPAmount(jtx.XRP(1)))
	require.Equal(t, "tesSUCCESS", env.Submit(createCheck).Code)
	checkKey := keylet.Check(sponsee.ID, checkSequence)

	remaining1 := int32(2)
	remaining2 := int32(2)
	require.Equal(t, "tesSUCCESS", setSponsorship(env, sponsor1, sponsee, 0, &remaining1).Code)
	require.Equal(t, "tesSUCCESS", setSponsorship(env, sponsor2, sponsee, 0, &remaining2).Code)

	created := transferObject(env, sponsee, checkKey, sponsor1, sponsortx.SponsorshipTransferFlagCreate)
	require.Equal(t, "tesSUCCESS", created.Code)
	requireAffectedNodes(t, created,
		"ModifiedNode/AccountRoot",
		"ModifiedNode/AccountRoot",
		"ModifiedNode/Check",
		"ModifiedNode/Sponsorship",
	)
	gotSponsor, present := objectSponsor(t, env, checkKey)
	require.True(t, present)
	require.Equal(t, sponsor1.Address, gotSponsor)
	require.Equal(t, uint32(1), accountState(t, env, sponsee).SponsoredOwnerCount)
	require.Equal(t, uint32(1), accountState(t, env, sponsor1).SponsoringOwnerCount)
	require.Equal(t, uint32(1), sponsorshipEntry(t, env, sponsor1, sponsee).RemainingOwnerCount)

	sameSponsor := transferObject(env, sponsee, checkKey, sponsor1, sponsortx.SponsorshipTransferFlagReassign)
	require.Equal(t, "tecNO_PERMISSION", sameSponsor.Code)
	require.Equal(t, uint32(1), accountState(t, env, sponsor1).SponsoringOwnerCount)
	require.Equal(t, uint32(1), sponsorshipEntry(t, env, sponsor1, sponsee).RemainingOwnerCount,
		"reassigning to the current sponsor must not consume prefunded reserve capacity")

	reassigned := transferObject(env, sponsee, checkKey, sponsor2, sponsortx.SponsorshipTransferFlagReassign)
	require.Equal(t, "tesSUCCESS", reassigned.Code)
	requireAffectedNodes(t, reassigned,
		"ModifiedNode/AccountRoot",
		"ModifiedNode/AccountRoot",
		"ModifiedNode/AccountRoot",
		"ModifiedNode/Check",
		"ModifiedNode/Sponsorship",
	)
	gotSponsor, present = objectSponsor(t, env, checkKey)
	require.True(t, present)
	require.Equal(t, sponsor2.Address, gotSponsor)
	require.Equal(t, uint32(1), accountState(t, env, sponsee).SponsoredOwnerCount)
	require.Equal(t, uint32(0), accountState(t, env, sponsor1).SponsoringOwnerCount)
	require.Equal(t, uint32(1), accountState(t, env, sponsor2).SponsoringOwnerCount)
	require.Equal(t, uint32(1), sponsorshipEntry(t, env, sponsor2, sponsee).RemainingOwnerCount)

	setAccountBalance(t, env, sponsee, env.ReserveBase()-1)

	end := sponsortx.NewSponsorshipTransfer(sponsor2.Address)
	end.Sponsee = sponsee.Address
	end.ObjectID = hex.EncodeToString(checkKey.Key[:])
	end.SetFlags(sponsortx.SponsorshipTransferFlagEnd)
	ended := env.Submit(end)
	require.Equal(t, "tesSUCCESS", ended.Code)
	requireAffectedNodes(t, ended,
		"ModifiedNode/AccountRoot",
		"ModifiedNode/AccountRoot",
		"ModifiedNode/Check",
	)
	_, present = objectSponsor(t, env, checkKey)
	require.False(t, present)
	require.Equal(t, uint32(0), accountState(t, env, sponsee).SponsoredOwnerCount)
	require.Equal(t, uint32(0), accountState(t, env, sponsor2).SponsoringOwnerCount)
	require.Equal(t, uint32(1), sponsorshipEntry(t, env, sponsor2, sponsee).RemainingOwnerCount,
		"ending sponsorship does not refund prefunded reserve capacity")
}

func TestSponsoredCheckCreateAndDeleteLifecycle(t *testing.T) {
	env, sponsee, destination, sponsor, _ := sponsorEnv(t)
	remaining := int32(1)
	require.Equal(t, "tesSUCCESS", setSponsorship(env, sponsor, sponsee, 0, &remaining).Code)

	sequence := env.Seq(sponsee)
	create := checktx.NewCheckCreate(sponsee.Address, destination.Address, tx.NewXRPAmount(jtx.XRP(1)))
	create.Sponsor = sponsor.Address
	reserve := tx.SpfSponsorReserve
	create.SponsorFlags = &reserve
	require.Equal(t, "tesSUCCESS", env.Submit(create).Code)

	checkKey := keylet.Check(sponsee.ID, sequence)
	gotSponsor, present := objectSponsor(t, env, checkKey)
	require.True(t, present)
	require.Equal(t, sponsor.Address, gotSponsor)
	require.Equal(t, uint32(1), accountState(t, env, sponsee).OwnerCount)
	require.Equal(t, uint32(1), accountState(t, env, sponsee).SponsoredOwnerCount)
	require.Equal(t, uint32(1), accountState(t, env, sponsor).SponsoringOwnerCount)
	require.Zero(t, sponsorshipEntry(t, env, sponsor, sponsee).RemainingOwnerCount)

	cancel := checktx.NewCheckCancel(destination.Address, hex.EncodeToString(checkKey.Key[:]))
	require.Equal(t, "tesSUCCESS", env.Submit(cancel).Code)
	require.False(t, env.LedgerEntryExists(checkKey))
	require.Zero(t, accountState(t, env, sponsee).OwnerCount)
	require.Zero(t, accountState(t, env, sponsee).SponsoredOwnerCount)
	require.Zero(t, accountState(t, env, sponsor).SponsoringOwnerCount)
	require.Zero(t, sponsorshipEntry(t, env, sponsor, sponsee).RemainingOwnerCount,
		"deleting a sponsored object must not restore prefunded capacity")
}

func TestSponsoredTrustLineCreateAndDeleteLifecycle(t *testing.T) {
	env, sponsee, issuer, sponsor, _ := sponsorEnv(t)
	remaining := int32(1)
	require.Equal(t, "tesSUCCESS", setSponsorship(env, sponsor, sponsee, 0, &remaining).Code)

	limit := tx.NewIssuedAmountFromFloat64(100, "USD", issuer.Address)
	create := trustsettx.NewTrustSet(sponsee.Address, limit)
	create.Sponsor = sponsor.Address
	reserve := tx.SpfSponsorReserve
	create.SponsorFlags = &reserve
	require.Equal(t, "tesSUCCESS", env.Submit(create).Code)

	lineKey := keylet.Line(sponsee.ID, issuer.ID, "USD")
	lineData, err := env.LedgerEntry(lineKey)
	require.NoError(t, err)
	line, err := state.ParseRippleState(lineData)
	require.NoError(t, err)
	if state.CompareAccountIDs(sponsee.ID, issuer.ID) < 0 {
		require.Equal(t, sponsor.Address, line.LowSponsor)
	} else {
		require.Equal(t, sponsor.Address, line.HighSponsor)
	}
	require.Equal(t, uint32(1), accountState(t, env, sponsee).OwnerCount)
	require.Equal(t, uint32(1), accountState(t, env, sponsee).SponsoredOwnerCount)
	require.Equal(t, uint32(1), accountState(t, env, sponsor).SponsoringOwnerCount)

	clear := trustsettx.NewTrustSet(sponsee.Address, tx.NewIssuedAmountFromFloat64(0, "USD", issuer.Address))
	require.Equal(t, "tesSUCCESS", env.Submit(clear).Code)
	require.False(t, env.LedgerEntryExists(lineKey))
	require.Zero(t, accountState(t, env, sponsee).OwnerCount)
	require.Zero(t, accountState(t, env, sponsee).SponsoredOwnerCount)
	require.Zero(t, accountState(t, env, sponsor).SponsoringOwnerCount)
	require.Zero(t, sponsorshipEntry(t, env, sponsor, sponsee).RemainingOwnerCount)
}

func TestSponsorshipTransferCosignedObjectReserveBoundary(t *testing.T) {
	env, sponsee, destination, sponsor, _ := sponsorEnv(t)
	checkSequence := env.Seq(sponsee)
	require.Equal(t, "tesSUCCESS", env.Submit(checktx.NewCheckCreate(
		sponsee.Address, destination.Address, tx.NewXRPAmount(jtx.XRP(1)))).Code)
	checkKey := keylet.Check(sponsee.ID, checkSequence)

	transfer := func() *sponsortx.SponsorshipTransfer {
		txn := sponsortx.NewSponsorshipTransfer(sponsee.Address)
		txn.ObjectID = hex.EncodeToString(checkKey.Key[:])
		txn.SetFlags(sponsortx.SponsorshipTransferFlagCreate)
		txn.Sponsor = sponsor.Address
		reserve := tx.SpfSponsorReserve
		txn.SponsorFlags = &reserve
		txn.SponsorSignature = &tx.SponsorSignature{}
		return txn
	}

	required := env.ReserveBase() + env.ReserveIncrement()
	setAccountBalance(t, env, sponsor, required-1)
	require.Equal(t, "tecINSUFFICIENT_RESERVE", env.Submit(transfer()).Code)
	_, present := objectSponsor(t, env, checkKey)
	require.False(t, present)
	require.Zero(t, accountState(t, env, sponsor).SponsoringOwnerCount)

	setAccountBalance(t, env, sponsor, required)
	require.Equal(t, "tesSUCCESS", env.Submit(transfer()).Code)
	gotSponsor, present := objectSponsor(t, env, checkKey)
	require.True(t, present)
	require.Equal(t, sponsor.Address, gotSponsor)
	require.Equal(t, uint32(1), accountState(t, env, sponsor).SponsoringOwnerCount)
}

func TestSponsorshipTransferAccountCreateReassignEnd(t *testing.T) {
	env, sponsee, _, sponsor1, sponsor2 := sponsorEnv(t)

	create := sponsortx.NewSponsorshipTransfer(sponsee.Address)
	create.SetFlags(sponsortx.SponsorshipTransferFlagCreate)
	create.Sponsor = sponsor1.Address
	reserve := tx.SpfSponsorReserve
	create.SponsorFlags = &reserve
	create.SponsorSignature = &tx.SponsorSignature{}
	created := env.Submit(create)
	require.Equal(t, "tesSUCCESS", created.Code)
	requireAffectedNodes(t, created,
		"ModifiedNode/AccountRoot",
		"ModifiedNode/AccountRoot",
	)
	require.True(t, accountState(t, env, sponsee).HasSponsor)
	require.Equal(t, sponsor1.Address, accountState(t, env, sponsee).Sponsor)
	require.Equal(t, uint32(1), accountState(t, env, sponsor1).SponsoringAccountCount)

	reassign := sponsortx.NewSponsorshipTransfer(sponsee.Address)
	reassign.SetFlags(sponsortx.SponsorshipTransferFlagReassign)
	reassign.Sponsor = sponsor2.Address
	reassign.SponsorFlags = &reserve
	reassign.SponsorSignature = &tx.SponsorSignature{}
	reassigned := env.Submit(reassign)
	require.Equal(t, "tesSUCCESS", reassigned.Code)
	requireAffectedNodes(t, reassigned,
		"ModifiedNode/AccountRoot",
		"ModifiedNode/AccountRoot",
		"ModifiedNode/AccountRoot",
	)
	require.Equal(t, sponsor2.Address, accountState(t, env, sponsee).Sponsor)
	require.Equal(t, uint32(0), accountState(t, env, sponsor1).SponsoringAccountCount)
	require.Equal(t, uint32(1), accountState(t, env, sponsor2).SponsoringAccountCount)

	sameSponsor := sponsortx.NewSponsorshipTransfer(sponsee.Address)
	sameSponsor.SetFlags(sponsortx.SponsorshipTransferFlagReassign)
	sameSponsor.Sponsor = sponsor2.Address
	sameSponsor.SponsorFlags = &reserve
	sameSponsor.SponsorSignature = &tx.SponsorSignature{}
	require.Equal(t, "tecNO_PERMISSION", env.Submit(sameSponsor).Code)
	require.Equal(t, uint32(1), accountState(t, env, sponsor2).SponsoringAccountCount)

	setAccountBalance(t, env, sponsee, env.ReserveBase()-1)
	insufficientEnd := sponsortx.NewSponsorshipTransfer(sponsee.Address)
	insufficientEnd.SetFlags(sponsortx.SponsorshipTransferFlagEnd)
	require.Equal(t, "tecINSUFFICIENT_RESERVE", env.Submit(insufficientEnd).Code)
	require.True(t, accountState(t, env, sponsee).HasSponsor)
	require.Equal(t, uint32(1), accountState(t, env, sponsor2).SponsoringAccountCount)

	setAccountBalance(t, env, sponsee, env.ReserveBase())
	end := sponsortx.NewSponsorshipTransfer(sponsee.Address)
	end.SetFlags(sponsortx.SponsorshipTransferFlagEnd)
	ended := env.Submit(end)
	require.Equal(t, "tesSUCCESS", ended.Code)
	requireAffectedNodes(t, ended,
		"ModifiedNode/AccountRoot",
		"ModifiedNode/AccountRoot",
	)
	require.False(t, accountState(t, env, sponsee).HasSponsor)
	require.Empty(t, accountState(t, env, sponsee).Sponsor)
	require.Equal(t, uint32(0), accountState(t, env, sponsor2).SponsoringAccountCount)
}

func TestSponsorshipTransferAccountSponsorReserveBoundary(t *testing.T) {
	env, sponsee, _, sponsor, _ := sponsorEnv(t)
	reserveFlag := tx.SpfSponsorReserve
	transfer := func() *sponsortx.SponsorshipTransfer {
		txn := sponsortx.NewSponsorshipTransfer(sponsee.Address)
		txn.SetFlags(sponsortx.SponsorshipTransferFlagCreate)
		txn.Sponsor = sponsor.Address
		txn.SponsorFlags = &reserveFlag
		txn.SponsorSignature = &tx.SponsorSignature{}
		return txn
	}

	// The sponsor must fund its own base reserve plus the sponsored account's
	// base reserve. No owner-reserve increment is involved.
	required := 2 * env.ReserveBase()
	setAccountBalance(t, env, sponsor, required-1)
	require.Equal(t, "tecINSUFFICIENT_RESERVE", env.Submit(transfer()).Code)
	require.False(t, accountState(t, env, sponsee).HasSponsor)
	require.Zero(t, accountState(t, env, sponsor).SponsoringAccountCount)

	setAccountBalance(t, env, sponsor, required)
	require.Equal(t, "tesSUCCESS", env.Submit(transfer()).Code)
	require.Equal(t, sponsor.Address, accountState(t, env, sponsee).Sponsor)
	require.Equal(t, uint32(1), accountState(t, env, sponsor).SponsoringAccountCount)
}

func TestSponsorshipTransferAuthorizationLimitAndRollback(t *testing.T) {
	env, sponsee, destination, sponsor, _ := sponsorEnv(t)
	checkSequence := env.Seq(sponsee)
	require.Equal(t, "tesSUCCESS", env.Submit(checktx.NewCheckCreate(
		sponsee.Address, destination.Address, tx.NewXRPAmount(jtx.XRP(1)))).Code)
	checkKey := keylet.Check(sponsee.ID, checkSequence)

	unauthorized := transferObject(env, sponsee, checkKey, sponsor, sponsortx.SponsorshipTransferFlagCreate)
	require.Equal(t, "terNO_PERMISSION", unauthorized.Code)

	// A fee-only budget makes the relationship usable for fee sponsorship but
	// provides zero object-reserve units. The transfer must claim only its fee
	// and leave every lifecycle field untouched.
	require.Equal(t, "tesSUCCESS", setSponsorship(env, sponsor, sponsee, 1_000, nil).Code)
	ownerBefore := accountState(t, env, sponsee)
	sponsorBefore := accountState(t, env, sponsor)
	limited := transferObject(env, sponsee, checkKey, sponsor, sponsortx.SponsorshipTransferFlagCreate)
	require.Equal(t, "tecINSUFFICIENT_RESERVE", limited.Code)
	_, present := objectSponsor(t, env, checkKey)
	require.False(t, present)
	require.Equal(t, ownerBefore.SponsoredOwnerCount, accountState(t, env, sponsee).SponsoredOwnerCount)
	require.Equal(t, sponsorBefore.SponsoringOwnerCount, accountState(t, env, sponsor).SponsoringOwnerCount)
	require.Equal(t, uint32(0), sponsorshipEntry(t, env, sponsor, sponsee).RemainingOwnerCount)

	remaining := int32(1)
	requireSignature := sponsortx.NewSponsorshipSet(sponsor.Address)
	requireSignature.Sponsee = sponsee.Address
	requireSignature.RemainingOwnerCountDelta = &remaining
	requireSignature.SetFlags(sponsortx.SponsorshipSetFlagRequireSignForReserve)
	require.Equal(t, "tesSUCCESS", env.Submit(requireSignature).Code)

	unsigned := transferObject(env, sponsee, checkKey, sponsor, sponsortx.SponsorshipTransferFlagCreate)
	require.Equal(t, "terNO_PERMISSION", unsigned.Code)
	require.Equal(t, uint32(1), sponsorshipEntry(t, env, sponsor, sponsee).RemainingOwnerCount)

	signed := sponsortx.NewSponsorshipTransfer(sponsee.Address)
	signed.ObjectID = hex.EncodeToString(checkKey.Key[:])
	signed.SetFlags(sponsortx.SponsorshipTransferFlagCreate)
	signed.Sponsor = sponsor.Address
	reserveFlag := tx.SpfSponsorReserve
	signed.SponsorFlags = &reserveFlag
	signed.SponsorSignature = &tx.SponsorSignature{}
	require.Equal(t, "tesSUCCESS", env.Submit(signed).Code)
	require.Equal(t, uint32(0), sponsorshipEntry(t, env, sponsor, sponsee).RemainingOwnerCount)
}

func TestSponsorSingleSignatureAuthorizationAndReplayProtection(t *testing.T) {
	t.Run("master key", func(t *testing.T) {
		env, source, _, sponsor, _ := sponsorEnv(t)
		sourceBefore := env.Balance(source)
		sponsorBefore := env.Balance(sponsor)

		transaction := accounttx.NewAccountSet(source.Address)
		transaction.Fee = "10"
		transaction.Sponsor = sponsor.Address
		flags := tx.SpfSponsorFee
		transaction.SponsorFlags = &flags
		attachSponsorSignature(t, env, transaction, source, sponsor)

		result := env.SubmitSigned(transaction)
		require.Equal(t, "tesSUCCESS", result.Code)
		require.Equal(t, sourceBefore, env.Balance(source))
		require.Equal(t, sponsorBefore-10, env.Balance(sponsor))
	})

	t.Run("stale after mutation", func(t *testing.T) {
		env, source, _, sponsor, _ := sponsorEnv(t)
		sourceBefore := env.Balance(source)
		sponsorBefore := env.Balance(sponsor)
		sequenceBefore := env.Seq(source)

		transaction := accounttx.NewAccountSet(source.Address)
		transaction.Fee = "10"
		transaction.Sponsor = sponsor.Address
		flags := tx.SpfSponsorFee
		transaction.SponsorFlags = &flags
		attachSponsorSignature(t, env, transaction, source, sponsor)
		transaction.Fee = "11"

		result := env.SubmitSigned(transaction)
		require.Equal(t, "temINVALID", result.Code)
		require.Equal(t, sequenceBefore, env.Seq(source))
		require.Equal(t, sourceBefore, env.Balance(source))
		require.Equal(t, sponsorBefore, env.Balance(sponsor))
	})

	t.Run("unauthorized key", func(t *testing.T) {
		env, source, _, sponsor, wrongSigner := sponsorEnv(t)
		sourceBefore := env.Balance(source)
		sponsorBefore := env.Balance(sponsor)
		sequenceBefore := env.Seq(source)

		transaction := accounttx.NewAccountSet(source.Address)
		transaction.Fee = "10"
		transaction.Sponsor = sponsor.Address
		flags := tx.SpfSponsorFee
		transaction.SponsorFlags = &flags
		attachSponsorSignature(t, env, transaction, source, wrongSigner)

		result := env.SubmitSigned(transaction)
		require.Equal(t, "tefBAD_AUTH", result.Code)
		require.Equal(t, sequenceBefore, env.Seq(source))
		require.Equal(t, sourceBefore, env.Balance(source))
		require.Equal(t, sponsorBefore, env.Balance(sponsor))
	})

	t.Run("regular key and disabled master", func(t *testing.T) {
		env, source, _, sponsor, regularKey := sponsorEnv(t)
		env.SetRegularKey(sponsor, regularKey)
		env.DisableMasterKey(sponsor)

		masterSigned := accounttx.NewAccountSet(source.Address)
		masterSigned.Fee = "10"
		masterSigned.Sponsor = sponsor.Address
		flags := tx.SpfSponsorFee
		masterSigned.SponsorFlags = &flags
		attachSponsorSignature(t, env, masterSigned, source, sponsor)
		sponsorBefore := env.Balance(sponsor)

		result := env.SubmitSigned(masterSigned)
		require.Equal(t, "tefMASTER_DISABLED", result.Code)
		require.Equal(t, sponsorBefore, env.Balance(sponsor))

		regularSigned := accounttx.NewAccountSet(source.Address)
		regularSigned.Fee = "10"
		regularSigned.Sponsor = sponsor.Address
		regularSigned.SponsorFlags = &flags
		attachSponsorSignature(t, env, regularSigned, source, regularKey)

		result = env.SubmitSigned(regularSigned)
		require.Equal(t, "tesSUCCESS", result.Code)
		require.Equal(t, sponsorBefore-10, env.Balance(sponsor))
	})
}

func TestSponsorAuthorizationFailuresNeverDebit(t *testing.T) {
	t.Run("missing relationship and signature", func(t *testing.T) {
		env, source, _, sponsor, _ := sponsorEnv(t)
		sourceBefore := env.Balance(source)
		sponsorBefore := env.Balance(sponsor)
		sequenceBefore := env.Seq(source)

		transaction := accounttx.NewAccountSet(source.Address)
		transaction.Fee = "10"
		transaction.Sponsor = sponsor.Address
		flags := tx.SpfSponsorFee
		transaction.SponsorFlags = &flags

		require.Equal(t, "terNO_PERMISSION", env.Submit(transaction).Code)
		require.Equal(t, sequenceBefore, env.Seq(source))
		require.Equal(t, sourceBefore, env.Balance(source))
		require.Equal(t, sponsorBefore, env.Balance(sponsor))
	})

	t.Run("missing sponsor account", func(t *testing.T) {
		env, source, _, _, _ := sponsorEnv(t)
		missingSponsor := jtx.NewAccount("missing-fee-sponsor")
		sourceBefore := env.Balance(source)
		sequenceBefore := env.Seq(source)

		transaction := accounttx.NewAccountSet(source.Address)
		transaction.Fee = "10"
		transaction.Sponsor = missingSponsor.Address
		flags := tx.SpfSponsorFee
		transaction.SponsorFlags = &flags
		transaction.SponsorSignature = &tx.SponsorSignature{}

		require.Equal(t, "terNO_ACCOUNT", env.Submit(transaction).Code)
		require.Equal(t, sequenceBefore, env.Seq(source))
		require.Equal(t, sourceBefore, env.Balance(source))
	})
}

func TestSponsorSignatureStructuralFailures(t *testing.T) {
	env, source, _, sponsor, _ := sponsorEnv(t)
	signer1 := jtx.NewAccount("sponsor-structure-signer-1")
	signer2 := jtx.NewAccount("sponsor-structure-signer-2")
	flags := tx.SpfSponsorFee

	sorted := []tx.SignerWrapper{
		{Signer: tx.Signer{Account: signer1.Address}},
		{Signer: tx.Signer{Account: signer2.Address}},
	}
	sort.Slice(sorted, func(i, j int) bool {
		left, _ := state.DecodeAccountID(sorted[i].Signer.Account)
		right, _ := state.DecodeAccountID(sorted[j].Signer.Account)
		return string(left[:]) < string(right[:])
	})
	unsorted := append([]tx.SignerWrapper(nil), sorted...)
	unsorted[0], unsorted[1] = unsorted[1], unsorted[0]

	testCases := []struct {
		name      string
		signature *tx.SponsorSignature
	}{
		{
			name: "single and multi",
			signature: &tx.SponsorSignature{
				SigningPubKey: sponsor.PublicKeyHex(),
				TxnSignature:  "AA",
				Signers:       sorted,
			},
		},
		{
			name:      "unsorted multisigners",
			signature: &tx.SponsorSignature{Signers: unsorted},
		},
		{
			name:      "duplicate multisigners",
			signature: &tx.SponsorSignature{Signers: []tx.SignerWrapper{sorted[0], sorted[0]}},
		},
		{
			name:      "signature without key",
			signature: &tx.SponsorSignature{TxnSignature: "AA"},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			sourceBefore := env.Balance(source)
			sponsorBefore := env.Balance(sponsor)
			sequenceBefore := env.Seq(source)
			transaction := accounttx.NewAccountSet(source.Address)
			transaction.Fee = "10"
			transaction.Sponsor = sponsor.Address
			transaction.SponsorFlags = &flags
			transaction.SponsorSignature = testCase.signature

			require.Equal(t, "temINVALID", env.Submit(transaction).Code)
			require.Equal(t, sequenceBefore, env.Seq(source))
			require.Equal(t, sourceBefore, env.Balance(source))
			require.Equal(t, sponsorBefore, env.Balance(sponsor))
		})
	}
}

func TestSponsorSignatureRejectsPseudoAccount(t *testing.T) {
	env, source, _, sponsor, _ := sponsorEnv(t)
	setPseudoAccount(t, env, sponsor)

	transaction := accounttx.NewAccountSet(source.Address)
	transaction.Fee = "10"
	transaction.Sponsor = sponsor.Address
	flags := tx.SpfSponsorFee
	transaction.SponsorFlags = &flags
	attachSponsorSignature(t, env, transaction, source, sponsor)

	require.Equal(t, "tefBAD_AUTH", env.SubmitSigned(transaction).Code)
}

func TestPresentEmptySponsorIsNotIgnored(t *testing.T) {
	env, source, _, _, _ := sponsorEnv(t)
	transaction := accounttx.NewAccountSet(source.Address)
	flags := tx.SpfSponsorFee
	transaction.SponsorFlags = &flags
	transaction.SetPresentFields(map[string]bool{
		"Sponsor":      true,
		"SponsorFlags": true,
	})

	require.Equal(t, "terNO_ACCOUNT", env.Submit(transaction).Code)
}

func TestSponsorMultisignAuthorizationAndFeeUnits(t *testing.T) {
	env, source, _, sponsor, _ := sponsorEnv(t)
	signer1 := jtx.NewAccount("sponsor-multisigner-1")
	signer2 := jtx.NewAccount("sponsor-multisigner-2")
	env.SetSignerList(sponsor, 2, []jtx.TestSigner{
		{Account: signer1, Weight: 1},
		{Account: signer2, Weight: 1},
	})

	flags := tx.SpfSponsorFee
	oneSigner := accounttx.NewAccountSet(source.Address)
	oneSigner.Fee = "20"
	oneSigner.Sponsor = sponsor.Address
	oneSigner.SponsorFlags = &flags
	attachSponsorMultiSignature(t, env, oneSigner, source, signer1)
	require.Equal(t, "tefBAD_QUORUM", env.SubmitSigned(oneSigner).Code)

	underpaid := accounttx.NewAccountSet(source.Address)
	underpaid.Fee = "29"
	underpaid.Sponsor = sponsor.Address
	underpaid.SponsorFlags = &flags
	attachSponsorMultiSignature(t, env, underpaid, source, signer1, signer2)
	require.Equal(t, "telINSUF_FEE_P", env.SubmitSigned(underpaid).Code)

	sourceBefore := env.Balance(source)
	sponsorBefore := env.Balance(sponsor)
	valid := accounttx.NewAccountSet(source.Address)
	valid.Fee = "30"
	valid.Sponsor = sponsor.Address
	valid.SponsorFlags = &flags
	attachSponsorMultiSignature(t, env, valid, source, signer1, signer2)
	require.Equal(t, "tesSUCCESS", env.SubmitSigned(valid).Code)
	require.Equal(t, sourceBefore, env.Balance(source))
	require.Equal(t, sponsorBefore-30, env.Balance(sponsor))
}

func TestTopLevelMultisignWithSponsorFee(t *testing.T) {
	env, source, _, sponsor, _ := sponsorEnv(t)
	sourceSigner := jtx.NewAccount("source-multisigner")
	env.SetSignerList(source, 1, []jtx.TestSigner{{Account: sourceSigner, Weight: 1}})

	transaction := accounttx.NewAccountSet(source.Address)
	transaction.Fee = "20"
	sequence := env.Seq(source)
	transaction.Sequence = &sequence
	transaction.SigningPubKey = ""
	transaction.Sponsor = sponsor.Address
	flags := tx.SpfSponsorFee
	transaction.SponsorFlags = &flags
	sponsorSignature, err := signtx.SignSponsor(
		transaction,
		sponsor.PublicKeyHex(),
		signingPrivateKey(sponsor),
	)
	require.NoError(t, err)
	transaction.SponsorSignature = sponsorSignature

	sourceBefore := env.Balance(source)
	sponsorBefore := env.Balance(sponsor)
	require.Equal(t, "tesSUCCESS", env.SubmitMultiSigned(transaction, []*jtx.Account{sourceSigner}).Code)
	require.Equal(t, sourceBefore, env.Balance(source))
	require.Equal(t, sponsorBefore-20, env.Balance(sponsor))
}

func TestSponsorPrefundedFeeSelectionLimitsAndRecovery(t *testing.T) {
	env, source, destination, sponsor, _ := sponsorEnv(t)
	require.Equal(t, "tesSUCCESS", setFeeSponsorship(env, sponsor, source, 100, 10).Code)

	sourceBefore := env.Balance(source)
	destinationBefore := env.Balance(destination)
	sponsorBefore := env.Balance(sponsor)
	flags := tx.SpfSponsorFee
	payment := paymenttx.NewPayment(source.Address, destination.Address, tx.NewXRPAmount(1_000))
	payment.Fee = "10"
	payment.Sponsor = sponsor.Address
	payment.SponsorFlags = &flags
	// Even with a co-signature present, the existing pre-funded relationship
	// remains the selected payer.
	payment.SponsorSignature = &tx.SponsorSignature{}
	require.Equal(t, "tesSUCCESS", env.Submit(payment).Code)
	require.Equal(t, sourceBefore-1_000, env.Balance(source))
	require.Equal(t, destinationBefore+1_000, env.Balance(destination))
	require.Equal(t, sponsorBefore, env.Balance(sponsor))
	require.Equal(t, uint64(90), sponsorshipEntry(t, env, sponsor, source).FeeAmount)

	overMax := accounttx.NewAccountSet(source.Address)
	overMax.Fee = "11"
	overMax.Sponsor = sponsor.Address
	overMax.SponsorFlags = &flags
	require.Equal(t, "terINSUF_FEE_B", env.Submit(overMax).Code)
	require.Equal(t, uint64(90), sponsorshipEntry(t, env, sponsor, source).FeeAmount)

	sourceBefore = env.Balance(source)
	destinationBefore = env.Balance(destination)
	failingPayment := paymenttx.NewPayment(
		source.Address,
		destination.Address,
		tx.NewXRPAmount(jtx.XRP(20_000)),
	)
	failingPayment.Fee = "10"
	failingPayment.Sponsor = sponsor.Address
	failingPayment.SponsorFlags = &flags
	require.Equal(t, "tecUNFUNDED_PAYMENT", env.Submit(failingPayment).Code)
	require.Equal(t, sourceBefore, env.Balance(source))
	require.Equal(t, destinationBefore, env.Balance(destination))
	require.Equal(t, sponsorBefore, env.Balance(sponsor))
	require.Equal(t, uint64(80), sponsorshipEntry(t, env, sponsor, source).FeeAmount)

	// On a closed view, a fee above MaxFee claims only the authorized cap.
	env.SetOpenLedger(false)
	closedLedgerFailure := accounttx.NewAccountSet(source.Address)
	closedLedgerFailure.Fee = "1000"
	closedLedgerFailure.Sponsor = sponsor.Address
	closedLedgerFailure.SponsorFlags = &flags
	require.Equal(t, "tecINSUFF_FEE", env.Submit(closedLedgerFailure).Code)
	require.Equal(t, uint64(70), sponsorshipEntry(t, env, sponsor, source).FeeAmount)
	require.Equal(t, sponsorBefore, env.Balance(sponsor))
}

func TestReserveOnlySponsorDoesNotPayFee(t *testing.T) {
	env, source, _, sponsor, _ := sponsorEnv(t)
	transaction := accounttx.NewAccountSet(source.Address)
	transaction.Fee = "10"
	transaction.Sponsor = sponsor.Address
	flags := tx.SpfSponsorReserve
	transaction.SponsorFlags = &flags
	attachSponsorSignature(t, env, transaction, source, sponsor)

	sourceBefore := env.Balance(source)
	sponsorBefore := env.Balance(sponsor)
	require.Equal(t, "tesSUCCESS", env.SubmitSigned(transaction).Code)
	require.Equal(t, sourceBefore-10, env.Balance(source))
	require.Equal(t, sponsorBefore, env.Balance(sponsor))
}

func TestCosignedSponsorFeeNeverConsumesReserve(t *testing.T) {
	t.Run("exactly fee above reserve", func(t *testing.T) {
		env, source, _, sponsor, _ := sponsorEnv(t)
		setAccountBalance(t, env, sponsor, env.ReserveBase()+10)
		sourceBefore := env.Balance(source)

		transaction := accounttx.NewAccountSet(source.Address)
		transaction.Fee = "10"
		transaction.Sponsor = sponsor.Address
		flags := tx.SpfSponsorFee
		transaction.SponsorFlags = &flags
		attachSponsorSignature(t, env, transaction, source, sponsor)

		require.Equal(t, "tesSUCCESS", env.SubmitSigned(transaction).Code)
		require.Equal(t, sourceBefore, env.Balance(source))
		require.Equal(t, env.ReserveBase(), env.Balance(sponsor))
	})

	t.Run("one drop short", func(t *testing.T) {
		env, source, _, sponsor, _ := sponsorEnv(t)
		setAccountBalance(t, env, sponsor, env.ReserveBase()+9)
		sourceBefore := env.Balance(source)
		sponsorBefore := env.Balance(sponsor)
		sequenceBefore := env.Seq(source)

		transaction := accounttx.NewAccountSet(source.Address)
		transaction.Fee = "10"
		transaction.Sponsor = sponsor.Address
		flags := tx.SpfSponsorFee
		transaction.SponsorFlags = &flags
		attachSponsorSignature(t, env, transaction, source, sponsor)

		require.Equal(t, "terINSUF_FEE_B", env.SubmitSigned(transaction).Code)
		require.Equal(t, sequenceBefore, env.Seq(source))
		require.Equal(t, sourceBefore, env.Balance(source))
		require.Equal(t, sponsorBefore, env.Balance(sponsor))
	})
}

func TestSponsorFeeWithTicketConsumesTicketNotSequence(t *testing.T) {
	env, source, _, sponsor, _ := sponsorEnv(t)
	ticketSequence := env.CreateTickets(source, 1)
	require.Equal(t, "tesSUCCESS", setFeeSponsorship(env, sponsor, source, 30, 0).Code)

	sourceBalance := env.Balance(source)
	sourceSequence := env.Seq(source)
	transaction := accounttx.NewAccountSet(source.Address)
	transaction.Fee = "10"
	transaction.Sponsor = sponsor.Address
	flags := tx.SpfSponsorFee
	transaction.SponsorFlags = &flags
	jtx.WithTicketSeq(transaction, ticketSequence)

	require.Equal(t, "tesSUCCESS", env.Submit(transaction).Code)
	require.Equal(t, sourceBalance, env.Balance(source))
	require.Equal(t, sourceSequence, env.Seq(source))
	require.Zero(t, env.TicketCount(source))
	require.Equal(t, uint64(20), sponsorshipEntry(t, env, sponsor, source).FeeAmount)
}

func TestDelegatedTransactionSponsorFeePayer(t *testing.T) {
	t.Run("co-signed sponsor account", func(t *testing.T) {
		env, source, destination, sponsor, delegate := sponsorEnv(t)
		grantDelegatePermission(t, env, source, delegate, "Payment")

		sourceBefore := env.Balance(source)
		delegateBefore := env.Balance(delegate)
		destinationBefore := env.Balance(destination)
		sponsorBefore := env.Balance(sponsor)

		payment := paymenttx.NewPayment(source.Address, destination.Address, tx.NewXRPAmount(1_000))
		payment.Fee = "10"
		payment.Delegate = delegate.Address
		payment.Sponsor = sponsor.Address
		flags := tx.SpfSponsorFee
		payment.SponsorFlags = &flags
		attachSponsorSignatureFor(t, env, payment, source, delegate, sponsor)

		require.Equal(t, "tesSUCCESS", env.SubmitSignedWith(payment, delegate).Code)
		require.Equal(t, sourceBefore-1_000, env.Balance(source))
		require.Equal(t, delegateBefore, env.Balance(delegate))
		require.Equal(t, destinationBefore+1_000, env.Balance(destination))
		require.Equal(t, sponsorBefore-10, env.Balance(sponsor))
	})

	t.Run("pre-funded relationship targets delegate", func(t *testing.T) {
		env, source, destination, sponsor, delegate := sponsorEnv(t)
		grantDelegatePermission(t, env, source, delegate, "Payment")
		require.Equal(t, "tesSUCCESS", setFeeSponsorship(env, sponsor, delegate, 100, 0).Code)
		env.Close()

		sourceBefore := env.Balance(source)
		delegateBefore := env.Balance(delegate)
		destinationBefore := env.Balance(destination)
		sponsorBefore := env.Balance(sponsor)
		require.False(t, env.LedgerEntryExists(keylet.Sponsorship(sponsor.ID, source.ID)))

		payment := paymenttx.NewPayment(source.Address, destination.Address, tx.NewXRPAmount(1_000))
		payment.Fee = "10"
		payment.Delegate = delegate.Address
		payment.Sponsor = sponsor.Address
		flags := tx.SpfSponsorFee
		payment.SponsorFlags = &flags

		require.Equal(t, "tesSUCCESS", env.SubmitSignedWith(payment, delegate).Code)
		require.Equal(t, sourceBefore-1_000, env.Balance(source))
		require.Equal(t, delegateBefore, env.Balance(delegate))
		require.Equal(t, destinationBefore+1_000, env.Balance(destination))
		require.Equal(t, sponsorBefore, env.Balance(sponsor))
		require.Equal(t, uint64(90), sponsorshipEntry(t, env, sponsor, delegate).FeeAmount)
	})
}

func TestDelegatedTransactionRejectsReserveSponsor(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		prefunded bool
	}{
		{name: "co-signed"},
		{name: "pre-funded", prefunded: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			env, source, destination, sponsor, delegate := sponsorEnv(t)
			grantDelegatePermission(t, env, source, delegate, "CheckCreate")
			if testCase.prefunded {
				remaining := int32(1)
				require.Equal(t, "tesSUCCESS", setSponsorship(env, sponsor, delegate, 0, &remaining).Code)
			}
			env.Close()

			sourceBefore := env.Balance(source)
			delegateBefore := env.Balance(delegate)
			sponsorBefore := env.Balance(sponsor)
			sequenceBefore := env.Seq(source)

			transaction := checktx.NewCheckCreate(
				source.Address,
				destination.Address,
				tx.NewXRPAmount(1_000),
			)
			transaction.Delegate = delegate.Address
			transaction.Sponsor = sponsor.Address
			flags := tx.SpfSponsorReserve
			transaction.SponsorFlags = &flags
			if !testCase.prefunded {
				attachSponsorSignatureFor(t, env, transaction, source, delegate, sponsor)
			}

			require.Equal(t, "temINVALID", env.SubmitSignedWith(transaction, delegate).Code)
			require.Equal(t, sequenceBefore, env.Seq(source))
			require.Equal(t, sourceBefore, env.Balance(source))
			require.Equal(t, delegateBefore, env.Balance(delegate))
			require.Equal(t, sponsorBefore, env.Balance(sponsor))
		})
	}
}

func TestSponsorCreatedAccount(t *testing.T) {
	t.Run("one drop account", func(t *testing.T) {
		env, source, _, _, _ := sponsorEnv(t)
		destination := jtx.NewAccount("sponsor-created-destination")
		sourceBefore := env.Balance(source)

		payment := paymenttx.NewPayment(source.Address, destination.Address, tx.NewXRPAmount(1))
		payment.Fee = "10"
		payment.SetFlags(paymenttx.PaymentFlagSponsorCreatedAccount)
		require.Equal(t, "tesSUCCESS", env.Submit(payment).Code)

		require.Equal(t, sourceBefore-11, env.Balance(source))
		require.Equal(t, uint64(1), env.Balance(destination))
		destinationRoot := accountState(t, env, destination)
		require.True(t, destinationRoot.HasSponsor)
		require.Equal(t, source.Address, destinationRoot.Sponsor)
		require.Equal(t, uint32(1), accountState(t, env, source).SponsoringAccountCount)
	})

	t.Run("existing destination", func(t *testing.T) {
		env, source, destination, _, _ := sponsorEnv(t)
		sourceBefore := env.Balance(source)
		destinationBefore := env.Balance(destination)

		payment := paymenttx.NewPayment(source.Address, destination.Address, tx.NewXRPAmount(1))
		payment.Fee = "10"
		payment.SetFlags(paymenttx.PaymentFlagSponsorCreatedAccount)
		require.Equal(t, "tecNO_SPONSOR_PERMISSION", env.Submit(payment).Code)
		require.Equal(t, sourceBefore-10, env.Balance(source))
		require.Equal(t, destinationBefore, env.Balance(destination))
		require.Zero(t, accountState(t, env, source).SponsoringAccountCount)
	})

	t.Run("source reserve boundary", func(t *testing.T) {
		t.Run("one drop short", func(t *testing.T) {
			env, source, _, _, _ := sponsorEnv(t)
			destination := jtx.NewAccount("sponsor-created-boundary-short")
			required := 2*env.ReserveBase() + 1
			setAccountBalance(t, env, source, required-1)

			payment := paymenttx.NewPayment(source.Address, destination.Address, tx.NewXRPAmount(1))
			payment.Fee = "10"
			payment.SetFlags(paymenttx.PaymentFlagSponsorCreatedAccount)
			require.Equal(t, "tecUNFUNDED_PAYMENT", env.Submit(payment).Code)
			require.False(t, env.LedgerEntryExists(keylet.Account(destination.ID)))
			require.Equal(t, required-1-10, env.Balance(source))
			require.Zero(t, accountState(t, env, source).SponsoringAccountCount)
		})

		t.Run("exact", func(t *testing.T) {
			env, source, _, _, _ := sponsorEnv(t)
			destination := jtx.NewAccount("sponsor-created-boundary-exact")
			required := 2*env.ReserveBase() + 1
			setAccountBalance(t, env, source, required)

			payment := paymenttx.NewPayment(source.Address, destination.Address, tx.NewXRPAmount(1))
			payment.Fee = "10"
			payment.SetFlags(paymenttx.PaymentFlagSponsorCreatedAccount)
			require.Equal(t, "tesSUCCESS", env.Submit(payment).Code)
			require.Equal(t, required-11, env.Balance(source))
			require.Equal(t, uint32(1), accountState(t, env, source).SponsoringAccountCount)
		})
	})
}

func TestSponsorCreatedAccountPreflightMatrix(t *testing.T) {
	env, source, _, issuer, _ := sponsorEnv(t)
	destination := jtx.NewAccount("sponsor-created-preflight-destination")

	testCases := []struct {
		name    string
		mutate  func(*paymenttx.Payment)
		wantTER string
	}{
		{
			name: "path flag",
			mutate: func(payment *paymenttx.Payment) {
				payment.SetFlags(
					paymenttx.PaymentFlagSponsorCreatedAccount |
						paymenttx.PaymentFlagPartialPayment,
				)
			},
			wantTER: "temINVALID_FLAG",
		},
		{
			name: "SendMax",
			mutate: func(payment *paymenttx.Payment) {
				sendMax := tx.NewXRPAmount(2)
				payment.SendMax = &sendMax
			},
			wantTER: "temINVALID",
		},
		{
			name: "Paths",
			mutate: func(payment *paymenttx.Payment) {
				payment.Paths = [][]paymenttx.PathStep{{{Account: issuer.Address}}}
			},
			wantTER: "temINVALID",
		},
		{
			name: "issued amount",
			mutate: func(payment *paymenttx.Payment) {
				payment.Amount = issuer.IOU("USD", 1)
			},
			wantTER: "temBAD_AMOUNT",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			payment := paymenttx.NewPayment(source.Address, destination.Address, tx.NewXRPAmount(1))
			payment.Fee = "10"
			payment.SetFlags(paymenttx.PaymentFlagSponsorCreatedAccount)
			testCase.mutate(payment)
			require.Equal(t, testCase.wantTER, env.Submit(payment).Code)
		})
	}
}

func TestSponsorshipSetDirectoryFailureRollsBackFirstInsert(t *testing.T) {
	env, sponsee, _, sponsor, _ := sponsorEnv(t)

	// Keep the DirectoryNode type marker but omit required directory fields.
	// The sponsor directory insert succeeds in the sandbox; parsing this
	// sponsee directory then fails, forcing a full rollback.
	malformedDirectory, err := binarycodec.EncodeBytes(map[string]any{
		"LedgerEntryType": "DirectoryNode",
	})
	require.NoError(t, err)
	if len(malformedDirectory) < 12 {
		malformedDirectory = append(malformedDirectory, make([]byte, 12-len(malformedDirectory))...)
	}
	require.NoError(t, env.Ledger().Insert(keylet.OwnerDir(sponsee.ID), malformedDirectory))

	remaining := int32(1)
	result := setSponsorship(env, sponsor, sponsee, 0, &remaining)
	require.Equal(t, "tefINTERNAL", result.Code)
	require.False(t, env.LedgerEntryExists(keylet.Sponsorship(sponsor.ID, sponsee.ID)))
	require.False(t, env.LedgerEntryExists(keylet.OwnerDir(sponsor.ID)),
		"the first directory insert must not escape the failed transaction sandbox")
	require.Zero(t, env.OwnerCount(sponsor))
}

func TestSponsorAmendmentGate(t *testing.T) {
	env := jtx.NewTestEnv(t)
	sponsor := jtx.NewAccount("gate-sponsor")
	sponsee := jtx.NewAccount("gate-sponsee")
	destination := jtx.NewAccount("gate-destination")
	env.Fund(sponsor, sponsee)
	env.DisableFeature("Sponsor")
	env.Close()
	remaining := int32(1)
	require.Equal(t, "temDISABLED", setSponsorship(env, sponsor, sponsee, 0, &remaining).Code)

	payment := paymenttx.NewPayment(sponsee.Address, destination.Address, tx.NewXRPAmount(1))
	payment.SetFlags(paymenttx.PaymentFlagSponsorCreatedAccount)
	require.Equal(t, "temDISABLED", env.Submit(payment).Code)
}
