package xchain

import (
	"errors"
	"strconv"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

type signerSet struct {
	weights map[string]uint32
	quorum  uint32
}

func loadSignerSet(view tx.LedgerView, bridge *entry.Bridge) (signerSet, ter.Result) {
	doorID, err := state.DecodeAccountID(bridge.Account)
	if err != nil {
		return signerSet{}, ter.TecINTERNAL
	}
	door, err := state.ReadAccountRoot(view, doorID)
	if err != nil || door == nil {
		return signerSet{}, ter.TecINTERNAL
	}
	data, err := view.Read(keylet.SignerList(doorID))
	if err != nil {
		return signerSet{}, ter.TecINTERNAL
	}
	if data == nil {
		return signerSet{}, ter.TecXCHAIN_NO_SIGNERS_LIST
	}
	list, err := state.ParseSignerList(data)
	if err != nil {
		return signerSet{}, ter.TecINTERNAL
	}
	result := signerSet{weights: make(map[string]uint32, len(list.SignerEntries)), quorum: list.SignerQuorum}
	for _, signer := range list.SignerEntries {
		result.weights[signer.Account] = uint32(signer.SignerWeight)
	}
	return result, ter.TesSUCCESS
}

func checkAttestationPublicKey(view tx.LedgerView, signers signerSet, signerAccount, publicKey string) ter.Result {
	if _, ok := signers.weights[signerAccount]; !ok {
		return ter.TecNO_PERMISSION
	}
	derived, err := publicKeyAccount(publicKey)
	if err != nil {
		return ter.TecXCHAIN_BAD_PUBLIC_KEY_ACCOUNT_PAIR
	}
	signerID, err := state.DecodeAccountID(signerAccount)
	if err != nil {
		return ter.TecXCHAIN_BAD_PUBLIC_KEY_ACCOUNT_PAIR
	}
	account, err := state.ReadAccountRoot(view, signerID)
	if err != nil {
		return ter.TecINTERNAL
	}
	if account == nil {
		if derived != signerAccount {
			return ter.TecXCHAIN_BAD_PUBLIC_KEY_ACCOUNT_PAIR
		}
		return ter.TesSUCCESS
	}
	if derived == signerAccount {
		if account.Flags&state.LsfDisableMaster != 0 {
			return ter.TecXCHAIN_BAD_PUBLIC_KEY_ACCOUNT_PAIR
		}
		return ter.TesSUCCESS
	}
	if account.RegularKey != derived {
		return ter.TecXCHAIN_BAD_PUBLIC_KEY_ACCOUNT_PAIR
	}
	return ter.TesSUCCESS
}

func attestationPreclaim(
	view tx.LedgerView,
	bridgeSpec XChainBridge,
	signerAccount, publicKey string,
) ter.Result {
	bridge, _, err := readBridge(view, bridgeSpec)
	if err != nil {
		return ter.TecINTERNAL
	}
	if bridge == nil {
		return ter.TecNO_ENTRY
	}
	signers, result := loadSignerSet(view, bridge)
	if result != ter.TesSUCCESS {
		return result
	}
	return checkAttestationPublicKey(view, signers, signerAccount, publicKey)
}

func (x *XChainAddClaimAttestation) Preclaim(view tx.LedgerView, _ tx.EngineConfig) ter.Result {
	return attestationPreclaim(view, x.XChainBridge, x.AttestationSignerAccount, x.PublicKey)
}

func (x *XChainAddAccountCreateAttestation) Preclaim(view tx.LedgerView, _ tx.EngineConfig) ter.Result {
	return attestationPreclaim(view, x.XChainBridge, x.AttestationSignerAccount, x.PublicKey)
}

type storedClaimAttestation struct {
	signerAccount string
	publicKey     string
	amount        tx.Amount
	rewardAccount string
	lockingSend   bool
	destination   string
}

type storedCreateAttestation struct {
	signerAccount string
	publicKey     string
	amount        tx.Amount
	reward        tx.Amount
	rewardAccount string
	lockingSend   bool
	destination   string
}

const maxStoredAttestations = 256

func attestationsWithinLimit(values []any) bool {
	return len(values) <= maxStoredAttestations
}

func unwrapAttestation(value any, name string) (map[string]any, bool) {
	wrapper, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	inner, ok := wrapper[name].(map[string]any)
	return inner, ok
}

func boolValue(value any) (bool, bool) {
	switch v := value.(type) {
	case bool:
		return v, true
	case int:
		return v != 0, true
	case uint8:
		return v != 0, true
	case uint32:
		return v != 0, true
	case uint64:
		return v != 0, true
	case float64:
		return v != 0, true
	default:
		return false, false
	}
}

func parseStoredClaim(value any) (storedClaimAttestation, bool) {
	fields, ok := unwrapAttestation(value, "XChainClaimProofSig")
	if !ok {
		return storedClaimAttestation{}, false
	}
	amount, err := amountFromAny(fields["Amount"])
	locking, okLock := boolValue(fields["WasLockingChainSend"])
	result := storedClaimAttestation{
		signerAccount: stringField(fields, "AttestationSignerAccount"),
		publicKey:     stringField(fields, "PublicKey"),
		amount:        amount, rewardAccount: stringField(fields, "AttestationRewardAccount"),
		lockingSend: locking, destination: stringField(fields, "Destination"),
	}
	return result, err == nil && okLock && result.signerAccount != "" && result.publicKey != ""
}

func parseStoredCreate(value any) (storedCreateAttestation, bool) {
	fields, ok := unwrapAttestation(value, "XChainCreateAccountProofSig")
	if !ok {
		return storedCreateAttestation{}, false
	}
	amount, err := amountFromAny(fields["Amount"])
	if err != nil {
		return storedCreateAttestation{}, false
	}
	reward, err := amountFromAny(fields["SignatureReward"])
	locking, okLock := boolValue(fields["WasLockingChainSend"])
	result := storedCreateAttestation{
		signerAccount: stringField(fields, "AttestationSignerAccount"),
		publicKey:     stringField(fields, "PublicKey"), amount: amount, reward: reward,
		rewardAccount: stringField(fields, "AttestationRewardAccount"),
		lockingSend:   locking, destination: stringField(fields, "Destination"),
	}
	return result, err == nil && okLock && result.signerAccount != "" && result.publicKey != ""
}

func stringField(fields map[string]any, name string) string {
	value, _ := fields[name].(string)
	return value
}

func storedClaimMap(x *XChainAddClaimAttestation) map[string]any {
	fields := map[string]any{
		"AttestationSignerAccount": x.AttestationSignerAccount,
		"PublicKey":                x.PublicKey,
		"Amount":                   mustAmountAny(x.Amount),
		"AttestationRewardAccount": x.AttestationRewardAccount,
		"WasLockingChainSend":      boolInt(x.WasLockingChainSend),
	}
	if x.Destination != "" {
		fields["Destination"] = x.Destination
	}
	return map[string]any{"XChainClaimProofSig": fields}
}

func storedCreateMap(x *XChainAddAccountCreateAttestation) map[string]any {
	return map[string]any{"XChainCreateAccountProofSig": map[string]any{
		"AttestationSignerAccount": x.AttestationSignerAccount,
		"PublicKey":                x.PublicKey,
		"Amount":                   mustAmountAny(x.Amount),
		"SignatureReward":          mustAmountAny(x.SignatureReward),
		"AttestationRewardAccount": x.AttestationRewardAccount,
		"WasLockingChainSend":      boolInt(x.WasLockingChainSend),
		"Destination":              x.Destination,
	}}
}

func addOrReplaceClaim(values []any, x *XChainAddClaimAttestation) []any {
	for i, value := range values {
		att, ok := parseStoredClaim(value)
		if ok && att.signerAccount == x.AttestationSignerAccount {
			values[i] = storedClaimMap(x)
			return values
		}
	}
	return append(values, storedClaimMap(x))
}

func addOrReplaceCreate(values []any, x *XChainAddAccountCreateAttestation) []any {
	for i, value := range values {
		att, ok := parseStoredCreate(value)
		if ok && att.signerAccount == x.AttestationSignerAccount {
			values[i] = storedCreateMap(x)
			return values
		}
	}
	return append(values, storedCreateMap(x))
}

func claimQuorum(
	view tx.LedgerView,
	values []any,
	signers signerSet,
	amount tx.Amount,
	lockingSend bool,
	destination string,
	checkDestination bool,
) ([]any, []string, bool) {
	valid := make([]any, 0, len(values))
	rewards := make([]string, 0, len(values))
	var weight uint64
	for _, value := range values {
		att, ok := parseStoredClaim(value)
		if !ok || checkAttestationPublicKey(view, signers, att.signerAccount, att.publicKey) != ter.TesSUCCESS {
			continue
		}
		valid = append(valid, value)
		if !amountEqual(att.amount, amount) || att.lockingSend != lockingSend {
			continue
		}
		if checkDestination && att.destination != destination {
			continue
		}
		weight += uint64(signers.weights[att.signerAccount])
		rewards = append(rewards, att.rewardAccount)
	}
	return valid, rewards, weight >= uint64(signers.quorum)
}

func createQuorum(
	view tx.LedgerView,
	values []any,
	signers signerSet,
	x *XChainAddAccountCreateAttestation,
) ([]any, []string, bool) {
	valid := make([]any, 0, len(values))
	rewards := make([]string, 0, len(values))
	var weight uint64
	for _, value := range values {
		att, ok := parseStoredCreate(value)
		if !ok || checkAttestationPublicKey(view, signers, att.signerAccount, att.publicKey) != ter.TesSUCCESS {
			continue
		}
		valid = append(valid, value)
		if !amountEqual(att.amount, x.Amount) || !amountEqual(att.reward, x.SignatureReward) ||
			att.lockingSend != x.WasLockingChainSend || att.destination != x.Destination {
			continue
		}
		weight += uint64(signers.weights[att.signerAccount])
		rewards = append(rewards, att.rewardAccount)
	}
	return valid, rewards, weight >= uint64(signers.quorum)
}

func amountEqual(a, b tx.Amount) bool {
	aAsset := normalizedAsset(assetOf(a))
	bAsset := normalizedAsset(assetOf(b))
	if aAsset != bAsset {
		return false
	}
	if !aAsset.IsMPT() {
		a = amountWithAsset(a, aAsset)
		b = amountWithAsset(b, bAsset)
	}
	comparison, err := a.CompareChecked(b)
	return err == nil && comparison == 0
}

type transferFailurePolicy uint8

const (
	keepClaim transferFailurePolicy = iota
	removeClaim
)

type finalizeResult struct {
	main   ter.Result
	reward ter.Result
	remove ter.Result
}

func (r finalizeResult) result() ter.Result {
	for _, value := range []ter.Result{r.main, r.reward, r.remove} {
		if value == ter.TecINTERNAL || value == ter.TecINVARIANT_FAILED || value.IsTef() {
			return value
		}
	}
	for _, value := range []ter.Result{r.main, r.reward, r.remove} {
		if value != 0 && value != ter.TesSUCCESS {
			return value
		}
	}
	return ter.TesSUCCESS
}

func (r finalizeResult) claimAttestationFatalResult() ter.Result {
	for _, value := range []ter.Result{r.main, r.reward, r.remove} {
		if value == ter.TecINVARIANT_FAILED {
			return value
		}
	}
	result := r.result()
	if result == ter.TecINTERNAL || result == ter.TefBAD_LEDGER {
		return result
	}
	return ter.TesSUCCESS
}

func (r finalizeResult) accountCreateAttestationFatalResult() ter.Result {
	for _, value := range []ter.Result{r.main, r.reward, r.remove} {
		if value == ter.TecINVARIANT_FAILED {
			return value
		}
	}
	result := r.result()
	if result == ter.TecINTERNAL || result == ter.TecUNFUNDED_PAYMENT || result.IsTef() {
		return result
	}
	return ter.TesSUCCESS
}

func finalizeClaim(
	ctx *tx.ApplyContext,
	outer *payment.PaymentSandbox,
	bridge XChainBridge,
	destination string,
	destinationTag *uint32,
	claimOwner string,
	sendingAmount tx.Amount,
	rewardSource string,
	rewardPool tx.Amount,
	rewardAccounts []string,
	srcChain chainType,
	claimKey keylet.Keylet,
	policy transferFailurePolicy,
	bypassDepositAuth bool,
) finalizeResult {
	result := finalizeResult{main: ter.TesSUCCESS, reward: ter.TesSUCCESS, remove: ter.TesSUCCESS}
	inner := payment.NewChildSandbox(outer)
	dstChain := otherChain(srcChain)
	result.main = transferOnView(
		ctx, inner, bridge.door(dstChain), destination, destinationTag, claimOwner,
		amountWithAsset(sendingAmount, bridge.issue(dstChain)), true, bypassDepositAuth, false,
	)
	if result.main != ter.TesSUCCESS && policy == keepClaim {
		return result
	}

	if len(rewardAccounts) > 0 {
		share := rewardShare(
			rewardPool,
			uint64(len(rewardAccounts)),
			ctx.NumberContext(),
			ctx.Rules().Enabled(amendment.FeatureFixXChainRewardRounding),
		)
		distributed := tx.NewXRPAmount(0)
		if !rewardPool.IsNative() {
			distributed = tx.NewIssuedAmount(0, 0, rewardPool.Currency, rewardPool.Issuer)
		}
		for _, rewardAccount := range rewardAccounts {
			transferResult := transferOnView(ctx, inner, rewardSource, rewardAccount, nil, "", share, false, false, false)
			if transferResult == ter.TecUNFUNDED_PAYMENT || transferResult == ter.TecINTERNAL ||
				transferResult == ter.TecINVARIANT_FAILED || transferResult.IsTef() {
				result.reward = transferResult
				break
			}
			if transferResult == ter.TesSUCCESS {
				var err error
				distributed, err = distributed.AddWithNumberContext(share, ctx.NumberContext(), state.RoundToNearest)
				if err != nil {
					result.reward = ter.TecINTERNAL
					break
				}
			}
		}
		if distributed.Compare(rewardPool) > 0 {
			result.reward = ter.TecINTERNAL
		}
	}
	if result.reward != ter.TesSUCCESS && (policy == keepClaim || result.reward == ter.TecINTERNAL) {
		return result
	}
	if result.main != ter.TesSUCCESS || result.reward == ter.TesSUCCESS {
		if err := inner.Apply(outer); err != nil {
			result.reward = ter.TecINTERNAL
			return result
		}
	}

	data, err := outer.Read(claimKey)
	if err != nil {
		result.remove = ter.TecINTERNAL
		return result
	}
	if data == nil {
		return result
	}
	owner, ownerNode, sponsor, parseResult := claimOwnerFields(data)
	if parseResult != ter.TesSUCCESS {
		result.remove = parseResult
		return result
	}
	ownerID, err := state.DecodeAccountID(owner)
	if err != nil {
		result.remove = ter.TecINTERNAL
		return result
	}
	removed, err := state.DirRemove(outer, keylet.OwnerDir(ownerID), ownerNode, claimKey.Key, true)
	if err != nil || removed == nil || !removed.Success {
		result.remove = ter.TefBAD_LEDGER
		return result
	}
	if err := tx.DecreaseOwnerCountOnView(outer, ownerID, sponsor, 1); err != nil {
		result.remove = ter.TecINTERNAL
		return result
	}
	if err := outer.Erase(claimKey); err != nil {
		result.remove = ter.TecINTERNAL
	}
	return result
}

func rewardShare(pool tx.Amount, count uint64, numberContext state.NumberContext, roundDown bool) tx.Amount {
	if count == 0 {
		return pool
	}
	mode := state.RoundToNearest
	if roundDown {
		mode = state.RoundDownward
	}
	numerator := numberContext.FromAmount(pool, mode)
	denominator := numberContext.FromInt(int64(count), mode)
	return numberContext.ToAmount(numerator.DivRounded(denominator, mode), pool, mode)
}

func claimOwnerFields(data []byte) (string, uint64, string, ter.Result) {
	var claim entry.XChainOwnedClaimID
	if err := claim.Decode(data); err == nil {
		page, err := parseHexUint(claim.OwnerNode)
		if err != nil {
			return "", 0, "", ter.TecINTERNAL
		}
		return claim.Account, page, claim.Sponsor, ter.TesSUCCESS
	}
	var create entry.XChainOwnedCreateAccountClaimID
	if err := create.Decode(data); err != nil {
		return "", 0, "", ter.TecINTERNAL
	}
	page, err := parseHexUint(create.OwnerNode)
	if err != nil {
		return "", 0, "", ter.TecINTERNAL
	}
	return create.Account, page, create.Sponsor, ter.TesSUCCESS
}

func bridgeDestinationSide(bridge *entry.Bridge, spec XChainBridge) (chainType, ter.Result) {
	if bridge.Account == spec.LockingChainDoor {
		return lockingChain, ter.TesSUCCESS
	}
	if bridge.Account == spec.IssuingChainDoor {
		return issuingChain, ter.TesSUCCESS
	}
	return lockingChain, ter.TecINTERNAL
}

func (x *XChainAddClaimAttestation) Apply(ctx *tx.ApplyContext) ter.Result {
	outer := payment.NewPaymentSandbox(ctx.View)
	outer.SetTransactionContext(ctx.TxHash, ctx.Config.LedgerSequence)
	bridge, _, err := readBridge(outer, x.XChainBridge)
	if err != nil {
		return ter.TecINTERNAL
	}
	if bridge == nil {
		return ter.TecNO_ENTRY
	}
	dstChain, result := bridgeDestinationSide(bridge, x.XChainBridge)
	if result != ter.TesSUCCESS {
		return result
	}
	srcChain := otherChain(dstChain)
	signers, result := loadSignerSet(outer, bridge)
	if result != ter.TesSUCCESS {
		return result
	}
	claimBridge, err := claimBridgeKeylet(x.XChainBridge)
	if err != nil {
		return ter.TecINTERNAL
	}
	claimKey := keylet.XChainClaimID(claimBridge, x.XChainClaimID)
	data, err := outer.Read(claimKey)
	if err != nil {
		return ter.TecINTERNAL
	}
	if data == nil {
		return ter.TecXCHAIN_NO_CLAIM_ID
	}
	var claim entry.XChainOwnedClaimID
	if err := claim.Decode(data); err != nil {
		return ter.TecINTERNAL
	}
	if _, ok := signers.weights[x.AttestationSignerAccount]; !ok {
		return ter.TecXCHAIN_PROOF_UNKNOWN_KEY
	}
	if claim.OtherChainSource != x.OtherChainSource {
		return ter.TecXCHAIN_SENDING_ACCOUNT_MISMATCH
	}
	if destinationChain(x.WasLockingChainSend) != dstChain {
		return ter.TecXCHAIN_WRONG_CHAIN
	}
	values := append([]any(nil), claim.XChainClaimAttestations...)
	if !attestationsWithinLimit(values) {
		return ter.TefEXCEPTION
	}
	didModify := false
	if checkAttestationPublicKey(outer, signers, x.AttestationSignerAccount, x.PublicKey) == ter.TesSUCCESS {
		values = addOrReplaceClaim(values, x)
		didModify = true
	}
	values, rewards, quorum := claimQuorum(outer, values, signers, x.Amount, x.WasLockingChainSend, x.Destination, true)
	claim.SetXChainClaimAttestations(values)
	data, encodeResult := encodeEntry(&claim)
	if encodeResult != ter.TesSUCCESS {
		return encodeResult
	}
	if err := outer.Update(claimKey, data); err != nil {
		return ter.TecINTERNAL
	}
	if quorum && x.Destination != "" {
		reward, err := amountFromAny(claim.SignatureReward)
		if err != nil {
			return ter.TecINTERNAL
		}
		final := finalizeClaim(
			ctx, outer, x.XChainBridge, x.Destination, nil, claim.Account, x.Amount,
			claim.Account, reward, rewards, srcChain, claimKey, keepClaim, false,
		)
		finalResult := final.result()
		if finalResult != ter.TesSUCCESS {
			if fatalResult := final.claimAttestationFatalResult(); fatalResult != ter.TesSUCCESS {
				return fatalResult
			}
			if !didModify {
				return finalResult
			}
		}
	}
	if err := outer.ApplyToView(ctx.View); err != nil {
		return ter.TecINTERNAL
	}
	syncApplySource(ctx)
	return ter.TesSUCCESS
}

func (x *XChainAddAccountCreateAttestation) Apply(ctx *tx.ApplyContext) ter.Result {
	outer := payment.NewPaymentSandbox(ctx.View)
	outer.SetTransactionContext(ctx.TxHash, ctx.Config.LedgerSequence)
	bridge, bridgeKey, err := readBridge(outer, x.XChainBridge)
	if err != nil {
		return ter.TecINTERNAL
	}
	if bridge == nil {
		return ter.TecNO_ENTRY
	}
	dstChain, result := bridgeDestinationSide(bridge, x.XChainBridge)
	if result != ter.TesSUCCESS {
		return result
	}
	srcChain := otherChain(dstChain)
	if destinationChain(x.WasLockingChainSend) != dstChain {
		return ter.TecXCHAIN_WRONG_CHAIN
	}
	signers, result := loadSignerSet(outer, bridge)
	if result != ter.TesSUCCESS {
		return result
	}
	claimCount, err := parseHexUint(bridge.XChainAccountClaimCount)
	if err != nil {
		return ter.TecINTERNAL
	}
	if x.XChainAccountCreateCount <= claimCount {
		return ter.TecXCHAIN_ACCOUNT_CREATE_PAST
	}
	if x.XChainAccountCreateCount >= claimCount+maxAccountCreateClaims {
		return ter.TecXCHAIN_ACCOUNT_CREATE_TOO_MANY
	}
	claimBridge, err := claimBridgeKeylet(x.XChainBridge)
	if err != nil {
		return ter.TecINTERNAL
	}
	claimKey := keylet.XChainCreateAccountClaimID(claimBridge, x.XChainAccountCreateCount)
	data, err := outer.Read(claimKey)
	if err != nil {
		return ter.TecINTERNAL
	}
	createClaim := data == nil
	values := []any{}
	var existing entry.XChainOwnedCreateAccountClaimID
	if !createClaim {
		if err := existing.Decode(data); err != nil {
			return ter.TecINTERNAL
		}
		values = append(values, existing.XChainCreateAccountAttestations...)
		if !attestationsWithinLimit(values) {
			return ter.TefEXCEPTION
		}
	} else {
		doorID, err := state.DecodeAccountID(bridge.Account)
		if err != nil {
			return ter.TecINTERNAL
		}
		door, err := state.ReadAccountRoot(outer, doorID)
		if err != nil || door == nil {
			return ter.TecINTERNAL
		}
		reserve, ok := tx.AccountReserveForView(outer, ctx.Config, door, tx.ConfineOwnerCount(door.OwnerCount, 1))
		if !ok || door.Balance < reserve {
			return ter.TecINSUFFICIENT_RESERVE
		}
	}
	if _, ok := signers.weights[x.AttestationSignerAccount]; !ok {
		return ter.TecXCHAIN_PROOF_UNKNOWN_KEY
	}
	if checkAttestationPublicKey(outer, signers, x.AttestationSignerAccount, x.PublicKey) == ter.TesSUCCESS {
		values = addOrReplaceCreate(values, x)
	}
	values, rewards, quorum := createQuorum(outer, values, signers, x)
	if !createClaim {
		existing.SetXChainCreateAccountAttestations(values)
		data, result := encodeEntry(&existing)
		if result != ter.TesSUCCESS {
			return result
		}
		if err := outer.Update(claimKey, data); err != nil {
			return ter.TecINTERNAL
		}
	}

	if quorum && claimCount+1 == x.XChainAccountCreateCount {
		final := finalizeClaim(
			ctx, outer, x.XChainBridge, x.Destination, nil, bridge.Account, x.Amount,
			bridge.Account, x.SignatureReward, rewards, srcChain, claimKey, removeClaim, false,
		)
		if fatalResult := final.accountCreateAttestationFatalResult(); fatalResult != ter.TesSUCCESS {
			return fatalResult
		}
		bridge, _, err = readBridge(outer, x.XChainBridge)
		if err != nil || bridge == nil {
			return ter.TecINTERNAL
		}
		bridge.SetXChainAccountClaimCount(strconv.FormatUint(x.XChainAccountCreateCount, 16))
		bridgeData, result := encodeEntry(bridge)
		if result != ter.TesSUCCESS {
			return result
		}
		if err := outer.Update(bridgeKey, bridgeData); err != nil {
			return ter.TecINTERNAL
		}
	} else if createClaim {
		doorID, err := state.DecodeAccountID(bridge.Account)
		if err != nil {
			return ter.TecINTERNAL
		}
		dirResult, err := state.DirInsert(outer, keylet.OwnerDir(doorID), claimKey.Key, false, func(dir *state.DirectoryNode) {
			dir.Owner = doorID
		})
		if err != nil {
			if errors.Is(err, state.ErrDirFull) {
				return ter.TecDIR_FULL
			}
			return ter.TecINTERNAL
		}
		claim := &entry.XChainOwnedCreateAccountClaimID{}
		claim.SetAccount(bridge.Account)
		claim.SetXChainBridge(bridgeMap(x.XChainBridge))
		claim.SetXChainAccountCreateCount(strconv.FormatUint(x.XChainAccountCreateCount, 16))
		claim.SetXChainCreateAccountAttestations(values)
		claim.SetOwnerNode(strconv.FormatUint(dirResult.Page, 16))
		claim.SetFlags(0)
		door, err := state.ReadAccountRoot(outer, doorID)
		if err != nil || door == nil {
			return ter.TecINTERNAL
		}
		door.OwnerCount = tx.ConfineOwnerCount(door.OwnerCount, 1)
		doorData, err := state.SerializeAccountRoot(door)
		if err != nil {
			return ter.TecINTERNAL
		}
		if err := outer.Update(keylet.Account(doorID), doorData); err != nil {
			return ter.TecINTERNAL
		}
		claimData, result := encodeEntry(claim)
		if result != ter.TesSUCCESS {
			return result
		}
		if err := outer.Insert(claimKey, claimData); err != nil {
			return ter.TecINTERNAL
		}
	}

	if err := outer.ApplyToView(ctx.View); err != nil {
		return ter.TecINTERNAL
	}
	syncApplySource(ctx)
	return ter.TesSUCCESS
}
