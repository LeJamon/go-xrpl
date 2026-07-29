package sponsor

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// SponsorshipTransfer moves reserve responsibility for an account or one of
// its supported owned ledger objects.
type SponsorshipTransfer struct {
	tx.BaseTx

	ObjectID string `json:"ObjectID,omitempty" xrpl:"ObjectID,omitempty"`
	Sponsee  string `json:"Sponsee,omitempty" xrpl:"Sponsee,omitempty"`
}

func NewSponsorshipTransfer(account string) *SponsorshipTransfer {
	return &SponsorshipTransfer{BaseTx: *tx.NewBaseTx(tx.TypeSponsorshipTransfer, account)}
}

func (s *SponsorshipTransfer) TxType() tx.Type { return tx.TypeSponsorshipTransfer }

func (s *SponsorshipTransfer) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureSponsor}
}

func (s *SponsorshipTransfer) GetFlagsMask(*amendment.Rules) uint32 {
	return ^sponsorshipTransferValidFlags
}

func (s *SponsorshipTransfer) Flatten() (map[string]any, error) { return tx.ReflectFlatten(s) }

func (s *SponsorshipTransfer) Validate() error {
	if err := s.BaseTx.Validate(); err != nil {
		return err
	}
	flags := s.GetCommon().GetFlags()
	transferFlags := flags & (SponsorshipTransferFlagCreate | SponsorshipTransferFlagReassign | SponsorshipTransferFlagEnd)
	if transferFlags == 0 || transferFlags&(transferFlags-1) != 0 {
		return ter.Errorf(ter.TemINVALID_FLAG, "exactly one SponsorshipTransfer operation is required")
	}
	if s.ObjectID != "" {
		if _, err := parseObjectID(s.ObjectID); err != nil {
			return ter.Errorf(ter.TemMALFORMED, "%v", err)
		}
	}

	isCreateOrReassign := flags&(SponsorshipTransferFlagCreate|SponsorshipTransferFlagReassign) != 0
	if isCreateOrReassign {
		if s.GetCommon().Sponsor == "" {
			return ter.Errorf(ter.TemMALFORMED, "Sponsor is required")
		}
		if s.GetCommon().SponsorFlags == nil || *s.GetCommon().SponsorFlags&tx.SpfSponsorReserve == 0 {
			return ter.Errorf(ter.TemINVALID_FLAG, "reserve sponsorship is required")
		}
		if s.Sponsee != "" {
			return ter.Errorf(ter.TemMALFORMED, "Sponsee is not allowed when creating or reassigning")
		}
		if s.ObjectID == "" && s.GetCommon().SponsorSignature == nil {
			return ter.Errorf(ter.TemMALFORMED, "account sponsorship requires SponsorSignature")
		}
		return nil
	}

	if s.GetCommon().Sponsor != "" || s.GetCommon().SponsorFlags != nil {
		return ter.Errorf(ter.TemMALFORMED, "ending sponsorship cannot define a new sponsor")
	}
	if s.Sponsee != "" && s.Sponsee == s.Account {
		return ter.Errorf(ter.TemMALFORMED, "explicit Sponsee must differ from Account")
	}
	return nil
}

func (s *SponsorshipTransfer) sponsee() (string, [20]byte, ter.Result) {
	account := s.Account
	if s.Sponsee != "" {
		account = s.Sponsee
	}
	accountID, err := state.DecodeAccountID(account)
	if err != nil {
		return account, accountID, ter.TerNO_ACCOUNT
	}
	return account, accountID, ter.TesSUCCESS
}

