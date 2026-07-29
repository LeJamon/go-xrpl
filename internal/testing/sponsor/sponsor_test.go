package sponsor_test

import (
	"encoding/hex"
	"sort"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	checktx "github.com/LeJamon/go-xrpl/internal/tx/check"
	sponsortx "github.com/LeJamon/go-xrpl/internal/tx/sponsor"
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
	feeDrops uint64,
	remaining *uint32,
) jtx.TxResult {
	txn := sponsortx.NewSponsorshipSet(sponsor.Address)
	txn.Sponsee = sponsee.Address
	if feeDrops > 0 {
		amount := tx.NewXRPAmount(int64(feeDrops))
		txn.FeeAmount = &amount
	}
	txn.RemainingOwnerCount = remaining
	return env.Submit(txn)
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
	remaining := uint32(3)
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

	updatedFee := tx.NewXRPAmount(400)
	updatedRemaining := uint32(2)
	update := sponsortx.NewSponsorshipSet(sponsor.Address)
	update.Sponsee = sponsee.Address
	update.FeeAmount = &updatedFee
	update.RemainingOwnerCount = &updatedRemaining
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

func TestSponsorshipSetPreclaimMatrix(t *testing.T) {
	env, sponsee, _, sponsor, _ := sponsorEnv(t)
	missing := jtx.NewAccount("missing-sponsor-party")
	remaining := uint32(1)

	require.Equal(t, "tecNO_DST", setSponsorship(env, sponsor, missing, 0, &remaining).Code)

	missingSponsor := sponsortx.NewSponsorshipSet(sponsee.Address)
	missingSponsor.CounterpartySponsor = missing.Address
	missingSponsor.SetFlags(sponsortx.SponsorshipSetFlagDelete)
	require.Equal(t, "tecNO_DST", env.Submit(missingSponsor).Code)

	missingObject := sponsortx.NewSponsorshipSet(sponsor.Address)
	missingObject.Sponsee = sponsee.Address
	missingObject.SetFlags(sponsortx.SponsorshipSetFlagDelete)
	require.Equal(t, "tecNO_ENTRY", env.Submit(missingObject).Code)

	require.Equal(t, "tecNO_PERMISSION", setSponsorship(env, sponsor, sponsee, 0, nil).Code)
	require.False(t, env.LedgerEntryExists(keylet.Sponsorship(sponsor.ID, sponsee.ID)))

	setPseudoAccount(t, env, sponsee)
	require.Equal(t, "tecNO_PERMISSION", setSponsorship(env, sponsor, sponsee, 0, &remaining).Code)
	require.False(t, env.LedgerEntryExists(keylet.Sponsorship(sponsor.ID, sponsee.ID)))

	pseudoSponsorEnv, ordinarySponsee, _, pseudoSponsor, _ := sponsorEnv(t)
	setPseudoAccount(t, pseudoSponsorEnv, pseudoSponsor)
	deleteFromSponsee := sponsortx.NewSponsorshipSet(ordinarySponsee.Address)
	deleteFromSponsee.CounterpartySponsor = pseudoSponsor.Address
	deleteFromSponsee.SetFlags(sponsortx.SponsorshipSetFlagDelete)
	require.Equal(t, "tecNO_PERMISSION", pseudoSponsorEnv.Submit(deleteFromSponsee).Code)
	require.False(t, pseudoSponsorEnv.LedgerEntryExists(keylet.Sponsorship(pseudoSponsor.ID, ordinarySponsee.ID)))
}

func TestSponsorshipTransferObjectCreateReassignEnd(t *testing.T) {
	env, sponsee, destination, sponsor1, sponsor2 := sponsorEnv(t)

	checkSequence := env.Seq(sponsee)
	createCheck := checktx.NewCheckCreate(sponsee.Address, destination.Address, tx.NewXRPAmount(jtx.XRP(1)))
	require.Equal(t, "tesSUCCESS", env.Submit(createCheck).Code)
	checkKey := keylet.Check(sponsee.ID, checkSequence)

	remaining1 := uint32(2)
	remaining2 := uint32(2)
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

	remaining := uint32(1)
	requireSignature := sponsortx.NewSponsorshipSet(sponsor.Address)
	requireSignature.Sponsee = sponsee.Address
	requireSignature.RemainingOwnerCount = &remaining
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

	remaining := uint32(1)
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
	env.Fund(sponsor, sponsee)
	remaining := uint32(1)
	require.Equal(t, "temDISABLED", setSponsorship(env, sponsor, sponsee, 0, &remaining).Code)
}
