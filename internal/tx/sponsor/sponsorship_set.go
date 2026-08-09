package sponsor

import (
	"errors"
	"math"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// SponsorshipSet creates, updates, or deletes a directional sponsorship
// agreement. CounterpartySponsor is used when the submitter is the sponsee;
// Sponsee is used when the submitter is the sponsor.
type SponsorshipSet struct {
	tx.BaseTx

	CounterpartySponsor      string     `json:"CounterpartySponsor,omitempty" xrpl:"CounterpartySponsor,omitempty"`
	Sponsee                  string     `json:"Sponsee,omitempty" xrpl:"Sponsee,omitempty"`
	FeeAmountDelta           *tx.Amount `json:"FeeAmountDelta,omitempty" xrpl:"FeeAmountDelta,omitempty,amount"`
	MaxFee                   *tx.Amount `json:"MaxFee,omitempty" xrpl:"MaxFee,omitempty,amount"`
	RemainingOwnerCountDelta *int32     `json:"RemainingOwnerCountDelta,omitempty" xrpl:"RemainingOwnerCountDelta,omitempty"`
}

func totalRemainingOwnerCount(object *state.SponsorshipData, delta *int32) int64 {
	current := int64(0)
	if object != nil {
		current = int64(object.RemainingOwnerCount)
	}
	if delta != nil {
		current += int64(*delta)
	}
	return current
}

func applyFeeAmountDelta(current uint64, delta int64) (uint64, bool) {
	if delta >= 0 {
		increase := uint64(delta)
		if increase > math.MaxUint64-current {
			return 0, false
		}
		return current + increase, true
	}
	decrease := uint64(-(delta + 1)) + 1
	if decrease >= current {
		return 0, true
	}
	return current - decrease, true
}

func hasSponsorshipBudget(
	object *state.SponsorshipData,
	feeAmountDelta *tx.Amount,
	remainingOwnerCountDelta *int32,
) bool {
	if object == nil {
		if feeAmountDelta != nil && feeAmountDelta.Signum() <= 0 {
			return false
		}
		if remainingOwnerCountDelta != nil && *remainingOwnerCountDelta <= 0 {
			return false
		}
	}

	feeAmount := uint64(0)
	if object != nil && object.HasFeeAmount {
		feeAmount = object.FeeAmount
	}
	if feeAmountDelta != nil {
		var ok bool
		feeAmount, ok = applyFeeAmountDelta(feeAmount, feeAmountDelta.Drops())
		if !ok {
			return false
		}
	}
	return feeAmount > 0 || totalRemainingOwnerCount(object, remainingOwnerCountDelta) > 0
}

func NewSponsorshipSet(account string) *SponsorshipSet {
	return &SponsorshipSet{BaseTx: *tx.NewBaseTx(tx.TypeSponsorshipSet, account)}
}

func (s *SponsorshipSet) TxType() tx.Type { return tx.TypeSponsorshipSet }

func (s *SponsorshipSet) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureSponsor}
}

func (s *SponsorshipSet) GetFlagsMask(*amendment.Rules) uint32 {
	return ^sponsorshipSetValidFlags
}

func (s *SponsorshipSet) Flatten() (map[string]any, error) { return tx.ReflectFlatten(s) }

