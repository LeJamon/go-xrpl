package sponsor

import (
	"encoding/hex"
	"errors"
	"math"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

func parseObjectID(value string) ([32]byte, error) {
	var id [32]byte
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(id) {
		return id, errors.New("ObjectID must be a 256-bit hex value")
	}
	copy(id[:], decoded)
	return id, nil
}

func readAccount(view tx.ReadOnlyLedgerView, accountID [20]byte) (*state.AccountRoot, ter.Result) {
	account, err := tx.ReadAccountRoot(view, accountID)
	if err != nil {
		return nil, ter.TefINTERNAL
	}
	if account == nil {
		return nil, ter.TerNO_ACCOUNT
	}
	return account, ter.TesSUCCESS
}

func accountForApply(ctx *tx.ApplyContext, accountID [20]byte) (*state.AccountRoot, ter.Result) {
	if accountID == ctx.AccountID {
		return ctx.Account, ter.TesSUCCESS
	}
	return readAccount(ctx.View, accountID)
}

func writeAccount(ctx *tx.ApplyContext, accountID [20]byte, account *state.AccountRoot) ter.Result {
	if accountID == ctx.AccountID {
		return ter.TesSUCCESS
	}
	return ctx.UpdateAccountRoot(accountID, account)
}

func loadSponsorship(view tx.ReadOnlyLedgerView, sponsorID, sponseeID [20]byte) (*state.SponsorshipData, bool, ter.Result) {
	data, err := view.Read(keylet.Sponsorship(sponsorID, sponseeID))
	if err != nil {
		return nil, false, ter.TefINTERNAL
	}
	if data == nil {
		return nil, false, ter.TesSUCCESS
	}
	sponsorship, err := state.ParseSponsorship(data)
	if err != nil {
		return nil, false, ter.TefINTERNAL
	}
	return sponsorship, true, ter.TesSUCCESS
}

func commonSponsorPermission(view tx.ReadOnlyLedgerView, common *tx.Common) ter.Result {
	if common.Sponsor == "" {
		return ter.TesSUCCESS
	}

	sponsorID, err := state.DecodeAccountID(common.Sponsor)
	if err != nil {
		return ter.TerNO_ACCOUNT
	}
	if common.Delegate != "" && common.SponsorFlags != nil && *common.SponsorFlags&tx.SpfSponsorReserve != 0 {
		return ter.TemINVALID
	}
	if sponsor, result := readAccount(view, sponsorID); result != ter.TesSUCCESS || sponsor == nil {
		return result
	}
	if common.SponsorSignature != nil {
		return ter.TesSUCCESS
	}

	initiator := common.Account
	if common.Delegate != "" {
		initiator = common.Delegate
	}
	initiatorID, err := state.DecodeAccountID(initiator)
	if err != nil {
		return ter.TerNO_PERMISSION
	}
	sponsorship, exists, result := loadSponsorship(view, sponsorID, initiatorID)
	if result != ter.TesSUCCESS {
		return result
	}
	if !exists {
		return ter.TerNO_PERMISSION
	}
	flags := uint32(0)
	if common.SponsorFlags != nil {
		flags = *common.SponsorFlags
	}
	if flags&tx.SpfSponsorFee != 0 && sponsorship.Flags&entry.LsfSponsorshipRequireSignForFee != 0 {
		return ter.TerNO_PERMISSION
	}
	if flags&tx.SpfSponsorReserve != 0 && sponsorship.Flags&entry.LsfSponsorshipRequireSignForReserve != 0 {
		return ter.TerNO_PERMISSION
	}
	return ter.TesSUCCESS
}

func effectiveOwnerCount(account *state.AccountRoot, delta uint32) (uint32, bool) {
	if account.SponsoredOwnerCount > account.OwnerCount {
		return 0, false
	}
	adjustment := int64(delta) - int64(account.SponsoredOwnerCount) +
		int64(account.SponsoringOwnerCount)
	if adjustment > math.MaxInt32 {
		adjustment = math.MaxInt32
	} else if adjustment < math.MinInt32 {
		adjustment = math.MinInt32
	}
	return tx.ConfineOwnerCount(account.OwnerCount, int(adjustment)), true
}

func effectiveAccountCount(account *state.AccountRoot, delta uint32) uint32 {
	base := uint64(1)
	if account.HasSponsor {
		base = 0
	}
	total := base + uint64(account.SponsoringAccountCount) + uint64(delta)
	if total > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(total)
}

func reserveRequired(config tx.EngineConfig, account *state.AccountRoot, ownerDelta, accountDelta uint32) (uint64, bool) {
	owners, ok := effectiveOwnerCount(account, ownerDelta)
	if !ok {
		return 0, false
	}
	return config.AccountReserveWithCounts(owners, effectiveAccountCount(account, accountDelta)), true
}

func checkAccountReserve(config tx.EngineConfig, account *state.AccountRoot, balance uint64, ownerDelta, accountDelta uint32, failure ter.Result) ter.Result {
	reserve, ok := reserveRequired(config, account, ownerDelta, accountDelta)
	if !ok {
		return ter.TecINTERNAL
	}
	if balance < reserve {
		return failure
	}
	return ter.TesSUCCESS
}

func checkNewSponsorReserve(
	view tx.LedgerView,
	config tx.EngineConfig,
	sponsorID, sponseeID [20]byte,
	sponsor *state.AccountRoot,
	ownerDelta, accountDelta uint32,
) ter.Result {
	if ownerDelta > 0 {
		sponsorship, exists, result := loadSponsorship(view, sponsorID, sponseeID)
		if result != ter.TesSUCCESS {
			return result
		}
		if exists && sponsorship.RemainingOwnerCount < ownerDelta {
			return ter.TecINSUFFICIENT_RESERVE
		}
	}
	return checkAccountReserve(config, sponsor, sponsor.Balance, ownerDelta, accountDelta, ter.TecINSUFFICIENT_RESERVE)
}

func incrementCount(value *uint32, delta uint32) bool {
	if math.MaxUint32-*value < delta {
		return false
	}
	*value += delta
	return true
}

func decrementCount(value *uint32, delta uint32) bool {
	if *value < delta {
		return false
	}
	*value -= delta
	return true
}

type sponsoredTarget struct {
	key          keylet.Keylet
	model        entry.Entry
	entryType    entry.Type
	sponsorField string
	ownerCount   uint32
}

func readSponsoredTarget(view tx.ReadOnlyLedgerView, objectID [32]byte, sponseeID [20]byte, sponsee string) (*sponsoredTarget, ter.Result) {
	objectKey := keylet.Keylet{Type: entry.TypeAny, Key: objectID}
	data, err := view.Read(objectKey)
	if err != nil {
		return nil, ter.TefINTERNAL
	}
	if data == nil {
		return nil, ter.TecNO_ENTRY
	}

	entryType, err := state.DecodeType(data)
	if err != nil {
		return nil, ter.TefINTERNAL
	}
	if !isSupportedObjectType(entryType) {
		return nil, ter.TecNO_PERMISSION
	}
	model := entry.New(entryType)
	if model == nil {
		return nil, ter.TefINTERNAL
	}
	if err := model.Decode(data); err != nil {
		return nil, ter.TefINTERNAL
	}

	target := &sponsoredTarget{
		key:          objectKey,
		model:        model,
		entryType:    entryType,
		sponsorField: "Sponsor",
		ownerCount:   1,
	}
	if !target.resolveOwner(sponseeID, sponsee) {
		return nil, ter.TecNO_PERMISSION
	}
	return target, ter.TesSUCCESS
}

func isSupportedObjectType(entryType entry.Type) bool {
	switch entryType {
	case entry.TypeCheck,
		entry.TypeEscrow,
		entry.TypePayChannel,
		entry.TypeMPToken,
		entry.TypeDelegate,
		entry.TypeDepositPreauth,
		entry.TypeMPTokenIssuance,
		entry.TypeSignerList,
		entry.TypeCredential,
		entry.TypeRippleState:
		return true
	default:
		return false
	}
}

func (target *sponsoredTarget) resolveOwner(sponseeID [20]byte, sponsee string) bool {
	_ = sponsee
	switch target.entryType {
	case entry.TypeCheck,
		entry.TypeEscrow,
		entry.TypePayChannel,
		entry.TypeMPToken,
		entry.TypeDelegate,
		entry.TypeDepositPreauth:
		model, ok := target.model.(interface{ GetAccount() ([20]byte, error) })
		if !ok {
			return false
		}
		account, err := model.GetAccount()
		return err == nil && account == sponseeID
	case entry.TypeMPTokenIssuance:
		model, ok := target.model.(*entry.MPTokenIssuance)
		if !ok {
			return false
		}
		issuer, err := model.GetIssuer()
		return err == nil && issuer == sponseeID
	case entry.TypeSignerList:
		if target.key.Key != keylet.SignerList(sponseeID).Key {
			return false
		}
		model, ok := target.model.(*entry.SignerList)
		if !ok {
			return false
		}
		flags, err := model.GetFlags()
		if err != nil {
			return false
		}
		if flags&entry.LsfOneOwnerCount == 0 {
			signerEntries, err := model.GetSignerEntries()
			if err != nil {
				return false
			}
			target.ownerCount = 2 + uint32(len(signerEntries))
		}
		return true
	case entry.TypeCredential:
		model, ok := target.model.(*entry.Credential)
		if !ok {
			return false
		}
		owner, err := model.GetIssuer()
		flags, flagsErr := model.GetFlags()
		if flagsErr != nil {
			return false
		}
		if flags&entry.LsfAccepted != 0 {
			owner, err = model.GetSubject()
		}
		return err == nil && owner == sponseeID
	case entry.TypeRippleState:
		model, ok := target.model.(*entry.RippleState)
		if !ok {
			return false
		}
		flags, err := model.GetFlags()
		if err != nil {
			return false
		}
		highLimit, err := model.GetHighLimit()
		if err != nil {
			return false
		}
		lowLimit, err := model.GetLowLimit()
		if err != nil {
			return false
		}
		if flags&entry.LsfHighReserve != 0 && highLimit.Issuer == sponsee {
			target.sponsorField = "HighSponsor"
			return true
		}
		if flags&entry.LsfLowReserve != 0 && lowLimit.Issuer == sponsee {
			target.sponsorField = "LowSponsor"
			return true
		}
	}
	return false
}

func (target *sponsoredTarget) sponsor() (string, bool) {
	var (
		account [20]byte
		has     bool
		err     error
	)
	switch model := target.model.(type) {
	case *entry.RippleState:
		switch target.sponsorField {
		case "HighSponsor":
			has = model.HasHighSponsor()
			if has {
				account, err = model.GetHighSponsor()
			}
		case "LowSponsor":
			has = model.HasLowSponsor()
			if has {
				account, err = model.GetLowSponsor()
			}
		}
	default:
		value, ok := target.model.(interface {
			HasSponsor() bool
			GetSponsor() ([20]byte, error)
		})
		if !ok {
			return "", false
		}
		has = value.HasSponsor()
		if has {
			account, err = value.GetSponsor()
		}
	}
	if !has || err != nil {
		return "", false
	}
	sponsor, err := state.EncodeAccountID(account)
	return sponsor, err == nil
}

func (target *sponsoredTarget) encodeWithSponsor(sponsor string) ([]byte, error) {
	if sponsor == "" {
		switch model := target.model.(type) {
		case *entry.RippleState:
			if target.sponsorField == "HighSponsor" {
				model.ClearHighSponsor()
			} else {
				model.ClearLowSponsor()
			}
		default:
			value, ok := target.model.(interface{ ClearSponsor() })
			if !ok {
				return nil, errors.New("ledger entry does not support Sponsor")
			}
			value.ClearSponsor()
		}
	} else {
		account, err := state.DecodeAccountID(sponsor)
		if err != nil {
			return nil, err
		}
		switch model := target.model.(type) {
		case *entry.RippleState:
			if target.sponsorField == "HighSponsor" {
				if err := model.SetHighSponsorValue(account); err != nil {
					return nil, err
				}
			} else if err := model.SetLowSponsorValue(account); err != nil {
				return nil, err
			}
		default:
			value, ok := target.model.(interface{ SetSponsorValue([20]byte) error })
			if !ok {
				return nil, errors.New("ledger entry does not support Sponsor")
			}
			if err := value.SetSponsorValue(account); err != nil {
				return nil, err
			}
		}
	}
	encoder, ok := target.model.(interface{ Encode() ([]byte, error) })
	if !ok {
		return nil, errors.New("ledger entry does not support encoding")
	}
	return encoder.Encode()
}

func consumePrefundedReserve(view tx.LedgerView, sponsorID, sponseeID [20]byte, delta uint32) ter.Result {
	sponsorship, exists, result := loadSponsorship(view, sponsorID, sponseeID)
	if result != ter.TesSUCCESS || !exists {
		return result
	}
	if sponsorship.RemainingOwnerCount < delta {
		return ter.TefINTERNAL
	}
	sponsorship.RemainingOwnerCount -= delta
	data, err := state.SerializeSponsorship(sponsorship)
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := view.Update(keylet.Sponsorship(sponsorID, sponseeID), data); err != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}
