package mpt

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/crypto/mptcrypto"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/credential"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

type ConfidentialMPTSend struct {
	tx.BaseTx
	MPTokenIssuanceID          string   `json:"MPTokenIssuanceID" xrpl:"MPTokenIssuanceID"`
	Destination                string   `json:"Destination" xrpl:"Destination"`
	DestinationTag             *uint32  `json:"DestinationTag,omitempty" xrpl:"DestinationTag,omitempty"`
	SenderEncryptedAmount      string   `json:"SenderEncryptedAmount" xrpl:"SenderEncryptedAmount"`
	DestinationEncryptedAmount string   `json:"DestinationEncryptedAmount" xrpl:"DestinationEncryptedAmount"`
	IssuerEncryptedAmount      string   `json:"IssuerEncryptedAmount" xrpl:"IssuerEncryptedAmount"`
	AuditorEncryptedAmount     *string  `json:"AuditorEncryptedAmount,omitempty" xrpl:"AuditorEncryptedAmount,omitempty"`
	ZKProof                    string   `json:"ZKProof" xrpl:"ZKProof"`
	AmountCommitment           string   `json:"AmountCommitment" xrpl:"AmountCommitment"`
	BalanceCommitment          string   `json:"BalanceCommitment" xrpl:"BalanceCommitment"`
	CredentialIDs              []string `json:"CredentialIDs,omitempty" xrpl:"CredentialIDs,omitempty"`
}

func (c *ConfidentialMPTSend) TxType() tx.Type { return tx.TypeConfidentialMPTSend }

func (c *ConfidentialMPTSend) GetFlagsMask(*amendment.Rules) uint32 { return tx.TfUniversalMask }

func (c *ConfidentialMPTSend) RequiredAmendments() [][32]byte {
	amendments := [][32]byte{amendment.FeatureConfidentialTransfer}
	if c.CredentialIDs != nil || c.HasField("CredentialIDs") {
		amendments = append(amendments, amendment.FeatureCredentials)
	}
	return amendments
}

func (c *ConfidentialMPTSend) Flatten() (map[string]any, error) { return tx.ReflectFlatten(c) }

func (c *ConfidentialMPTSend) Validate() error {
	if err := c.BaseTx.Validate(); err != nil {
		return err
	}
	id, ok := parseConfidentialID(c.MPTokenIssuanceID)
	if !ok {
		return ter.Errorf(ter.TemINVALID, "invalid MPTokenIssuanceID")
	}
	accountID, accountErr := state.DecodeAccountID(c.Account)
	destinationID, destinationErr := state.DecodeAccountID(c.Destination)
	if accountErr != nil || destinationErr != nil {
		return ter.Errorf(ter.TemMALFORMED, "invalid send account")
	}
	issuer := [20]byte(id[4:])
	if accountID == issuer || accountID == destinationID || destinationID == issuer {
		return ter.Errorf(ter.TemMALFORMED, "invalid confidential transfer participants")
	}

	for _, value := range []*string{&c.SenderEncryptedAmount, &c.DestinationEncryptedAmount, &c.IssuerEncryptedAmount, c.AuditorEncryptedAmount} {
		if value == nil {
			continue
		}
		if _, valid := decodeFixed(*value, mptcrypto.CiphertextSize); !valid {
			return ter.Errorf(ter.TemBAD_CIPHERTEXT, "invalid ciphertext length")
		}
	}
	if _, valid := decodeFixed(c.ZKProof, mptcrypto.SendProofSize); !valid {
		return ter.Errorf(ter.TemMALFORMED, "invalid ZKProof")
	}
	for _, value := range []string{c.BalanceCommitment, c.AmountCommitment} {
		commitment, valid := decodeFixed(value, mptcrypto.CommitmentSize)
		if !valid || !mptcrypto.ValidPublicKey(commitment) {
			return ter.Errorf(ter.TemMALFORMED, "invalid Pedersen commitment")
		}
	}
	for _, value := range []*string{&c.SenderEncryptedAmount, &c.DestinationEncryptedAmount, &c.IssuerEncryptedAmount, c.AuditorEncryptedAmount} {
		if value == nil {
			continue
		}
		ciphertext, _ := decodeFixed(*value, mptcrypto.CiphertextSize)
		if !mptcrypto.ValidCiphertext(ciphertext) {
			return ter.Errorf(ter.TemBAD_CIPHERTEXT, "invalid ciphertext")
		}
	}
	credentialsPresent := c.CredentialIDs != nil || c.HasField("CredentialIDs")
	return credential.CheckFields(c.CredentialIDs, credentialsPresent, "Duplicate credential ID")
}

func (c *ConfidentialMPTSend) CalculateBaseFee(_ tx.LedgerView, config tx.EngineConfig) uint64 {
	return confidentialBaseFee(c, config)
}