func (s *SponsorshipTransfer) Preclaim(view tx.LedgerView, _ tx.EngineConfig) ter.Result {
	if result := commonSponsorPermission(view, s.GetCommon()); result != ter.TesSUCCESS {
		return result
	}

	sponsee, sponseeID, result := s.sponsee()
	if result != ter.TesSUCCESS {
		return result
	}
	sponseeAccount, result := readAccount(view, sponseeID)
	if result != ter.TesSUCCESS {
		if s.Sponsee != "" && result == ter.TerNO_ACCOUNT {
			return ter.TerNO_ACCOUNT
		}
		return ter.TecINTERNAL
	}

	targetSponsored := sponseeAccount.HasSponsor
	currentSponsor := sponseeAccount.Sponsor
	if s.ObjectID != "" {
		objectID, err := parseObjectID(s.ObjectID)
		if err != nil {
			return ter.TemMALFORMED
		}
		target, targetResult := readSponsoredTarget(view, objectID, sponseeID, sponsee)
		if targetResult != ter.TesSUCCESS {
			return targetResult
		}
		currentSponsor, targetSponsored = target.sponsor()
	}

	flags := s.GetCommon().GetFlags()
	if flags&SponsorshipTransferFlagCreate != 0 {
		if s.GetCommon().Sponsor == "" || targetSponsored {
			return ter.TecNO_PERMISSION
		}
	} else if flags&SponsorshipTransferFlagReassign != 0 {
		if s.GetCommon().Sponsor == "" || !targetSponsored || currentSponsor == s.GetCommon().Sponsor {
			return ter.TecNO_PERMISSION
		}
	} else {
		if targetSponsored == false {
			return ter.TecNO_PERMISSION
		}
		if s.Account != currentSponsor && s.Account != sponsee {
			return ter.TecNO_PERMISSION
		}
	}
	return ter.TesSUCCESS
}

func (s *SponsorshipTransfer) Apply(ctx *tx.ApplyContext) ter.Result {
	sponsee, sponseeID, result := s.sponsee()
	if result != ter.TesSUCCESS {
		return ter.TefINTERNAL
	}
	sponseeAccount, result := accountForApply(ctx, sponseeID)
	if result != ter.TesSUCCESS {
		return ter.TefINTERNAL
	}

	if s.ObjectID != "" {
		objectID, err := parseObjectID(s.ObjectID)
		if err != nil {
			return ter.TefINTERNAL
		}
		target, targetResult := readSponsoredTarget(ctx.View, objectID, sponseeID, sponsee)
		if targetResult != ter.TesSUCCESS {
			return ter.TefINTERNAL
		}
		return s.applyObject(ctx, sponseeID, sponseeAccount, target)
	}
	return s.applyAccount(ctx, sponseeID, sponseeAccount)
}

func (s *SponsorshipTransfer) applyObject(
	ctx *tx.ApplyContext,
	sponseeID [20]byte,
	sponsee *state.AccountRoot,
	target *sponsoredTarget,
) ter.Result {
	flags := s.GetCommon().GetFlags()
	isCreate := flags&SponsorshipTransferFlagCreate != 0
	isReassign := flags&SponsorshipTransferFlagReassign != 0
	if isCreate || isReassign {
		newSponsorID, err := state.DecodeAccountID(s.GetCommon().Sponsor)
		if err != nil {
			return ter.TefINTERNAL
		}
		newSponsor, result := accountForApply(ctx, newSponsorID)
		if result != ter.TesSUCCESS {
			return ter.TefINTERNAL
		}
		if result := checkNewSponsorReserve(ctx.View, ctx.Config, newSponsorID, sponseeID, newSponsor, target.ownerCount, 0); result != ter.TesSUCCESS {
			return result
		}

		if isCreate {
			if !incrementCount(&sponsee.SponsoredOwnerCount, target.ownerCount) {
				return ter.TecINTERNAL
			}
		} else {
			oldSponsorAddress, ok := target.sponsor()
			if !ok {
				return ter.TefINTERNAL
			}
			oldSponsorID, err := state.DecodeAccountID(oldSponsorAddress)
			if err != nil {
				return ter.TefINTERNAL
			}
			oldSponsor, result := accountForApply(ctx, oldSponsorID)
			if result != ter.TesSUCCESS || !decrementCount(&oldSponsor.SponsoringOwnerCount, target.ownerCount) {
				return ter.TecINTERNAL
			}
			if result := writeAccount(ctx, oldSponsorID, oldSponsor); result != ter.TesSUCCESS {
				return result
			}
		}

		if !incrementCount(&newSponsor.SponsoringOwnerCount, target.ownerCount) {
			return ter.TecINTERNAL
		}
		encoded, err := target.encodeWithSponsor(s.GetCommon().Sponsor)
		if err != nil {
			return ctx.Internal("encode sponsored object", err)
		}
		if err := ctx.View.Update(target.key, encoded); err != nil {
			return ctx.Internal("update sponsored object", err)
		}
		if result := consumePrefundedReserve(ctx.View, newSponsorID, sponseeID, target.ownerCount); result != ter.TesSUCCESS {
			return result
		}
		if result := writeAccount(ctx, sponseeID, sponsee); result != ter.TesSUCCESS {
			return result
		}
		return writeAccount(ctx, newSponsorID, newSponsor)
	}

	oldSponsorAddress, ok := target.sponsor()
	if !ok {
		return ter.TefINTERNAL
	}
	oldSponsorID, err := state.DecodeAccountID(oldSponsorAddress)
	if err != nil {
		return ter.TefINTERNAL
	}
	oldSponsor, result := accountForApply(ctx, oldSponsorID)
	if result != ter.TesSUCCESS {
		return ter.TefINTERNAL
	}
	if !decrementCount(&sponsee.SponsoredOwnerCount, target.ownerCount) ||
		!decrementCount(&oldSponsor.SponsoringOwnerCount, target.ownerCount) {
		return ter.TecINTERNAL
	}
	encoded, err := target.encodeWithSponsor("")
	if err != nil {
		return ctx.Internal("encode unsponsored object", err)
	}
	if err := ctx.View.Update(target.key, encoded); err != nil {
		return ctx.Internal("update unsponsored object", err)
	}
	if result := writeAccount(ctx, sponseeID, sponsee); result != ter.TesSUCCESS {
		return result
	}
	return writeAccount(ctx, oldSponsorID, oldSponsor)
}

