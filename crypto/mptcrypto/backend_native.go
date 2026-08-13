//go:build mptcrypto && cgo

package mptcrypto

import "github.com/LeJamon/go-xrpl/crypto/mptcrypto/internal/native"

// Available reports whether the native confidential MPT backend is available.
func Available() bool { return native.Available() }

// ValidPublicKey reports whether value is a valid confidential MPT public key.
func ValidPublicKey(value []byte) bool { return native.ValidPublicKey(value) }

// ValidCommitment reports whether value is a valid Pedersen commitment.
func ValidCommitment(value []byte) bool { return native.ValidCommitment(value) }

// ValidCiphertext reports whether value is a valid encrypted balance.
func ValidCiphertext(value []byte) bool { return native.ValidCiphertext(value) }

// AddCiphertexts adds two encrypted balances.
func AddCiphertexts(a, b []byte) ([]byte, bool) { return native.AddCiphertexts(a, b) }

// SubtractCiphertexts subtracts one encrypted balance from another.
func SubtractCiphertexts(a, b []byte) ([]byte, bool) {
	return native.SubtractCiphertexts(a, b)
}

// CanonicalZero creates the canonical encrypted zero for an account and issuance.
func CanonicalZero(pub []byte, account [20]byte, issuance [24]byte) ([]byte, bool) {
	return native.CanonicalZero(pub, account, issuance)
}

// ConvertContext derives the proof context for a confidential conversion.
func ConvertContext(account [20]byte, issuance [24]byte, sequence uint32) ([32]byte, bool) {
	return native.ConvertContext(account, issuance, sequence)
}

// ConvertBackContext derives the proof context for a conversion back to a public balance.
func ConvertBackContext(account [20]byte, issuance [24]byte, sequence, version uint32) ([32]byte, bool) {
	return native.ConvertBackContext(account, issuance, sequence, version)
}

// SendContext derives the proof context for a confidential transfer.
func SendContext(account [20]byte, issuance [24]byte, sequence uint32, destination [20]byte, version uint32) ([32]byte, bool) {
	return native.SendContext(account, issuance, sequence, destination, version)
}

// ClawbackContext derives the proof context for a confidential clawback.
func ClawbackContext(account [20]byte, issuance [24]byte, sequence uint32, holder [20]byte) ([32]byte, bool) {
	return native.ClawbackContext(account, issuance, sequence, holder)
}

func nativeParticipant(value Participant) native.Participant {
	return native.Participant{PublicKey: value.PublicKey, Ciphertext: value.Ciphertext}
}

func nativeAuditor(value *Participant) *native.Participant {
	if value == nil {
		return nil
	}
	converted := nativeParticipant(*value)
	return &converted
}

// VerifyRevealed verifies that revealed balance data matches every participant ciphertext.
func VerifyRevealed(amount uint64, blind [32]byte, holder, issuer Participant, auditor *Participant) bool {
	return native.VerifyRevealed(
		amount,
		blind,
		nativeParticipant(holder),
		nativeParticipant(issuer),
		nativeAuditor(auditor),
	)
}

// VerifyConvert verifies a confidential conversion proof.
func VerifyConvert(proof, pub []byte, context [32]byte) bool {
	return native.VerifyConvert(proof, pub, context)
}

// VerifyConvertBack verifies a confidential conversion-back proof.
func VerifyConvertBack(proof, pub, spending, commitment []byte, amount uint64, context [32]byte) bool {
	return native.VerifyConvertBack(proof, pub, spending, commitment, amount, context)
}

// VerifySend verifies a confidential transfer proof.
func VerifySend(proof []byte, sender, destination, issuer Participant, auditor *Participant, spending, amountCommitment, balanceCommitment []byte, context [32]byte) bool {
	return native.VerifySend(
		proof,
		nativeParticipant(sender),
		nativeParticipant(destination),
		nativeParticipant(issuer),
		nativeAuditor(auditor),
		spending,
		amountCommitment,
		balanceCommitment,
		context,
	)
}

// VerifyClawback verifies a confidential clawback proof.
func VerifyClawback(proof, pub, ciphertext []byte, amount uint64, context [32]byte) bool {
	return native.VerifyClawback(proof, pub, ciphertext, amount, context)
}

// RerandomizeCiphertext rerandomizes an encrypted balance.
func RerandomizeCiphertext(ciphertext, pub []byte, randomness [32]byte) ([]byte, bool) {
	return native.RerandomizeCiphertext(ciphertext, pub, randomness)
}

// GenerateKeyPair creates a confidential MPT private and public key pair.
func GenerateKeyPair() ([32]byte, []byte, bool) { return native.GenerateKeyPair() }

// GenerateBlindingFactor creates a cryptographically secure blinding factor.
func GenerateBlindingFactor() ([32]byte, bool) { return native.GenerateBlindingFactor() }

// EncryptAmount encrypts an amount for a public key with the supplied blinding factor.
func EncryptAmount(amount uint64, public []byte, blind [32]byte) ([]byte, bool) {
	return native.EncryptAmount(amount, public, blind)
}

// GenerateConvertProof creates a confidential conversion proof.
func GenerateConvertProof(public []byte, private [32]byte, context [32]byte) ([]byte, bool) {
	return native.GenerateConvertProof(public, private, context)
}

// PedersenCommitment creates a commitment to an amount and blinding factor.
func PedersenCommitment(amount uint64, blind [32]byte) ([]byte, bool) {
	return native.PedersenCommitment(amount, blind)
}

// GenerateConvertBackProof creates a confidential conversion-back proof.
func GenerateConvertBackProof(private [32]byte, public []byte, context [32]byte, amount uint64, balanceCommitment []byte, balance uint64, spending []byte, balanceBlind [32]byte) ([]byte, bool) {
	return native.GenerateConvertBackProof(
		private,
		public,
		context,
		amount,
		balanceCommitment,
		balance,
		spending,
		balanceBlind,
	)
}

// GenerateSendProof creates a confidential transfer proof.
func GenerateSendProof(private [32]byte, amount uint64, participants []Participant, transactionBlind [32]byte, context [32]byte, amountCommitment, balanceCommitment []byte, balance uint64, spending []byte, balanceBlind [32]byte) ([]byte, bool) {
	converted := make([]native.Participant, len(participants))
	for i := range participants {
		converted[i] = nativeParticipant(participants[i])
	}
	return native.GenerateSendProof(
		private,
		amount,
		converted,
		transactionBlind,
		context,
		amountCommitment,
		balanceCommitment,
		balance,
		spending,
		balanceBlind,
	)
}

// GenerateClawbackProof creates a confidential clawback proof.
func GenerateClawbackProof(private [32]byte, public []byte, context [32]byte, amount uint64, ciphertext []byte) ([]byte, bool) {
	return native.GenerateClawbackProof(private, public, context, amount, ciphertext)
}
