package jtx

import (
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/LeJamon/go-xrpl/internal/tx/sign"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// SubmitOptions selects explicit autofill opt-outs for malformed and specialized
// transaction tests. Ordinary submissions should use Submit.
type SubmitOptions struct {
	SkipFee       bool
	SkipSequence  bool
	SkipNetworkID bool
	SkipSignature bool
}

// WithSeq sets the sequence number on a transaction manually.
// This bypasses autofill and allows testing transactions from non-existent accounts.
// Reference: rippled's seq(1) funclet in test/jtx/seq.h
func WithSeq(transaction tx.Transaction, seq uint32) tx.Transaction {
	transaction.GetCommon().Sequence = &seq
	return transaction
}

// formatUint64 formats a uint64 as a base-10 string (for XRP amounts).
func formatUint64(n uint64) string {
	return strconv.FormatUint(n, 10)
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
	common.SigningPubKey = signer.PublicKeyHex()

	if !e.VerifySignatures {
		// When signature verification is disabled, use a dummy signature
		// matching rippled's autofill_sig() behavior (Env.cpp line 649):
		//   jv[jss::TxnSignature] = "00";
		// This ensures identical binary serialization and tx hashes.
		common.TxnSignature = "00"
	} else {
		sig, err := sign.SignTransaction(txn, privateKeyHex(signer))
		if err != nil {
			e.t.Fatalf("Failed to sign transaction: %v", err)
		}
		common.TxnSignature = sig
	}

	return txn
}

// signReal always computes a real cryptographic signature, regardless of
// VerifySignatures. Used by SubmitSigned/SubmitSignedWith which submit
// with verification enabled and need actual valid signatures.
func (e *TestEnv) signReal(txn tx.Transaction, signer *Account) {
	e.t.Helper()
	common := txn.GetCommon()
	common.SigningPubKey = signer.PublicKeyHex()
	sig, err := sign.SignTransaction(txn, privateKeyHex(signer))
	if err != nil {
		e.t.Fatalf("Failed to sign transaction: %v", err)
	}
	common.TxnSignature = sig
}

// SubmitSigned signs the transaction with the account's own key and submits
// with signature verification enabled.
// The signing account is inferred from the transaction's Account field.
func (e *TestEnv) SubmitSigned(txn tx.Transaction) TxResult {
	e.t.Helper()
	previousVerify := e.VerifySignatures
	e.VerifySignatures = true
	defer func() { e.VerifySignatures = previousVerify }()
	if txn == nil || txn.GetCommon() == nil {
		e.t.Fatal("SubmitSigned: nil transaction")
	}

	// Look up the account by address
	acc := e.findAccountByAddress(txn.GetCommon().Account)
	if acc == nil {
		e.t.Fatalf("SubmitSigned: account %s not registered in test env", txn.GetCommon().Account)
	}

	// Auto-fill BEFORE signing, since sequence/fee are part of the signed payload.
	e.autoFillForSigning(txn)
	e.signReal(txn, acc) // Always use real signature for verified submission
	return e.submitWithSigVerification(txn)
}

// SubmitSignedWith signs the transaction with a different key (e.g. a regular key)
// and submits with signature verification enabled.
// Reference: rippled's sig(account) -- sign with regular key.
func (e *TestEnv) SubmitSignedWith(txn tx.Transaction, signer *Account) TxResult {
	e.t.Helper()
	previousVerify := e.VerifySignatures
	e.VerifySignatures = true
	defer func() { e.VerifySignatures = previousVerify }()

	// Auto-fill BEFORE signing, since sequence/fee are part of the signed payload.
	e.autoFillForSigning(txn)
	e.signReal(txn, signer) // Always use real signature for verified submission
	return e.submitWithSigVerification(txn)
}

// SubmitMultiSigned attaches multi-signatures from the given signers and submits
// with signature verification enabled.
// Each signer signs the transaction with their key, sorted by account ID.
// Reference: rippled's msig(signers...) funclet.
func (e *TestEnv) SubmitMultiSigned(txn tx.Transaction, signers []*Account) TxResult {
	e.t.Helper()
	previousVerify := e.VerifySignatures
	e.VerifySignatures = true
	defer func() { e.VerifySignatures = previousVerify }()

	// Auto-fill BEFORE signing, since sequence/fee are part of the signed payload.
	e.autoFillForSigning(txn)

	common := txn.GetCommon()

	// Clear single-signature fields for multi-sign
	common.SigningPubKey = ""
	common.TxnSignature = ""

	// Calculate multi-sign fee: (numSigners + 1) * baseFee.
	// Only override if the fee isn't already set higher (e.g., AccountDelete
	// requires a higher fee than the standard multi-sign minimum).
	fee, err := multiSignFee(common.Fee, len(signers), e.baseFee)
	if err != nil {
		e.t.Fatalf("invalid multi-sign fee: %v", err)
	}
	common.Fee = fee

	// Each signer signs and is added (AddMultiSigner maintains sorted order)
	for _, signer := range signers {
		sig, err := sign.SignTransactionForMultiSign(txn, signer.Address, privateKeyHex(signer))
		if err != nil {
			e.t.Fatalf("Failed to multi-sign for %s: %v", signer.Name, err)
		}

		err = sign.AddMultiSigner(txn, signer.Address, signer.PublicKeyHex(), sig)
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
	e.autoFill(txn, SubmitOptions{SkipSignature: true})
}

func (e *TestEnv) autoFill(txn tx.Transaction, options SubmitOptions) {
	e.t.Helper()
	if txn == nil || txn.GetCommon() == nil {
		e.t.Fatal("cannot autofill nil transaction")
	}
	common := txn.GetCommon()
	if !options.SkipFee && common.Fee == "" {
		config := e.engineConfig(e.ledger, engineConfigOpts{openLedger: e.openLedger})
		common.Fee = formatUint64(sign.CalculateBaseFee(txn, e.ledger, config))
	}
	if !options.SkipSequence && common.Sequence == nil {
		_, accountID, err := addresscodec.DecodeClassicAddressToAccountID(common.Account)
		if err != nil || len(accountID) != 20 {
			e.t.Fatalf("autoFillForSigning: failed to decode account address: %v", err)
		}

		var id [20]byte
		copy(id[:], accountID)
		accountRoot, exists, err := readAccountRoot(e.ledger, id)
		if err != nil {
			e.t.Fatalf("autoFillForSigning: failed to read account: %v", err)
		}
		if !exists {
			e.t.Fatalf("autoFillForSigning: account %s does not exist", common.Account)
		}

		seq := accountRoot.Sequence
		common.Sequence = &seq
	}
	if !options.SkipNetworkID && common.NetworkID == nil && e.networkID > tx.LegacyNetworkIDThreshold {
		networkID := e.networkID
		common.NetworkID = &networkID
	}
	if options.SkipSignature {
		return
	}
	signerAddress := common.Account
	if common.Delegate != "" {
		signerAddress = common.Delegate
	}
	signer := e.findAccountByAddress(signerAddress)
	if signer == nil {
		e.t.Fatalf("autofill signature: account %s is not registered", signerAddress)
	}
	if e.VerifySignatures {
		signingAccount := signer
		accountRoot, exists, err := readAccountRoot(e.ledger, signer.ID)
		if err != nil {
			e.t.Fatalf("autofill signature: read account %s: %v", signerAddress, err)
		}
		if exists && accountRoot.RegularKey != "" {
			signingAccount = e.findAccountByAddress(accountRoot.RegularKey)
			if signingAccount == nil {
				e.t.Fatalf("autofill signature: regular key %s is not registered", accountRoot.RegularKey)
			}
		}
		if len(signingAccount.PublicKey) == 0 || len(signingAccount.PrivateKey) == 0 {
			e.t.Fatalf("autofill signature: account %s has no signing key", signingAccount.Address)
		}
		e.signReal(txn, signingAccount)
	} else {
		if len(signer.PublicKey) == 0 {
			e.t.Fatalf("autofill signature: account %s has no signing key", signerAddress)
		}
		e.SignWith(txn, signer)
	}
}

func multiSignFee(current string, signerCount int, baseFee uint64) (string, error) {
	if signerCount < 0 {
		return "", fmt.Errorf("signer count cannot be negative")
	}
	multiplier := uint64(signerCount) + 1
	if baseFee != 0 && multiplier > ^uint64(0)/baseFee {
		return "", fmt.Errorf("minimum fee overflows uint64")
	}
	minimum := multiplier * baseFee
	parsed, err := strconv.ParseUint(current, 10, 64)
	if err != nil {
		return "", err
	}
	if parsed < minimum {
		parsed = minimum
	}
	return formatUint64(parsed), nil
}

// submitWithSigVerification is the internal submit path with signature
// verification enabled. Callers must auto-fill and sign BEFORE calling this.
//
// It mirrors applyDirect so mixed signed and ordinary submissions share the
// same metadata indexes and TxQ fee accounting.
func (e *TestEnv) submitWithSigVerification(txn tx.Transaction) TxResult {
	e.t.Helper()
	previousVerify := e.VerifySignatures
	e.VerifySignatures = true
	defer func() { e.VerifySignatures = previousVerify }()
	if e.txQueue != nil && !e.bypassTxQ {
		return e.submitViaTxQ(txn)
	}
	return e.applyDirect(txn)
}

// findAccountByAddress looks up a registered account by its XRPL address.
func (e *TestEnv) findAccountByAddress(address string) *Account {
	return e.accountsByAddress[address]
}