func (s *SponsorshipTransfer) applyAccount(
	ctx *tx.ApplyContext,
	sponseeID [20]byte,
	sponsee *state.AccountRoot,
) ter.Result {
	flags := s.GetCommon().GetFlags()
	isCreate := flags&SponsorshipTransferFlagCreate != 0
	isReassign := flags&SponsorshipTransferFlagReassign != 0
	if isCreate || isReassign {
		newSponsorID, err := state.DecodeAccountID(s.GetCommon().Sponsor)
		if err != nil {
			return ter.TefINTERNAL
		}
		newSponsor, result := accountForApply(ctx, newSponsorID)
		if result != ter.TesSUCCESS {
			return ter.TefINTERNAL
		}
		if result := checkNewSponsorReserve(ctx.View, ctx.Config, newSponsorID, sponseeID, newSponsor, 0, 1); result != ter.TesSUCCESS {
			return result
		}
		if isReassign {
			oldSponsorID, err := state.DecodeAccountID(sponsee.Sponsor)
			if err != nil {
				return ter.TefINTERNAL
			}
			oldSponsor, result := accountForApply(ctx, oldSponsorID)
			if result != ter.TesSUCCESS || !decrementCount(&oldSponsor.SponsoringAccountCount, 1) {
				return ter.TecINTERNAL
			}
			if result := writeAccount(ctx, oldSponsorID, oldSponsor); result != ter.TesSUCCESS {
				return result
			}
		}
		if !incrementCount(&newSponsor.SponsoringAccountCount, 1) {
			return ter.TecINTERNAL
		}
		sponsee.Sponsor = s.GetCommon().Sponsor
		sponsee.HasSponsor = true
		if result := writeAccount(ctx, sponseeID, sponsee); result != ter.TesSUCCESS {
			return result
		}
		return writeAccount(ctx, newSponsorID, newSponsor)
	}

	oldSponsorID, err := state.DecodeAccountID(sponsee.Sponsor)
	if err != nil {
		return ter.TefINTERNAL
	}
	oldSponsor, result := accountForApply(ctx, oldSponsorID)
	if result != ter.TesSUCCESS {
		return ter.TefINTERNAL
	}
	balanceBeforeFee := sponsee.Balance
	if sponseeID == ctx.AccountID {
		balanceBeforeFee = ctx.PriorBalance()
	}
	if result := checkAccountReserve(ctx.Config, sponsee, balanceBeforeFee, 0, 1, ter.TecINSUFFICIENT_RESERVE); result != ter.TesSUCCESS {
		return result
	}
	if !decrementCount(&oldSponsor.SponsoringAccountCount, 1) {
		return ter.TecINTERNAL
	}
	sponsee.Sponsor = ""
	sponsee.HasSponsor = false
	if result := writeAccount(ctx, sponseeID, sponsee); result != ter.TesSUCCESS {
		return result
	}
	return writeAccount(ctx, oldSponsorID, oldSponsor)
}
