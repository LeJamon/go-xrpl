package tx

import (
	"fmt"
	"math"
	"math/bits"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

func reserveOwnerCount(account *state.AccountRoot, ownerDelta int) (uint32, bool) {
	if account == nil || account.SponsoredOwnerCount > account.OwnerCount {
		return 0, false
	}
	delta := int64(ownerDelta) - int64(account.SponsoredOwnerCount) + int64(account.SponsoringOwnerCount)
	if delta > math.MaxInt32 {
		delta = math.MaxInt32
	} else if delta < math.MinInt32 {
		delta = math.MinInt32
	}
	return ConfineOwnerCount(account.OwnerCount, int(delta)), true
}

// EffectiveOwnerCount returns the owner reserve carried by account after a
// signed owner-count adjustment.
func EffectiveOwnerCount(account *state.AccountRoot, ownerDelta int) (uint32, bool) {
	return reserveOwnerCount(account, ownerDelta)
}

func reserveAccountCount(account *state.AccountRoot, accountDelta int) uint32 {
	count := int64(accountDelta) + int64(account.SponsoringAccountCount)
	if !account.HasSponsor {
		count++
	}
	if count < 0 {
		return 0
	}
	if count > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(count)
}

// RequiredAccountReserve returns the reserve carried by account after applying
// the supplied owner- and account-count deltas.
func RequiredAccountReserve(config EngineConfig, account *state.AccountRoot, ownerDelta, accountDelta int) (uint64, bool) {
	owners, ok := reserveOwnerCount(account, ownerDelta)
	if !ok {
		return 0, false
	}
	return checkedAccountReserve(config, owners, reserveAccountCount(account, accountDelta))
}

func checkedAccountReserve(config EngineConfig, ownerCount, accountCount uint32) (uint64, bool) {
	ownerHigh, ownerReserve := bits.Mul64(uint64(ownerCount), config.ReserveIncrement)
	accountHigh, accountReserve := bits.Mul64(uint64(accountCount), config.ReserveBase)
	if ownerHigh != 0 || accountHigh != 0 {
		return 0, false
	}
	reserve, carry := bits.Add64(ownerReserve, accountReserve, 0)
	return reserve, carry == 0
}

// AccountReserveForView returns account's reserve at the supplied raw owner
// count, using the view's amendment rules.
func AccountReserveForView(view LedgerView, config EngineConfig, account *state.AccountRoot, ownerCount uint32) (uint64, bool) {
	if account == nil {
		return 0, false
	}
	rules := config.Rules
	if view != nil && view.Rules() != nil {
		rules = view.Rules()
	}
	if rules == nil || !rules.Enabled(amendment.FeatureSponsor) {
		return checkedAccountReserve(config, ownerCount, 1)
	}
	delta := int64(ownerCount) - int64(account.OwnerCount)
	if delta > math.MaxInt || delta < math.MinInt {
		return 0, false
	}
	return RequiredAccountReserve(config, account, int(delta), 0)
}

func (ctx *ApplyContext) reserveSponsorFor(accountID [20]byte, account *state.AccountRoot) (*state.AccountRoot, [20]byte, ter.Result) {
	var zero [20]byte
	if ctx == nil || ctx.Common == nil || account == nil || accountID != ctx.AccountID ||
		ctx.Common.Sponsor == "" || ctx.Common.SponsorFlags == nil ||
		*ctx.Common.SponsorFlags&SpfSponsorReserve == 0 ||
		!ctx.Rules().Enabled(amendment.FeatureSponsor) || account.IsPseudoAccount() {
		return nil, zero, ter.TesSUCCESS
	}

	sponsorID, err := state.DecodeAccountID(ctx.Common.Sponsor)
	if err != nil {
		return nil, zero, ter.TecINTERNAL
	}
	sponsor, err := ReadAccountRoot(ctx.View, sponsorID)
	if err != nil || sponsor == nil {
		return nil, zero, ter.TecINTERNAL
	}
	return sponsor, sponsorID, ter.TesSUCCESS
}

// UsesReserveSponsorFor reports whether the transaction sponsor carries new
// owner reserve for accountID.
func (ctx *ApplyContext) UsesReserveSponsorFor(accountID [20]byte, account *state.AccountRoot) (bool, ter.Result) {
	sponsor, _, result := ctx.reserveSponsorFor(accountID, account)
	return sponsor != nil, result
}

func readSponsorship(view LedgerView, sponsorID, sponseeID [20]byte) (*state.SponsorshipData, bool, error) {
	data, err := view.Read(keylet.Sponsorship(sponsorID, sponseeID))
	if err != nil {
		return nil, false, err
	}
	if data == nil {
		return nil, false, nil
	}
	sponsorship, err := state.ParseSponsorship(data)
	if err != nil {
		return nil, false, err
	}
	return sponsorship, true, nil
}

// CheckReserveFor applies Sponsor's effective reserve accounting to an account
// reserve check. A transaction reserve sponsor is effective only for the
// transaction source account.
func (ctx *ApplyContext) CheckReserveFor(
	accountID [20]byte,
	account *state.AccountRoot,
	balance uint64,
	ownerDelta, accountDelta int,
	failure ter.Result,
) ter.Result {
	if !ctx.Rules().Enabled(amendment.FeatureSponsor) {
		ownerCount := uint32(int64(account.OwnerCount) + int64(ownerDelta))
		accountCount := uint32(1)
		if accountDelta != 0 {
			accountCount = uint32(int64(accountCount) + int64(accountDelta))
		}
		if balance < ctx.Config.AccountReserveWithCounts(ownerCount, accountCount) {
			return failure
		}
		return ter.TesSUCCESS
	}
	sponsor, sponsorID, result := ctx.reserveSponsorFor(accountID, account)
	if result != ter.TesSUCCESS {
		return result
	}
	reserveAccount := account
	reserveBalance := balance
	if sponsor != nil {
		reserveAccount = sponsor
		reserveBalance = sponsor.Balance
		if ownerDelta > 0 {
			sponsorship, exists, err := readSponsorship(ctx.View, sponsorID, accountID)
			if err != nil {
				return ter.TecINTERNAL
			}
			if exists && sponsorship.RemainingOwnerCount < uint32(ownerDelta) {
				return failure
			}
		}
	}
	reserve, ok := RequiredAccountReserve(ctx.Config, reserveAccount, ownerDelta, accountDelta)
	if !ok {
		return ter.TecINTERNAL
	}
	if reserveBalance < reserve {
		return failure
	}
	return ter.TesSUCCESS
}

func updateAccountOnView(view state.LedgerView, accountID [20]byte, account *state.AccountRoot) error {
	data, err := state.SerializeAccountRoot(account)
	if err != nil {
		return err
	}
	return view.Update(keylet.Account(accountID), data)
}

func consumeSponsoredOwnerBudget(view LedgerView, sponsorID, sponseeID [20]byte, count uint32) error {
	sponsorship, exists, err := readSponsorship(view, sponsorID, sponseeID)
	if err != nil || !exists {
		return err
	}
	if sponsorship.RemainingOwnerCount < count {
		return fmt.Errorf("sponsorship remaining owner count %d is below %d", sponsorship.RemainingOwnerCount, count)
	}
	sponsorship.RemainingOwnerCount -= count
	data, err := state.SerializeSponsorship(sponsorship)
	if err != nil {
		return err
	}
	return view.Update(keylet.Sponsorship(sponsorID, sponseeID), data)
}

// IncreaseOwnerCount increases an account's raw owner count and, when the
// transaction reserve sponsor applies, the matching sponsored counters. The
// returned address is the sponsor that must be stored on the new object.
func IncreaseOwnerCount(ctx *ApplyContext, accountID [20]byte, account *state.AccountRoot, count uint32) (string, ter.Result) {
	if count == 0 || count > math.MaxInt32 {
		return "", ter.TefINTERNAL
	}
	sponsor, sponsorID, result := ctx.reserveSponsorFor(accountID, account)
	if result != ter.TesSUCCESS {
		return "", result
	}
	account.OwnerCount = ConfineOwnerCount(account.OwnerCount, int(count))
	if sponsor == nil {
		return "", ter.TesSUCCESS
	}
	account.SponsoredOwnerCount = ConfineOwnerCount(account.SponsoredOwnerCount, int(count))
	sponsor.SponsoringOwnerCount = ConfineOwnerCount(sponsor.SponsoringOwnerCount, int(count))
	if err := consumeSponsoredOwnerBudget(ctx.View, sponsorID, accountID, count); err != nil {
		return "", ter.TefINTERNAL
	}
	if err := updateAccountOnView(ctx.View, sponsorID, sponsor); err != nil {
		return "", ter.TefINTERNAL
	}
	return ctx.Common.Sponsor, ter.TesSUCCESS
}

// DecreaseOwnerCount decreases the raw owner count and the matching sponsored
// counters when sponsorAddress is non-empty. Prefunded owner budget is not
// restored when an object is removed.
func DecreaseOwnerCount(
	view state.LedgerView,
	account *state.AccountRoot,
	sponsorAddress string,
	count uint32,
) error {
	if count == 0 || count > math.MaxInt32 || account == nil {
		return fmt.Errorf("owner count adjustment %d is outside the signed range", count)
	}
	accountCurrent := ownerCounts(account)
	var sponsor *state.AccountRoot
	var sponsorID [20]byte
	var sponsorCurrent OwnerCounts
	if sponsorAddress != "" {
		var err error
		sponsorID, err = state.DecodeAccountID(sponsorAddress)
		if err != nil {
			return err
		}
		sponsor, err = state.ReadAccountRoot(view, sponsorID)
		if err != nil {
			return fmt.Errorf("read reserve sponsor: %w", err)
		}
		if sponsor == nil {
			return fmt.Errorf("read reserve sponsor: account does not exist")
		}
		sponsorCurrent = ownerCounts(sponsor)
	}
	account.OwnerCount = ConfineOwnerCount(account.OwnerCount, -int(count))
	if sponsor != nil {
		account.SponsoredOwnerCount = ConfineOwnerCount(account.SponsoredOwnerCount, -int(count))
		sponsor.SponsoringOwnerCount = ConfineOwnerCount(sponsor.SponsoringOwnerCount, -int(count))
	}
	if hook, ok := view.(ownerCountsAdjuster); ok {
		if accountID, err := state.DecodeAccountID(account.Account); err == nil {
			hook.AdjustOwnerCounts(accountID, accountCurrent, ownerCounts(account))
		}
		if sponsor != nil {
			hook.AdjustOwnerCounts(sponsorID, sponsorCurrent, ownerCounts(sponsor))
		}
	} else if hook, ok := view.(ownerCountAdjuster); ok {
		if accountID, err := state.DecodeAccountID(account.Account); err == nil {
			hook.AdjustOwnerCount(accountID, accountCurrent.Owner, account.OwnerCount)
		}
		if sponsor != nil {
			hook.AdjustOwnerCount(sponsorID, sponsorCurrent.Owner, sponsor.OwnerCount)
		}
	}
	if sponsor == nil {
		return nil
	}
	return updateAccountOnView(view, sponsorID, sponsor)
}

// DecreaseOwnerCountFor reverses an object's physical and sponsored owner
// counts, updating the transaction source in memory and other owners through
// the ledger view.
func DecreaseOwnerCountFor(ctx *ApplyContext, accountID [20]byte, sponsorAddress string, count uint32) ter.Result {
	if ctx == nil {
		return ter.TefINTERNAL
	}
	if accountID == ctx.AccountID {
		if err := DecreaseOwnerCount(ctx.View, ctx.Account, sponsorAddress, count); err != nil {
			return ctx.Internal("DecreaseOwnerCount", err)
		}
		ctx.SyncSenderSponsorCounts(sponsorAddress)
		return ter.TesSUCCESS
	}
	account, err := ReadAccountRoot(ctx.View, accountID)
	if err != nil {
		return ctx.Internal("DecreaseOwnerCount.ReadOwner", err)
	}
	if account == nil {
		return ter.TefBAD_LEDGER
	}
	if err := DecreaseOwnerCount(ctx.View, account, sponsorAddress, count); err != nil {
		return ctx.Internal("DecreaseOwnerCount", err)
	}
	ctx.SyncSenderSponsorCounts(sponsorAddress)
	if err := updateAccountOnView(ctx.View, accountID, account); err != nil {
		return ctx.Internal("DecreaseOwnerCount.UpdateOwner", err)
	}
	return ter.TesSUCCESS
}

// DecreaseOwnerCountOnView always persists both the owner and sponsor account
// changes through view. It is used by cleanup that may survive a tec reset.
func DecreaseOwnerCountOnView(view state.LedgerView, accountID [20]byte, sponsorAddress string, count uint32) error {
	account, err := state.ReadAccountRoot(view, accountID)
	if err != nil {
		return err
	}
	if account == nil {
		return fmt.Errorf("owner account does not exist")
	}
	if err := DecreaseOwnerCount(view, account, sponsorAddress, count); err != nil {
		return err
	}
	return updateAccountOnView(view, accountID, account)
}

// LedgerEntrySponsor returns the AccountID stored in a ledger entry sponsor
// field. An absent field is not an error.
func LedgerEntrySponsor(data []byte, field string) (string, error) {
	fields, err := binarycodec.DecodeBytes(data)
	if err != nil {
		return "", err
	}
	value, ok := fields[field]
	if !ok {
		return "", nil
	}
	sponsor, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s is not an AccountID", field)
	}
	return sponsor, nil
}

// LedgerEntrySponsorFromView reads a ledger entry and returns its sponsor.
func LedgerEntrySponsorFromView(view LedgerView, object keylet.Keylet, field string) (string, error) {
	data, err := view.Read(object)
	if err != nil {
		return "", err
	}
	if data == nil {
		return "", fmt.Errorf("sponsored ledger entry does not exist")
	}
	return LedgerEntrySponsor(data, field)
}

// SetLedgerEntrySponsor sets or removes a ledger entry sponsor field while
// preserving every other serialized field.
func SetLedgerEntrySponsor(data []byte, field, sponsor string) ([]byte, error) {
	fields, err := binarycodec.DecodeBytes(data)
	if err != nil {
		return nil, err
	}
	if sponsor == "" {
		delete(fields, field)
	} else {
		fields[field] = sponsor
	}
	return binarycodec.EncodeBytes(fields)
}