func (s *SponsorshipSet) Validate() error {
	if err := s.BaseTx.Validate(); err != nil {
		return err
	}
	flags := s.GetCommon().GetFlags()
	if flags&SponsorshipSetFlagRequireSignForFee != 0 && flags&SponsorshipSetFlagClearRequireSignForFee != 0 {
		return ter.Errorf(ter.TemINVALID_FLAG, "cannot set and clear fee-signature requirement")
	}
	if flags&SponsorshipSetFlagRequireSignForReserve != 0 && flags&SponsorshipSetFlagClearRequireSignForReserve != 0 {
		return ter.Errorf(ter.TemINVALID_FLAG, "cannot set and clear reserve-signature requirement")
	}

	hasSponsor := s.CounterpartySponsor != "" || s.GetCommon().HasField("CounterpartySponsor")
	hasSponsee := s.Sponsee != "" || s.GetCommon().HasField("Sponsee")
	if hasSponsor == hasSponsee {
		return ter.Errorf(ter.TemMALFORMED, "exactly one of CounterpartySponsor and Sponsee is required")
	}
	sponsor := s.CounterpartySponsor
	sponsee := s.Account
	if hasSponsee {
		sponsor = s.Account
		sponsee = s.Sponsee
	}
	if sponsor == sponsee {
		return ter.Errorf(ter.TemMALFORMED, "sponsor and sponsee must differ")
	}

	if flags&SponsorshipSetFlagDelete != 0 {
		modifyFlags := SponsorshipSetFlagRequireSignForFee |
			SponsorshipSetFlagClearRequireSignForFee |
			SponsorshipSetFlagRequireSignForReserve |
			SponsorshipSetFlagClearRequireSignForReserve
		if flags&modifyFlags != 0 {
			return ter.Errorf(ter.TemINVALID_FLAG, "delete cannot modify sponsorship flags")
		}
		if s.FeeAmountDelta != nil || s.MaxFee != nil || s.RemainingOwnerCountDelta != nil {
			return ter.Errorf(ter.TemMALFORMED, "delete cannot modify sponsorship budget")
		}
		return nil
	}

	if s.Account != sponsor {
		return ter.Errorf(ter.TemMALFORMED, "only the sponsor may create or update")
	}
	if s.FeeAmountDelta != nil && (!s.FeeAmountDelta.IsNative() || s.FeeAmountDelta.IsZero()) {
		return ter.Errorf(ter.TemBAD_AMOUNT, "FeeAmountDelta must be a non-zero XRP amount")
	}
	if s.MaxFee != nil && (!s.MaxFee.IsNative() || s.MaxFee.IsNegative()) {
		return ter.Errorf(ter.TemBAD_AMOUNT, "MaxFee must be a non-negative XRP amount")
	}
	if s.RemainingOwnerCountDelta != nil && *s.RemainingOwnerCountDelta == 0 {
		return ter.Errorf(ter.TemINVALID, "RemainingOwnerCountDelta must be non-zero")
	}
	if s.FeeAmountDelta == nil && s.MaxFee == nil && s.RemainingOwnerCountDelta == nil && flags&tx.TfUniversalMask == 0 {
		return ter.Errorf(ter.TemREDUNDANT, "no sponsorship changes specified")
	}
	return nil
}

func (s *SponsorshipSet) parties() (sponsorID, sponseeID [20]byte, result ter.Result) {
	sponsor := s.CounterpartySponsor
	sponsee := s.Account
	if s.Sponsee != "" || s.GetCommon().HasField("Sponsee") {
		sponsor = s.Account
		sponsee = s.Sponsee
	}
	var err error
	sponsorID, err = state.DecodeAccountID(sponsor)
	if err != nil {
		return sponsorID, sponseeID, ter.TemMALFORMED
	}
	sponseeID, err = state.DecodeAccountID(sponsee)
	if err != nil {
		return sponsorID, sponseeID, ter.TemMALFORMED
	}
	return sponsorID, sponseeID, ter.TesSUCCESS
}

func (s *SponsorshipSet) Preclaim(view tx.LedgerView, _ tx.EngineConfig) ter.Result {
	sponsorID, sponseeID, result := s.parties()
	if result != ter.TesSUCCESS {
		return result
	}
	sponsor, result := readAccount(view, sponsorID)
	if result != ter.TesSUCCESS {
		if result == ter.TerNO_ACCOUNT {
			return ter.TecNO_DST
		}
		return result
	}
	sponsee, result := readAccount(view, sponseeID)
	if result != ter.TesSUCCESS {
		if result == ter.TerNO_ACCOUNT {
			return ter.TecNO_DST
		}
		return result
	}
	if sponsor.IsPseudoAccount() || sponsee.IsPseudoAccount() {
		return ter.TecPSEUDO_ACCOUNT
	}

	existing, exists, result := loadSponsorship(view, sponsorID, sponseeID)
	if result != ter.TesSUCCESS {
		return result
	}
	if s.GetCommon().GetFlags()&SponsorshipSetFlagDelete != 0 {
		if !exists {
			return ter.TecNO_ENTRY
		}
		return ter.TesSUCCESS
	}

	if totalRemainingOwnerCount(existing, s.RemainingOwnerCountDelta) > math.MaxUint32 {
		return ter.TecLIMIT_EXCEEDED
	}
	if !hasSponsorshipBudget(existing, s.FeeAmountDelta, s.RemainingOwnerCountDelta) {
		return ter.TecNO_PERMISSION
	}
	return ter.TesSUCCESS
}

