package xchain

import (
	"encoding/hex"
	"errors"

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

func loadSignerSet(view tx.ReadOnlyLedgerView, bridge *entry.Bridge) (signerSet, ter.Result) {
	door, err := bridge.GetAccount()
	if err != nil {
		return signerSet{}, ter.TecINTERNAL
	}
	doorRoot, err := state.ReadAccountRoot(view, door)
	if err != nil || doorRoot == nil {
		return signerSet{}, ter.TecINTERNAL
	}
	data, err := view.Read(keylet.SignerList(door))
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

func checkAttestationPublicKey(view tx.ReadOnlyLedgerView, signers signerSet, signerAccount, publicKey string) ter.Result {
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
	view tx.ReadOnlyLedgerView,
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

func (x *XChainAddClaimAttestation) Preclaim(view tx.ReadOnlyLedgerView, _ tx.EngineConfig) ter.Result {
	return attestationPreclaim(view, x.XChainBridge, x.AttestationSignerAccount, x.PublicKey)
}

func (x *XChainAddAccountCreateAttestation) Preclaim(view tx.ReadOnlyLedgerView, _ tx.EngineConfig) ter.Result {
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

func attestationsWithinLimit(values any) bool {
	switch values := values.(type) {
	case []entry.XChainClaimProofSigValue:
		return len(values) <= maxStoredAttestations
	case []entry.XChainCreateAccountProofSigValue:
		return len(values) <= maxStoredAttestations
	default:
		return false
	}
}

func accountString(value [20]byte) (string, bool) {
	account, err := state.EncodeAccountID(value)
	return account, err == nil
}

func parseStoredClaim(value entry.XChainClaimProofSigValue) (storedClaimAttestation, bool) {
	signer, err := value.GetAttestationSignerAccount()
	if err != nil {
		return storedClaimAttestation{}, false
	}
	signerAccount, ok := accountString(signer)
	if !ok {
		return storedClaimAttestation{}, false
	}
	reward, err := value.GetAttestationRewardAccount()
	if err != nil {
		return storedClaimAttestation{}, false
	}
	rewardAccount, ok := accountString(reward)
	if !ok {
		return storedClaimAttestation{}, false
	}
	amountValue, err := value.GetAmount()
	if err != nil {
		return storedClaimAttestation{}, false
	}
	amount, err := state.AmountFromLedgerValue(amountValue)
	if err != nil {
		return storedClaimAttestation{}, false
	}
	locking, err := value.GetWasLockingChainSend()
	if err != nil {
		return storedClaimAttestation{}, false
	}
	destination := ""
	if value.HasDestination() {
		destinationID, err := value.GetDestination()
		if err != nil {
			return storedClaimAttestation{}, false
		}
		destination, ok = accountString(destinationID)
		if !ok {
			return storedClaimAttestation{}, false
		}
	}
	publicKey, err := value.GetPublicKey()
	if err != nil {
		return storedClaimAttestation{}, false
	}
	return storedClaimAttestation{
		signerAccount: signerAccount,
		publicKey:     hex.EncodeToString(publicKey),
		amount:        amount,
		rewardAccount: rewardAccount,
		lockingSend:   locking != 0,
		destination:   destination,
	}, true
}

func parseStoredCreate(value entry.XChainCreateAccountProofSigValue) (storedCreateAttestation, bool) {
	signer, err := value.GetAttestationSignerAccount()
	if err != nil {
		return storedCreateAttestation{}, false
	}
	signerAccount, ok := accountString(signer)
	if !ok {
		return storedCreateAttestation{}, false
	}
	rewardAccountID, err := value.GetAttestationRewardAccount()
	if err != nil {
		return storedCreateAttestation{}, false
	}
	rewardAccount, ok := accountString(rewardAccountID)
	if !ok {
		return storedCreateAttestation{}, false
	}
	amountValue, err := value.GetAmount()
	if err != nil {
		return storedCreateAttestation{}, false
	}
	amount, err := state.AmountFromLedgerValue(amountValue)
	if err != nil {
		return storedCreateAttestation{}, false
	}
	rewardValue, err := value.GetSignatureReward()
	if err != nil {
		return storedCreateAttestation{}, false
	}
	reward, err := state.AmountFromLedgerValue(rewardValue)
	if err != nil {
		return storedCreateAttestation{}, false
	}
	locking, err := value.GetWasLockingChainSend()
	if err != nil {
		return storedCreateAttestation{}, false
	}
	destinationID, err := value.GetDestination()
	if err != nil {
		return storedCreateAttestation{}, false
	}
	destination, ok := accountString(destinationID)
	if !ok {
		return storedCreateAttestation{}, false
	}
	publicKey, err := value.GetPublicKey()
	if err != nil {
		return storedCreateAttestation{}, false
	}
	return storedCreateAttestation{
		signerAccount: signerAccount,
		publicKey:     hex.EncodeToString(publicKey),
		amount:        amount,
		reward:        reward,
		rewardAccount: rewardAccount,
		lockingSend:   locking != 0,
		destination:   destination,
	}, true
}

func claimProofValue(x *XChainAddClaimAttestation) (entry.XChainClaimProofSigValue, error) {
	var value entry.XChainClaimProofSigValue
	signer, err := state.DecodeAccountID(x.AttestationSignerAccount)
	if err != nil {
		return value, err
	}
	if err := value.SetAttestationSignerAccountValue(signer); err != nil {
		return value, err
	}
	rewardAccount, err := state.DecodeAccountID(x.AttestationRewardAccount)
	if err != nil {
		return value, err
	}
	if err := value.SetAttestationRewardAccountValue(rewardAccount); err != nil {
		return value, err
	}
	publicKey, err := hex.DecodeString(x.PublicKey)
	if err != nil {
		return value, err
	}
	value.SetPublicKeyValue(publicKey)
	if err := value.SetAmountValue(ledgerAmountValue(x.Amount)); err != nil {
		return value, err
	}
	value.SetWasLockingChainSendValue(boolInt(x.WasLockingChainSend != 0))
	if x.Destination != "" {
		destination, err := state.DecodeAccountID(x.Destination)
		if err != nil {
			return value, err
		}
		if err := value.SetDestinationValue(destination); err != nil {
			return value, err
		}
	}
	return value, nil
}

func createProofValue(x *XChainAddAccountCreateAttestation) (entry.XChainCreateAccountProofSigValue, error) {
	var value entry.XChainCreateAccountProofSigValue
	signer, err := state.DecodeAccountID(x.AttestationSignerAccount)
	if err != nil {
		return value, err
	}
	if err := value.SetAttestationSignerAccountValue(signer); err != nil {
		return value, err
	}
	rewardAccount, err := state.DecodeAccountID(x.AttestationRewardAccount)
	if err != nil {
		return value, err
	}
	if err := value.SetAttestationRewardAccountValue(rewardAccount); err != nil {
		return value, err
	}
	publicKey, err := hex.DecodeString(x.PublicKey)
	if err != nil {
		return value, err
	}
	value.SetPublicKeyValue(publicKey)
	if err := value.SetAmountValue(ledgerAmountValue(x.Amount)); err != nil {
		return value, err
	}
	if err := value.SetSignatureRewardValue(ledgerAmountValue(x.SignatureReward)); err != nil {
		return value, err
	}
	value.SetWasLockingChainSendValue(boolInt(x.WasLockingChainSend != 0))
	destination, err := state.DecodeAccountID(x.Destination)
	if err != nil {
		return value, err
	}
	if err := value.SetDestinationValue(destination); err != nil {
		return value, err
	}
	return value, nil
}

func ledgerAmountValue(amount tx.Amount) entry.AmountValue {
	value := entry.AmountValue{Value: amount.Value(), Currency: amount.Currency, Issuer: amount.Issuer}
	if amount.IsMPT() {
		value.Currency = ""
		value.Issuer = ""
		value.MPTIssuanceID = amount.MPTIssuanceID()
	}
	return value
}

func addOrReplaceClaim(values []entry.XChainClaimProofSigValue, x *XChainAddClaimAttestation) ([]entry.XChainClaimProofSigValue, error) {
	newValue, err := claimProofValue(x)
	if err != nil {
		return values, err
	}
	for i, value := range values {
		att, ok := parseStoredClaim(value)
		if ok && att.signerAccount == x.AttestationSignerAccount {
			values[i] = newValue
			return values, nil
		}
	}
	return append(values, newValue), nil
}

func addOrReplaceCreate(values []entry.XChainCreateAccountProofSigValue, x *XChainAddAccountCreateAttestation) ([]entry.XChainCreateAccountProofSigValue, error) {
	newValue, err := createProofValue(x)
	if err != nil {
		return values, err
	}
	for i, value := range values {
		att, ok := parseStoredCreate(value)
		if ok && att.signerAccount == x.AttestationSignerAccount {
			values[i] = newValue
			return values, nil
		}
	}
	return append(values, newValue), nil
}

func claimQuorum(
	view tx.ReadOnlyLedgerView,
	values []entry.XChainClaimProofSigValue,
	signers signerSet,
	amount tx.Amount,
	lockingSend bool,
	destination string,
	checkDestination bool,
) ([]entry.XChainClaimProofSigValue, []string, bool) {
	valid := make([]entry.XChainClaimProofSigValue, 0, len(values))
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
	view tx.ReadOnlyLedgerView,
	values []entry.XChainCreateAccountProofSigValue,
	signers signerSet,
	x *XChainAddAccountCreateAttestation,
) ([]entry.XChainCreateAccountProofSigValue, []string, bool) {
	valid := make([]entry.XChainCreateAccountProofSigValue, 0, len(values))
	rewards := make([]string, 0, len(values))
	var weight uint64
	for _, value := range values {
		att, ok := parseStoredCreate(value)
		if !ok || checkAttestationPublicKey(view, signers, att.signerAccount, att.publicKey) != ter.TesSUCCESS {
			continue
		}
		valid = append(valid, value)
		if !amountEqual(att.amount, x.Amount) || !amountEqual(att.reward, x.SignatureReward) ||
			att.lockingSend != (x.WasLockingChainSend != 0) || att.destination != x.Destination {
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
		page, err := claim.GetOwnerNode()
		if err != nil {
			return "", 0, "", ter.TecINTERNAL
		}
		account, err := claim.GetAccount()
		if err != nil {
			return "", 0, "", ter.TecINTERNAL
		}
		owner, err := state.EncodeAccountID(account)
		if err != nil {
			return "", 0, "", ter.TecINTERNAL
		}
		sponsor := ""
		if claim.HasSponsor() {
			sponsorID, err := claim.GetSponsor()
			if err != nil {
				return "", 0, "", ter.TecINTERNAL
			}
			sponsor, err = state.EncodeAccountID(sponsorID)
			if err != nil {
				return "", 0, "", ter.TecINTERNAL
			}
		}
		return owner, page, sponsor, ter.TesSUCCESS
	}
	var create entry.XChainOwnedCreateAccountClaimID
	if err := create.Decode(data); err != nil {
		return "", 0, "", ter.TecINTERNAL
	}
	page, err := create.GetOwnerNode()
	if err != nil {
		return "", 0, "", ter.TecINTERNAL
	}
	account, err := create.GetAccount()
	if err != nil {
		return "", 0, "", ter.TecINTERNAL
	}
	owner, err := state.EncodeAccountID(account)
	if err != nil {
		return "", 0, "", ter.TecINTERNAL
	}
	sponsor := ""
	if create.HasSponsor() {
		sponsorID, err := create.GetSponsor()
		if err != nil {
			return "", 0, "", ter.TecINTERNAL
		}
		sponsor, err = state.EncodeAccountID(sponsorID)
		if err != nil {
			return "", 0, "", ter.TecINTERNAL
		}
	}
	return owner, page, sponsor, ter.TesSUCCESS
}

func bridgeDestinationSide(bridge *entry.Bridge, spec XChainBridge) (chainType, ter.Result) {
	account, err := bridge.GetAccount()
	if err != nil {
		return lockingChain, ter.TecINTERNAL
	}
	accountString, err := state.EncodeAccountID(account)
	if err != nil {
		return lockingChain, ter.TecINTERNAL
	}
	if accountString == spec.LockingChainDoor {
		return lockingChain, ter.TesSUCCESS
	}
	if accountString == spec.IssuingChainDoor {
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
	otherChainSource, err := claim.GetOtherChainSource()
	if err != nil {
		return ter.TecINTERNAL
	}
	otherChainSourceAddress, err := state.EncodeAccountID(otherChainSource)
	if err != nil {
		return ter.TecINTERNAL
	}
	if otherChainSourceAddress != x.OtherChainSource {
		return ter.TecXCHAIN_SENDING_ACCOUNT_MISMATCH
	}
	if destinationChain(x.WasLockingChainSend != 0) != dstChain {
		return ter.TecXCHAIN_WRONG_CHAIN
	}
	values, err := claim.GetXChainClaimAttestations()
	if err != nil {
		return ter.TecINTERNAL
	}
	if !attestationsWithinLimit(values) {
		return ter.TefEXCEPTION
	}
	didModify := false
	if checkAttestationPublicKey(outer, signers, x.AttestationSignerAccount, x.PublicKey) == ter.TesSUCCESS {
		values, err = addOrReplaceClaim(values, x)
		if err != nil {
			return ter.TecINTERNAL
		}
		didModify = true
	}
	values, rewards, quorum := claimQuorum(outer, values, signers, x.Amount, x.WasLockingChainSend != 0, x.Destination, true)
	if err := claim.SetXChainClaimAttestationsValue(values); err != nil {
		return ter.TecINTERNAL
	}
	data, encodeResult := encodeEntry(&claim)
	if encodeResult != ter.TesSUCCESS {
		return encodeResult
	}
	if err := outer.Update(claimKey, data); err != nil {
		return ter.TecINTERNAL
	}
	if quorum && x.Destination != "" {
		rewardValue, err := claim.GetSignatureReward()
		if err != nil {
			return ter.TecINTERNAL
		}
		reward, err := state.AmountFromLedgerValue(rewardValue)
		if err != nil {
			return ter.TecINTERNAL
		}
		claimOwner, err := claim.GetAccount()
		if err != nil {
			return ter.TecINTERNAL
		}
		claimOwnerAddress, err := state.EncodeAccountID(claimOwner)
		if err != nil {
			return ter.TecINTERNAL
		}
		final := finalizeClaim(
			ctx, outer, x.XChainBridge, x.Destination, nil, claimOwnerAddress, x.Amount,
			claimOwnerAddress, reward, rewards, srcChain, claimKey, keepClaim, false,
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
	if destinationChain(x.WasLockingChainSend != 0) != dstChain {
		return ter.TecXCHAIN_WRONG_CHAIN
	}
	signers, result := loadSignerSet(outer, bridge)
	if result != ter.TesSUCCESS {
		return result
	}
	claimCount, err := bridge.GetXChainAccountClaimCount()
	if err != nil {
		return ter.TecINTERNAL
	}
	bridgeAccountID, err := bridge.GetAccount()
	if err != nil {
		return ter.TecINTERNAL
	}
	bridgeAccountAddress, err := state.EncodeAccountID(bridgeAccountID)
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
	values := []entry.XChainCreateAccountProofSigValue{}
	var existing entry.XChainOwnedCreateAccountClaimID
	if !createClaim {
		if err := existing.Decode(data); err != nil {
			return ter.TecINTERNAL
		}
		values, err = existing.GetXChainCreateAccountAttestations()
		if err != nil {
			return ter.TecINTERNAL
		}
		if !attestationsWithinLimit(values) {
			return ter.TefEXCEPTION
		}
	} else {
		door, err := state.ReadAccountRoot(outer, bridgeAccountID)
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
		values, err = addOrReplaceCreate(values, x)
		if err != nil {
			return ter.TecINTERNAL
		}
	}
	values, rewards, quorum := createQuorum(outer, values, signers, x)
	if !createClaim {
		if err := existing.SetXChainCreateAccountAttestationsValue(values); err != nil {
			return ter.TecINTERNAL
		}
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
			ctx, outer, x.XChainBridge, x.Destination, nil, bridgeAccountAddress, x.Amount,
			bridgeAccountAddress, x.SignatureReward, rewards, srcChain, claimKey, removeClaim, false,
		)
		if fatalResult := final.accountCreateAttestationFatalResult(); fatalResult != ter.TesSUCCESS {
			return fatalResult
		}
		bridge, _, err = readBridge(outer, x.XChainBridge)
		if err != nil || bridge == nil {
			return ter.TecINTERNAL
		}
		bridge.SetXChainAccountClaimCountValue(x.XChainAccountCreateCount)
		bridgeData, result := encodeEntry(bridge)
		if result != ter.TesSUCCESS {
			return result
		}
		if err := outer.Update(bridgeKey, bridgeData); err != nil {
			return ter.TecINTERNAL
		}
	} else if createClaim {
		dirResult, err := state.DirInsert(outer, keylet.OwnerDir(bridgeAccountID), claimKey.Key, false, func(dir *state.DirectoryNode) {
			dir.Owner = bridgeAccountID
		})
		if err != nil {
			if errors.Is(err, state.ErrDirFull) {
				return ter.TecDIR_FULL
			}
			return ter.TecINTERNAL
		}
		claim := &entry.XChainOwnedCreateAccountClaimID{}
		if err := claim.SetAccountValue(bridgeAccountID); err != nil {
			return ter.TecINTERNAL
		}
		bridgeSpecValue, err := bridgeValue(x.XChainBridge)
		if err != nil {
			return ter.TecINTERNAL
		}
		if err := claim.SetXChainBridgeValue(bridgeSpecValue); err != nil {
			return ter.TecINTERNAL
		}
		claim.SetXChainAccountCreateCountValue(x.XChainAccountCreateCount)
		if err := claim.SetXChainCreateAccountAttestationsValue(values); err != nil {
			return ter.TecINTERNAL
		}
		claim.SetOwnerNodeValue(dirResult.Page)
		claim.SetFlags(0)
		door, err := state.ReadAccountRoot(outer, bridgeAccountID)
		if err != nil || door == nil {
			return ter.TecINTERNAL
		}
		door.OwnerCount = tx.ConfineOwnerCount(door.OwnerCount, 1)
		doorData, err := state.SerializeAccountRoot(door)
		if err != nil {
			return ter.TecINTERNAL
		}
		if err := outer.Update(keylet.Account(bridgeAccountID), doorData); err != nil {
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
