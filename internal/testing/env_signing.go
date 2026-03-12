package testing

import (
	"encoding/hex"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

// DecodeAddress decodes an XRPL address to a 20-byte account ID.
func DecodeAddress(address string) ([20]byte, error) {
	_, accountIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(address)
	if err != nil {
		return [20]byte{}, err
	}

	var accountID [20]byte
	copy(accountID[:], accountIDBytes)
	return accountID, nil
}

// WithSeq sets the sequence number on a transaction manually.
// This bypasses autofill and allows testing transactions from non-existent accounts.
// Reference: rippled's seq(1) funclet in test/jtx/seq.h
func WithSeq(transaction tx.Transaction, seq uint32) tx.Transaction {
	transaction.GetCommon().Sequence = &seq
	return transaction
}

// formatUint64 formats a uint64 as a string (for XRP amounts).
func formatUint64(n uint64) string {
	// Simple conversion without importing strconv
	if n == 0 {
		return "0"
	}

	digits := make([]byte, 0, 20)
	for n > 0 {
		digits = append(digits, byte('0'+n%10))
		n /= 10
	}

	// Reverse
	for i, j := 0, len(digits)-1; i < j; i, j = i+1, j-1 {
		digits[i], digits[j] = digits[j], digits[i]
	}

	return string(digits)
}

// privateKeyHex returns the prefixed hex private key for use with tx.SignTransaction.
// tx.SignTransaction expects 0x00 prefix for secp256k1 and 0xED prefix for ed25519.
func privateKeyHex(acc *Account) string {
	switch acc.KeyType {
	case KeyTypeEd25519:
		return "ED" + hex.EncodeToString(acc.PrivateKey)
	case KeyTypeSecp256k1:
		return "00" + hex.EncodeToString(acc.PrivateKey)
	default:
		panic("unsupported key type: " + acc.KeyType)
	}
}

// SignWith signs a transaction using a specific account's key pair.
// Sets SigningPubKey and TxnSignature on the transaction.
// Reference: rippled's sig.h -- sig(account) funclet.
func (e *TestEnv) SignWith(txn tx.Transaction, signer *Account) tx.Transaction {
	e.t.Helper()

	common := txn.GetCommon()
	common.SigningPubKey = hex.EncodeToString(signer.PublicKey)

	sig, err := tx.SignTransaction(txn, privateKeyHex(signer))
	if err != nil {
		e.t.Fatalf("Failed to sign transaction: %v", err)
	}
	common.TxnSignature = sig

	return txn
}

// SubmitSigned signs the transaction with the account's own key and submits
// with signature verification enabled.
// The signing account is inferred from the transaction's Account field.
func (e *TestEnv) SubmitSigned(transaction interface{}) TxResult {
	e.t.Helper()

	txn, ok := transaction.(tx.Transaction)
	if !ok {
		e.t.Fatalf("Transaction does not implement tx.Transaction interface")
		return TxResult{Code: "temINVALID", Success: false, Message: "Invalid transaction type"}
	}

	// Look up the account by address
	acc := e.findAccountByAddress(txn.GetCommon().Account)
	if acc == nil {
		e.t.Fatalf("SubmitSigned: account %s not registered in test env", txn.GetCommon().Account)
		return TxResult{Code: "terNO_ACCOUNT", Success: false, Message: "Account not found"}
	}

	// Auto-fill BEFORE signing, since sequence/fee are part of the signed payload.
	e.autoFillForSigning(txn)
	e.SignWith(txn, acc)
	return e.submitWithSigVerification(txn)
}

// SubmitSignedWith signs the transaction with a different key (e.g. a regular key)
// and submits with signature verification enabled.
// Reference: rippled's sig(account) -- sign with regular key.
func (e *TestEnv) SubmitSignedWith(transaction interface{}, signer *Account) TxResult {
	e.t.Helper()

	txn, ok := transaction.(tx.Transaction)
	if !ok {
		e.t.Fatalf("Transaction does not implement tx.Transaction interface")
		return TxResult{Code: "temINVALID", Success: false, Message: "Invalid transaction type"}
	}

	// Auto-fill BEFORE signing, since sequence/fee are part of the signed payload.
	e.autoFillForSigning(txn)
	e.SignWith(txn, signer)
	return e.submitWithSigVerification(txn)
}

// SubmitMultiSigned attaches multi-signatures from the given signers and submits
// with signature verification enabled.
// Each signer signs the transaction with their key, sorted by account ID.
// Reference: rippled's msig(signers...) funclet.
func (e *TestEnv) SubmitMultiSigned(transaction interface{}, signers []*Account) TxResult {
	e.t.Helper()

	txn, ok := transaction.(tx.Transaction)
	if !ok {
		e.t.Fatalf("Transaction does not implement tx.Transaction interface")
		return TxResult{Code: "temINVALID", Success: false, Message: "Invalid transaction type"}
	}

	// Auto-fill BEFORE signing, since sequence/fee are part of the signed payload.
	e.autoFillForSigning(txn)

	common := txn.GetCommon()

	// Clear single-signature fields for multi-sign
	common.SigningPubKey = ""
	common.TxnSignature = ""

	// Calculate multi-sign fee: (numSigners + 1) * baseFee
	multisigFee := uint64(len(signers)+1) * e.baseFee
	common.Fee = formatUint64(multisigFee)

	// Each signer signs and is added (AddMultiSigner maintains sorted order)
	for _, signer := range signers {
		sig, err := tx.SignTransactionForMultiSign(txn, signer.Address, privateKeyHex(signer))
		if err != nil {
			e.t.Fatalf("Failed to multi-sign for %s: %v", signer.Name, err)
		}

		err = tx.AddMultiSigner(txn, signer.Address, hex.EncodeToString(signer.PublicKey), sig)
		if err != nil {
			e.t.Fatalf("Failed to add multi-signer %s: %v", signer.Name, err)
		}
	}

	return e.submitWithSigVerification(txn)
}

// autoFillForSigning fills in sequence and fee fields before signing.
// This must be called before signing, since these fields are part of the signed payload.
func (e *TestEnv) autoFillForSigning(txn tx.Transaction) {
	e.t.Helper()

	common := txn.GetCommon()

	// Auto-fill sequence if not set
	if common.Sequence == nil && common.TicketSequence == nil {
		_, accountID, err := addresscodec.DecodeClassicAddressToAccountID(common.Account)
		if err != nil {
			e.t.Fatalf("autoFillForSigning: failed to decode account address: %v", err)
			return
		}

		var id [20]byte
		copy(id[:], accountID)
		accountKey := keylet.Account(id)

		data, err := e.ledger.Read(accountKey)
		if err != nil || data == nil {
			e.t.Fatalf("autoFillForSigning: failed to read account: %v", err)
			return
		}

		accountRoot, err := state.ParseAccountRootFromBytes(data)
		if err != nil {
			e.t.Fatalf("autoFillForSigning: failed to parse account root: %v", err)
			return
		}

		seq := accountRoot.Sequence
		common.Sequence = &seq
	}

	// Auto-fill fee if not set
	if common.Fee == "" {
		common.Fee = formatUint64(e.baseFee)
	}
}

// submitWithSigVerification is the internal submit path with signature verification enabled.
// Callers must auto-fill and sign BEFORE calling this.
func (e *TestEnv) submitWithSigVerification(txn tx.Transaction) TxResult {
	e.t.Helper()

	parentCloseTime := uint32(e.clock.Now().Unix() - 946684800)
	engineConfig := tx.EngineConfig{
		BaseFee:                   e.baseFee,
		ReserveBase:               e.reserveBase,
		ReserveIncrement:          e.reserveIncrement,
		LedgerSequence:            e.ledger.Sequence(),
		SkipSignatureVerification: false, // Verify signatures
		Rules:                     e.rulesBuilder.Build(),
		ParentCloseTime:           parentCloseTime,
		NetworkID:                 e.networkID,
		ParentHash:                e.ledger.ParentHash(),
	}

	engine := tx.NewEngine(e.ledger, engineConfig)
	applyResult := engine.Apply(txn)

	return TxResult{
		Code:    applyResult.Result.String(),
		Success: applyResult.Result.IsSuccess(),
		Message: applyResult.Message,
	}
}

// findAccountByAddress looks up a registered account by its XRPL address.
func (e *TestEnv) findAccountByAddress(address string) *Account {
	for _, acc := range e.accounts {
		if acc.Address == address {
			return acc
		}
	}
	return nil
}
