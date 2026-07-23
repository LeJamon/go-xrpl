package sponsor

import (
	"encoding/hex"
	"errors"
	"math"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
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

func readAccount(view tx.LedgerView, accountID [20]byte) (*state.AccountRoot, ter.Result) {
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

func loadSponsorship(view tx.LedgerView, sponsorID, sponseeID [20]byte) (*state.SponsorshipData, bool, ter.Result) {
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

func commonSponsorPermission(view tx.LedgerView, common *tx.Common) ter.Result {
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
	total := uint64(account.OwnerCount-account.SponsoredOwnerCount) +
		uint64(account.SponsoringOwnerCount) + uint64(delta)
	if total > math.MaxUint32 {
		return math.MaxUint32, true
	}
	return uint32(total), true
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
	fields       map[string]any
	entryType    entry.Type
	sponsorField string
	ownerCount   uint32
}

func readSponsoredTarget(view tx.LedgerView, objectID [32]byte, sponseeID [20]byte, sponsee string) (*sponsoredTarget, ter.Result) {
	objectKey := keylet.Keylet{Type: entry.TypeAny, Key: objectID}
	data, err := view.Read(objectKey)
	if err != nil {
		return nil, ter.TefINTERNAL
	}
	if data == nil {
		return nil, ter.TecNO_ENTRY
	}

	entryType := entry.Type(state.EntryTypeCode(data))
	if !isSupportedObjectType(entryType) {
		return nil, ter.TecNO_PERMISSION
	}
	fields, err := binarycodec.DecodeBytes(data)
	if err != nil {
		return nil, ter.TefINTERNAL
	}

	target := &sponsoredTarget{
		key:          objectKey,
		fields:       fields,
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
	switch target.entryType {
	case entry.TypeCheck,
		entry.TypeEscrow,
		entry.TypePayChannel,
		entry.TypeMPToken,
		entry.TypeDelegate,
		entry.TypeDepositPreauth:
		return stringField(target.fields, "Account") == sponsee
	case entry.TypeMPTokenIssuance:
		return stringField(target.fields, "Issuer") == sponsee
	case entry.TypeSignerList:
		if target.key.Key != keylet.SignerList(sponseeID).Key {
			return false
		}
		flags := uint32Field(target.fields, "Flags")
		if flags&entry.LsfOneOwnerCount == 0 {
			target.ownerCount = 2 + uint32(sliceLength(target.fields["SignerEntries"]))
		}
		return true
	case entry.TypeCredential:
		ownerField := "Issuer"
		if uint32Field(target.fields, "Flags")&entry.LsfAccepted != 0 {
			ownerField = "Subject"
		}
		return stringField(target.fields, ownerField) == sponsee
	case entry.TypeRippleState:
		flags := uint32Field(target.fields, "Flags")
		if flags&entry.LsfHighReserve != 0 && amountIssuer(target.fields["HighLimit"]) == sponsee {
			target.sponsorField = "HighSponsor"
			return true
		}
		if flags&entry.LsfLowReserve != 0 && amountIssuer(target.fields["LowLimit"]) == sponsee {
			target.sponsorField = "LowSponsor"
			return true
		}
	}
	return false
}

func (target *sponsoredTarget) sponsor() (string, bool) {
	value, ok := target.fields[target.sponsorField]
	if !ok {
		return "", false
	}
	sponsor, ok := value.(string)
	return sponsor, ok && sponsor != ""
}

func (target *sponsoredTarget) encodeWithSponsor(sponsor string) ([]byte, error) {
	if sponsor == "" {
		delete(target.fields, target.sponsorField)
	} else {
		target.fields[target.sponsorField] = sponsor
	}
	return binarycodec.EncodeBytes(target.fields)
}

func stringField(fields map[string]any, name string) string {
	value, _ := fields[name].(string)
	return value
}

func uint32Field(fields map[string]any, name string) uint32 {
	switch value := fields[name].(type) {
	case uint32:
		return value
	case uint64:
		return uint32(value)
	case int:
		return uint32(value)
	case float64:
		return uint32(value)
	default:
		return 0
	}
}

func amountIssuer(value any) string {
	amount, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	issuer, _ := amount["issuer"].(string)
	if issuer == "" {
		issuer, _ = amount["Issuer"].(string)
	}
	return issuer
}

func sliceLength(value any) int {
	switch values := value.(type) {
	case []any:
		return len(values)
	case []map[string]any:
		return len(values)
	default:
		return 0
	}
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