func (c *ConfidentialMPTSend) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	id, _ := parseConfidentialID(c.MPTokenIssuanceID)
	accountID, err := state.DecodeAccountID(c.Account)
	if err != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}
	senderAccount, err := tx.ReadAccountRoot(view, accountID)
	if err != nil {
		return ter.TefINTERNAL
	}
	if senderAccount == nil {
		return ter.TerNO_ACCOUNT
	}
	destinationID, err := state.DecodeAccountID(c.Destination)
	if err != nil {
		return ter.TemMALFORMED
	}
	destinationAccount, err := tx.ReadAccountRoot(view, destinationID)
	if err != nil {
		return ter.TefINTERNAL
	}
	if destinationAccount == nil {
		return ter.TecNO_TARGET
	}
	if destinationAccount.Flags&state.LsfRequireDestTag != 0 && c.DestinationTag == nil {
		return ter.TecDST_TAG_NEEDED
	}

	issuance, _, result := mptutil.ReadIssuance(view, id)
	if result != ter.TesSUCCESS {
		return result
	}
	if issuance.Flags&entry.LsfMPTCanTransfer == 0 {
		return ter.TecNO_AUTH
	}
	if issuance.Flags&entry.LsfMPTCanHoldConfidentialBalance == 0 || issuance.TransferFee > 0 || len(issuance.IssuerEncryptionKey) == 0 {
		return ter.TecNO_PERMISSION
	}
	requiresAuditor := len(issuance.AuditorEncryptionKey) != 0
	if requiresAuditor != (c.AuditorEncryptedAmount != nil) {
		return ter.TecNO_PERMISSION
	}
	if issuance.Issuer == accountID {
		return ter.TefINTERNAL
	}

	senderToken, _, result := readConfidentialHolding(view, id, accountID)
	if result != ter.TesSUCCESS {
		return result
	}
	if len(senderToken.HolderEncryptionKey) == 0 || len(senderToken.ConfidentialBalanceSpending) == 0 || len(senderToken.IssuerEncryptedBalance) == 0 {
		return ter.TecNO_PERMISSION
	}
	destinationToken, _, result := readConfidentialHolding(view, id, destinationID)
	if result != ter.TesSUCCESS {
		return result
	}
	if len(destinationToken.HolderEncryptionKey) == 0 || len(destinationToken.ConfidentialBalanceInbox) == 0 || len(destinationToken.IssuerEncryptedBalance) == 0 {
		return ter.TecNO_PERMISSION
	}
	if requiresAuditor && (len(senderToken.AuditorEncryptedBalance) == 0 || len(destinationToken.AuditorEncryptedBalance) == 0) {
		return ter.TefINTERNAL
	}
	if mptutil.IsFrozen(view, id, accountID) {
		return ter.TecLOCKED
	}
	if mptutil.IsFrozen(view, id, destinationID) {
		return ter.TecLOCKED
	}
	if result := mptutil.RequireAuthAt(view, id, accountID, false, config.ParentCloseTime); result != ter.TesSUCCESS {
		return result
	}
	if result := mptutil.RequireAuthAt(view, id, destinationID, false, config.ParentCloseTime); result != ter.TesSUCCESS {
		return result
	}
	if result := credential.ValidCredentials(view, accountID, c.CredentialIDs); result != ter.TesSUCCESS {
		return result
	}
	credentialsPresent := c.CredentialIDs != nil || c.HasField("CredentialIDs")
	if result := credential.CheckDepositPreauth(view, c.CredentialIDs, credentialsPresent, accountID, destinationID, destinationAccount); result != ter.TesSUCCESS {
		return result
	}

	senderCiphertext, _ := decodeFixed(c.SenderEncryptedAmount, mptcrypto.CiphertextSize)
	destinationCiphertext, _ := decodeFixed(c.DestinationEncryptedAmount, mptcrypto.CiphertextSize)
	issuerCiphertext, _ := decodeFixed(c.IssuerEncryptedAmount, mptcrypto.CiphertextSize)
	proof, _ := decodeFixed(c.ZKProof, mptcrypto.SendProofSize)
	amountCommitment, _ := decodeFixed(c.AmountCommitment, mptcrypto.CommitmentSize)
	balanceCommitment, _ := decodeFixed(c.BalanceCommitment, mptcrypto.CommitmentSize)
	var auditor *mptcrypto.Participant
	if c.AuditorEncryptedAmount != nil {
		ciphertext, _ := decodeFixed(*c.AuditorEncryptedAmount, mptcrypto.CiphertextSize)
		value := participant(issuance.AuditorEncryptionKey, ciphertext)
		auditor = &value
	}
	context, contextOK := mptcrypto.SendContext(accountID, id, c.GetCommon().SeqProxy(), destinationID, senderToken.ConfidentialBalanceVersion)
	if !contextOK || !mptcrypto.VerifySend(
		proof,
		participant(senderToken.HolderEncryptionKey, senderCiphertext),
		participant(destinationToken.HolderEncryptionKey, destinationCiphertext),
		participant(issuance.IssuerEncryptionKey, issuerCiphertext),
		auditor,
		senderToken.ConfidentialBalanceSpending,
		amountCommitment,
		balanceCommitment,
		context,
	) {
		return ter.TecBAD_PROOF
	}
	return ter.TesSUCCESS
}