func (s *SponsorshipSet) Apply(ctx *tx.ApplyContext) ter.Result {
	sponsorID, sponseeID, result := s.parties()
	if result != ter.TesSUCCESS {
		return ter.TefINTERNAL
	}
	sponsor, result := accountForApply(ctx, sponsorID)
	if result != ter.TesSUCCESS {
		return ter.TefINTERNAL
	}
	if sponsee, result := accountForApply(ctx, sponseeID); result != ter.TesSUCCESS || sponsee == nil {
		return ter.TefINTERNAL
	}

	objectKey := keylet.Sponsorship(sponsorID, sponseeID)
	existing, exists, result := loadSponsorship(ctx.View, sponsorID, sponseeID)
	if result != ter.TesSUCCESS {
		return result
	}
	if s.GetCommon().GetFlags()&SponsorshipSetFlagDelete != 0 {
		if !exists {
			return ter.TecINTERNAL
		}
		return s.delete(ctx, objectKey, sponsorID, sponseeID, sponsor, existing)
	}
	if !exists {
		return s.create(ctx, objectKey, sponsorID, sponseeID, sponsor)
	}
	return s.update(ctx, objectKey, sponsorID, sponsor, existing)
}

func (s *SponsorshipSet) create(
	ctx *tx.ApplyContext,
	objectKey keylet.Keylet,
	sponsorID, sponseeID [20]byte,
	sponsor *state.AccountRoot,
) ter.Result {
	feeAmount := uint64(0)
	if s.FeeAmountDelta != nil && s.FeeAmountDelta.Signum() > 0 {
		feeAmount = uint64(s.FeeAmountDelta.Drops())
	}
	if feeAmount > sponsor.Balance {
		return ter.TecUNFUNDED
	}
	balanceAfterFee := sponsor.Balance - feeAmount
	if result := checkAccountReserve(ctx.Config, sponsor, balanceAfterFee, 1, 0, ter.TecUNFUNDED); result != ter.TesSUCCESS {
		return result
	}

	object := &state.SponsorshipData{
		Owner:         sponsorID,
		Sponsee:       sponseeID,
		Flags:         s.initialLedgerFlags(),
		PreviousTxnID: [32]byte{},
	}
	if feeAmount > 0 {
		object.FeeAmount = feeAmount
		object.HasFeeAmount = true
	}
	if s.MaxFee != nil && s.MaxFee.Signum() > 0 {
		object.MaxFee = uint64(s.MaxFee.Drops())
		object.HasMaxFee = true
	}
	if s.RemainingOwnerCountDelta != nil && *s.RemainingOwnerCountDelta > 0 {
		object.RemainingOwnerCount = uint32(*s.RemainingOwnerCountDelta)
	}

	ownerInsert, err := state.DirInsert(ctx.View, keylet.OwnerDir(sponsorID), objectKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = sponsorID
	})
	if err != nil {
		if errors.Is(err, state.ErrDirFull) {
			return ter.TecDIR_FULL
		}
		return ctx.Internal("insert sponsor owner directory", err)
	}
	object.OwnerNode = ownerInsert.Page
	sponseeInsert, err := state.DirInsert(ctx.View, keylet.OwnerDir(sponseeID), objectKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = sponseeID
	})
	if err != nil {
		if errors.Is(err, state.ErrDirFull) {
			return ter.TecDIR_FULL
		}
		return ctx.Internal("insert sponsee owner directory", err)
	}
	object.SponseeNode = sponseeInsert.Page

	data, err := state.SerializeSponsorship(object)
	if err != nil {
		return ctx.Internal("SerializeSponsorship(create)", err)
	}
	if err := ctx.View.Insert(objectKey, data); err != nil {
		return ctx.Internal("insert Sponsorship", err)
	}
	sponsor.Balance = balanceAfterFee
	sponsor.OwnerCount = tx.ConfineOwnerCount(sponsor.OwnerCount, 1)
	return writeAccount(ctx, sponsorID, sponsor)
}

