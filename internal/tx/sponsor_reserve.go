package tx

import (
	"math"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

type ReserveAdjustment struct {
	OwnerCountDelta   int32
	AccountCountDelta int32
}

type reserveSponsor struct {
	id       [20]byte
	address  string
	account  *state.AccountRoot
	relation *state.SponsorshipData
}

func effectiveReserveSponsor(ctx *ApplyContext, common *Common, ownerID [20]byte, owner *state.AccountRoot) (*reserveSponsor, ter.Result) {
	if !ctx.Rules().Enabled(amendment.FeatureSponsor) || common == nil || owner == nil || owner.IsPseudoAccount() || ownerID != ctx.AccountID {
		return nil, ter.TesSUCCESS
	}
	if common.Sponsor == "" || common.SponsorFlags == nil || *common.SponsorFlags&SpfSponsorReserve == 0 {
		return nil, ter.TesSUCCESS
	}

	sponsorID, err := state.DecodeAccountID(common.Sponsor)
	if err != nil {
		return nil, ter.TefINTERNAL
	}
	sponsor, err := ReadAccountRoot(ctx.View, sponsorID)
	if err != nil || sponsor == nil {
		return nil, ter.TecINTERNAL
	}

	var relation *state.SponsorshipData
	data, err := ctx.View.Read(keylet.Sponsorship(sponsorID, ownerID))
	if err != nil {
		return nil, ter.TefINTERNAL
	}
	if data != nil {
		relation, err = state.ParseSponsorship(data)
		if err != nil {
			return nil, ter.TefINTERNAL
		}
	}
	return &reserveSponsor{id: sponsorID, address: common.Sponsor, account: sponsor, relation: relation}, ter.TesSUCCESS
}

func effectiveOwnerCount(account *state.AccountRoot, delta int32) uint32 {
	adjustment := int64(delta) - int64(account.SponsoredOwnerCount) + int64(account.SponsoringOwnerCount)
	if adjustment > math.MaxInt32 {
		adjustment = math.MaxInt32
	} else if adjustment < math.MinInt32 {
		adjustment = math.MinInt32
	}
	return ConfineOwnerCount(account.OwnerCount, int(adjustment))
}

func OwnerCountForReserve(account *state.AccountRoot, rules *amendment.Rules) uint32 {
	if rules == nil || !rules.Enabled(amendment.FeatureSponsor) {
		return account.OwnerCount
	}
	return effectiveOwnerCount(account, 0)
}

func TransactionHasReserveSponsor(common *Common) bool {
	return common != nil && common.Sponsor != "" && common.SponsorFlags != nil && *common.SponsorFlags&SpfSponsorReserve != 0
}

func effectiveAccountCount(account *state.AccountRoot, delta int32) uint32 {
	base := int64(1)
	if account.HasSponsor {
		base = 0
	}
	total := base + int64(account.SponsoringAccountCount) + int64(delta)
	if total < 0 {
		return 0
	}
	if total > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(total)
}

func reserveForAdjustment(config EngineConfig, account *state.AccountRoot, adjustment ReserveAdjustment, sponsorEnabled bool) uint64 {
	if !sponsorEnabled {
		return config.AccountReserve(ConfineOwnerCount(account.OwnerCount, int(adjustment.OwnerCountDelta)))
	}
	return config.AccountReserveWithCounts(
		effectiveOwnerCount(account, adjustment.OwnerCountDelta),
		effectiveAccountCount(account, adjustment.AccountCountDelta),
	)
}

func RequiredReserve(ctx *ApplyContext, account *state.AccountRoot, adjustment ReserveAdjustment) uint64 {
	return reserveForAdjustment(ctx.Config, account, adjustment, ctx.Rules().Enabled(amendment.FeatureSponsor))
}

// CheckReserve validates the reserve burden after adjustment, routing it to the
// transaction reserve sponsor only for objects owned by the transaction source.
func CheckReserve(
	ctx *ApplyContext,
	common *Common,
	ownerID [20]byte,
	owner *state.AccountRoot,
	balance uint64,
	adjustment ReserveAdjustment,
	failure ter.Result,
) ter.Result {
	enabled := ctx.Rules().Enabled(amendment.FeatureSponsor)
	sponsor, result := effectiveReserveSponsor(ctx, common, ownerID, owner)
	if result != ter.TesSUCCESS {
		return result
	}
	if sponsor == nil {
		if balance < reserveForAdjustment(ctx.Config, owner, adjustment, enabled) {
			return failure
		}
		return ter.TesSUCCESS
	}
	if adjustment.OwnerCountDelta > 0 && sponsor.relation != nil &&
		sponsor.relation.RemainingOwnerCount < uint32(adjustment.OwnerCountDelta) {
		return failure
	}
	if sponsor.account.Balance < reserveForAdjustment(ctx.Config, sponsor.account, adjustment, true) {
		return failure
	}
	return ter.TesSUCCESS
}

func writeSponsorAccount(ctx *ApplyContext, accountID [20]byte, account *state.AccountRoot) ter.Result {
	if accountID == ctx.AccountID {
		ctx.Account = account
		return ter.TesSUCCESS
	}
	return ctx.UpdateAccountRoot(accountID, account)
}

func adjustSponsoredOwnerCount(
	ctx *ApplyContext,
	ownerID [20]byte,
	owner *state.AccountRoot,
	sponsor *reserveSponsor,
	delta int32,
) ter.Result {
	if delta == 0 {
		return ter.TesSUCCESS
	}
	ownerBefore := NewOwnerCounts(owner)
	var sponsorBefore OwnerCounts
	if sponsor != nil {
		sponsorBefore = NewOwnerCounts(sponsor.account)
	}
	owner.OwnerCount = ConfineOwnerCount(owner.OwnerCount, int(delta))
	if sponsor == nil {
		if hook, ok := ctx.View.(ownerCountAdjustHook); ok {
			hook.AdjustOwnerCount(ownerID, ownerBefore, NewOwnerCounts(owner))
		}
		return writeSponsorAccount(ctx, ownerID, owner)
	}
	owner.SponsoredOwnerCount = ConfineOwnerCount(owner.SponsoredOwnerCount, int(delta))
	sponsor.account.SponsoringOwnerCount = ConfineOwnerCount(sponsor.account.SponsoringOwnerCount, int(delta))
	if hook, ok := ctx.View.(ownerCountAdjustHook); ok {
		hook.AdjustOwnerCount(ownerID, ownerBefore, NewOwnerCounts(owner))
		hook.AdjustOwnerCount(sponsor.id, sponsorBefore, NewOwnerCounts(sponsor.account))
	}
	if delta > 0 && sponsor.relation != nil {
		amount := uint32(delta)
		if sponsor.relation.RemainingOwnerCount < amount {
			return ter.TefINTERNAL
		}
		sponsor.relation.RemainingOwnerCount -= amount
		encoded, err := state.SerializeSponsorship(sponsor.relation)
		if err != nil {
			return ctx.Internal("serialize Sponsorship reserve budget", err)
		}
		if err := ctx.View.Update(keylet.Sponsorship(sponsor.id, ownerID), encoded); err != nil {
			return ctx.Internal("update Sponsorship reserve budget", err)
		}
	}
	if result := writeSponsorAccount(ctx, ownerID, owner); result != ter.TesSUCCESS {
		return result
	}
	return writeSponsorAccount(ctx, sponsor.id, sponsor.account)
}

// IncreaseOwnerCount adjusts owner and sponsorship counters and returns the
// sponsor address to stamp on the created object, if reserve-sponsored.
func IncreaseOwnerCount(ctx *ApplyContext, common *Common, ownerID [20]byte, owner *state.AccountRoot, count uint32) (string, ter.Result) {
	if count == 0 || count > math.MaxInt32 {
		return "", ter.TefINTERNAL
	}
	sponsor, result := effectiveReserveSponsor(ctx, common, ownerID, owner)
	if result != ter.TesSUCCESS {
		return "", result
	}
	if result := adjustSponsoredOwnerCount(ctx, ownerID, owner, sponsor, int32(count)); result != ter.TesSUCCESS {
		return "", result
	}
	if sponsor == nil {
		return "", ter.TesSUCCESS
	}
	return sponsor.address, ter.TesSUCCESS
}

func sponsorFromLedgerEntry(ctx *ApplyContext, data []byte, field string) (*reserveSponsor, ter.Result) {
	if !ctx.Rules().Enabled(amendment.FeatureSponsor) || len(data) == 0 {
		return nil, ter.TesSUCCESS
	}
	fields, err := binarycodec.DecodeBytes(data)
	if err != nil {
		return nil, ter.TefINTERNAL
	}
	address, _ := fields[field].(string)
	if address == "" {
		return nil, ter.TesSUCCESS
	}
	id, err := state.DecodeAccountID(address)
	if err != nil {
		return nil, ter.TefINTERNAL
	}
	account := ctx.Account
	if id != ctx.AccountID {
		account, err = ReadAccountRoot(ctx.View, id)
		if err != nil || account == nil {
			return nil, ter.TefINTERNAL
		}
	}
	return &reserveSponsor{id: id, address: address, account: account}, ter.TesSUCCESS
}

// DecreaseOwnerCountForObject decrements OwnerCount and releases the paired
// sponsored/sponsoring counters recorded by the object's sponsor field.
func DecreaseOwnerCountForObject(
	ctx *ApplyContext,
	ownerID [20]byte,
	owner *state.AccountRoot,
	objectData []byte,
	sponsorField string,
	count uint32,
) ter.Result {
	if count == 0 || count > math.MaxInt32 {
		return ter.TefINTERNAL
	}
	sponsor, result := sponsorFromLedgerEntry(ctx, objectData, sponsorField)
	if result != ter.TesSUCCESS {
		return result
	}
	return adjustSponsoredOwnerCount(ctx, ownerID, owner, sponsor, -int32(count))
}

func DecreaseOwnerCountOnView(view LedgerView, ownerID [20]byte, sponsorAddress string, count uint32) ter.Result {
	if count == 0 || count > math.MaxInt32 {
		return ter.TefINTERNAL
	}
	owner, err := ReadAccountRoot(view, ownerID)
	if err != nil || owner == nil {
		return ter.TefINTERNAL
	}
	ownerBefore := NewOwnerCounts(owner)
	owner.OwnerCount = ConfineOwnerCount(owner.OwnerCount, -int(count))
	var sponsorID [20]byte
	var sponsor *state.AccountRoot
	var sponsorBefore OwnerCounts
	if view.Rules() != nil && view.Rules().Enabled(amendment.FeatureSponsor) && sponsorAddress != "" {
		sponsorID, err = state.DecodeAccountID(sponsorAddress)
		if err != nil {
			return ter.TefINTERNAL
		}
		sponsor, err = ReadAccountRoot(view, sponsorID)
		if err != nil || sponsor == nil {
			return ter.TefINTERNAL
		}
		sponsorBefore = NewOwnerCounts(sponsor)
		owner.SponsoredOwnerCount = ConfineOwnerCount(owner.SponsoredOwnerCount, -int(count))
		sponsor.SponsoringOwnerCount = ConfineOwnerCount(sponsor.SponsoringOwnerCount, -int(count))
		data, err := state.SerializeAccountRoot(sponsor)
		if err != nil || view.Update(keylet.Account(sponsorID), data) != nil {
			return ter.TefINTERNAL
		}
	}
	if hook, ok := view.(ownerCountAdjustHook); ok {
		hook.AdjustOwnerCount(ownerID, ownerBefore, NewOwnerCounts(owner))
		if sponsor != nil {
			hook.AdjustOwnerCount(sponsorID, sponsorBefore, NewOwnerCounts(sponsor))
		}
	}
	data, err := state.SerializeAccountRoot(owner)
	if err != nil || view.Update(keylet.Account(ownerID), data) != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}

// SetLedgerEntrySponsor stamps an AccountID sponsor field on serialized SLE
// bytes. An empty sponsor leaves the object unchanged.
func SetLedgerEntrySponsor(data []byte, field, sponsor string) ([]byte, error) {
	if sponsor == "" {
		return data, nil
	}
	fields, err := binarycodec.DecodeBytes(data)
	if err != nil {
		return nil, err
	}
	fields[field] = sponsor
	return binarycodec.EncodeBytes(fields)
}