func (c *ConfidentialMPTSend) Apply(ctx *tx.ApplyContext) ter.Result {
	id, _ := parseConfidentialID(c.MPTokenIssuanceID)
	destinationID, _ := state.DecodeAccountID(c.Destination)
	issuance, _, result := mptutil.ReadIssuance(ctx.View, id)
	if result != ter.TesSUCCESS {
		return ter.TecINTERNAL
	}
	sender, senderKeylet, result := readConfidentialHolding(ctx.View, id, ctx.AccountID)
	if result != ter.TesSUCCESS {
		return ter.TecINTERNAL
	}
	destination, destinationKeylet, result := readConfidentialHolding(ctx.View, id, destinationID)
	if result != ter.TesSUCCESS {
		return ter.TecINTERNAL
	}
	if len(c.CredentialIDs) > 0 {
		expired, cleanupResult := credential.RemoveExpiredCredentials(ctx, c.CredentialIDs)
		if cleanupResult != ter.TesSUCCESS {
			return cleanupResult
		}
		if expired {
			return ter.TecEXPIRED
		}
	}

	senderCiphertext, _ := decodeFixed(c.SenderEncryptedAmount, mptcrypto.CiphertextSize)
	destinationCiphertext, _ := decodeFixed(c.DestinationEncryptedAmount, mptcrypto.CiphertextSize)
	issuerCiphertext, _ := decodeFixed(c.IssuerEncryptedAmount, mptcrypto.CiphertextSize)
	proof, _ := decodeFixed(c.ZKProof, mptcrypto.SendProofSize)
	var challenge [mptcrypto.BlindingFactorSize]byte
	copy(challenge[:], proof[:mptcrypto.BlindingFactorSize])

	var ok bool
	if sender.ConfidentialBalanceSpending, ok = mptcrypto.SubtractCiphertexts(sender.ConfidentialBalanceSpending, senderCiphertext); !ok {
		return ter.TecINTERNAL
	}
	if sender.IssuerEncryptedBalance, ok = mptcrypto.SubtractCiphertexts(sender.IssuerEncryptedBalance, issuerCiphertext); !ok {
		return ter.TecINTERNAL
	}
	if c.AuditorEncryptedAmount != nil {
		auditorCiphertext, _ := decodeFixed(*c.AuditorEncryptedAmount, mptcrypto.CiphertextSize)
		if sender.AuditorEncryptedBalance, ok = mptcrypto.SubtractCiphertexts(sender.AuditorEncryptedBalance, auditorCiphertext); !ok {
			return ter.TecINTERNAL
		}
	}

	rerandomizedDestination, ok := mptcrypto.RerandomizeCiphertext(destinationCiphertext, destination.HolderEncryptionKey, challenge)
	if !ok {
		return ter.TecINTERNAL
	}
	if destination.ConfidentialBalanceInbox, ok = mptcrypto.AddCiphertexts(destination.ConfidentialBalanceInbox, rerandomizedDestination); !ok {
		return ter.TecINTERNAL
	}
	rerandomizedIssuer, ok := mptcrypto.RerandomizeCiphertext(issuerCiphertext, issuance.IssuerEncryptionKey, challenge)
	if !ok {
		return ter.TecINTERNAL
	}
	if destination.IssuerEncryptedBalance, ok = mptcrypto.AddCiphertexts(destination.IssuerEncryptedBalance, rerandomizedIssuer); !ok {
		return ter.TecINTERNAL
	}
	if c.AuditorEncryptedAmount != nil {
		auditorCiphertext, _ := decodeFixed(*c.AuditorEncryptedAmount, mptcrypto.CiphertextSize)
		rerandomizedAuditor, rerandomized := mptcrypto.RerandomizeCiphertext(auditorCiphertext, issuance.AuditorEncryptionKey, challenge)
		if !rerandomized {
			return ter.TecINTERNAL
		}
		if destination.AuditorEncryptedBalance, ok = mptcrypto.AddCiphertexts(destination.AuditorEncryptedBalance, rerandomizedAuditor); !ok {
			return ter.TecINTERNAL
		}
	}
	sender.ConfidentialBalanceVersion++

	senderData, err := state.SerializeMPToken(sender)
	if err != nil {
		return ter.TecINTERNAL
	}
	destinationData, err := state.SerializeMPToken(destination)
	if err != nil {
		return ter.TecINTERNAL
	}
	if err := ctx.View.Update(senderKeylet, senderData); err != nil {
		return ter.TecINTERNAL
	}
	if err := ctx.View.Update(destinationKeylet, destinationData); err != nil {
		return ter.TecINTERNAL
	}
	return ter.TesSUCCESS
}