func (s *SponsorshipSet) update(
	ctx *tx.ApplyContext,
	objectKey keylet.Keylet,
	sponsorID [20]byte,
	sponsor *state.AccountRoot,
	object *state.SponsorshipData,
) ter.Result {
	if s.FeeAmountDelta != nil {
		oldAmount := uint64(0)
		if object.HasFeeAmount {
			oldAmount = object.FeeAmount
		}
		newAmount, ok := applyFeeAmountDelta(oldAmount, s.FeeAmountDelta.Drops())
		if !ok {
			return ter.TecLIMIT_EXCEEDED
		}
		if newAmount != oldAmount {
			balanceAfter := sponsor.Balance
			if newAmount > oldAmount {
				delta := newAmount - oldAmount
				if delta > balanceAfter {
					return ter.TecUNFUNDED
				}
				balanceAfter -= delta
			} else {
				refund := oldAmount - newAmount
				if refund > math.MaxUint64-balanceAfter {
					return ter.TecLIMIT_EXCEEDED
				}
				balanceAfter += refund
			}
			if result := checkAccountReserve(ctx.Config, sponsor, balanceAfter, 0, 0, ter.TecUNFUNDED); result != ter.TesSUCCESS {
				return result
			}
			sponsor.Balance = balanceAfter
			object.FeeAmount = newAmount
			object.HasFeeAmount = newAmount > 0
		}
	}
	if s.MaxFee != nil {
		object.MaxFee = uint64(s.MaxFee.Drops())
		object.HasMaxFee = object.MaxFee > 0
	}
	if s.RemainingOwnerCountDelta != nil {
		newCount := totalRemainingOwnerCount(object, s.RemainingOwnerCountDelta)
		if newCount > math.MaxUint32 {
			return ter.TecLIMIT_EXCEEDED
		}
		if newCount <= 0 {
			object.RemainingOwnerCount = 0
		} else {
			object.RemainingOwnerCount = uint32(newCount)
		}
	}

	flags := s.GetCommon().GetFlags()
	if flags&SponsorshipSetFlagRequireSignForFee != 0 {
		object.Flags |= entry.LsfSponsorshipRequireSignForFee
	}
	if flags&SponsorshipSetFlagClearRequireSignForFee != 0 {
		object.Flags &^= entry.LsfSponsorshipRequireSignForFee
	}
	if flags&SponsorshipSetFlagRequireSignForReserve != 0 {
		object.Flags |= entry.LsfSponsorshipRequireSignForReserve
	}
	if flags&SponsorshipSetFlagClearRequireSignForReserve != 0 {
		object.Flags &^= entry.LsfSponsorshipRequireSignForReserve
	}

	data, err := state.SerializeSponsorship(object)
	if err != nil {
		return ctx.Internal("SerializeSponsorship(update)", err)
	}
	if err := ctx.View.Update(objectKey, data); err != nil {
		return ctx.Internal("update Sponsorship", err)
	}
	return writeAccount(ctx, sponsorID, sponsor)
}

func (s *SponsorshipSet) delete(
	ctx *tx.ApplyContext,
	objectKey keylet.Keylet,
	sponsorID, sponseeID [20]byte,
	sponsor *state.AccountRoot,
	object *state.SponsorshipData,
) ter.Result {
	ownerRemoved, err := state.DirRemove(ctx.View, keylet.OwnerDir(sponsorID), object.OwnerNode, objectKey.Key, false)
	if err != nil || ownerRemoved == nil || !ownerRemoved.Success {
		return ter.TefBAD_LEDGER
	}
	sponseeRemoved, err := state.DirRemove(ctx.View, keylet.OwnerDir(sponseeID), object.SponseeNode, objectKey.Key, false)
	if err != nil || sponseeRemoved == nil || !sponseeRemoved.Success {
		return ter.TefBAD_LEDGER
	}
	if sponsor.OwnerCount > 0 {
		sponsor.OwnerCount--
	}
	if object.HasFeeAmount {
		sponsor.Balance += object.FeeAmount
	}
	if err := ctx.View.Erase(objectKey); err != nil {
		return ctx.Internal("erase Sponsorship", err)
	}
	return writeAccount(ctx, sponsorID, sponsor)
}

func (s *SponsorshipSet) initialLedgerFlags() uint32 {
	flags := uint32(0)
	if s.GetCommon().GetFlags()&SponsorshipSetFlagRequireSignForFee != 0 {
		flags |= entry.LsfSponsorshipRequireSignForFee
	}
	if s.GetCommon().GetFlags()&SponsorshipSetFlagRequireSignForReserve != 0 {
		flags |= entry.LsfSponsorshipRequireSignForReserve
	}
	return flags
}
