//go:build mptcrypto && cgo

package mptcrypto

import "testing"

func TestNativeBackendFailsClosedWithoutContext(t *testing.T) {
	forceUnavailableForTest(true)
	t.Cleanup(func() { forceUnavailableForTest(false) })

	if Available() {
		t.Fatal("backend reported available without a context")
	}
	pub := make([]byte, PublicKeySize)
	ciphertext := make([]byte, CiphertextSize)
	commitment := make([]byte, CommitmentSize)
	convertProof := make([]byte, ConvertProofSize)
	convertBackProof := make([]byte, ConvertBackProofSize)
	sendProof := make([]byte, SendProofSize)
	clawbackProof := make([]byte, ClawbackProofSize)
	participant := Participant{PublicKey: pub, Ciphertext: ciphertext}
	participants := []Participant{participant, participant, participant}
	var account [20]byte
	var issuance [24]byte
	var value [32]byte

	if ValidPublicKey(pub) || ValidCiphertext(ciphertext) {
		t.Fatal("validation succeeded without a context")
	}
	if _, ok := AddCiphertexts(ciphertext, ciphertext); ok {
		t.Fatal("addition succeeded without a context")
	}
	if _, ok := SubtractCiphertexts(ciphertext, ciphertext); ok {
		t.Fatal("subtraction succeeded without a context")
	}
	if _, ok := CanonicalZero(pub, account, issuance); ok {
		t.Fatal("canonical zero succeeded without a context")
	}
	if _, ok := ConvertContext(account, issuance, 1); ok {
		t.Fatal("convert context succeeded without a context")
	}
	if _, ok := ConvertBackContext(account, issuance, 1, 1); ok {
		t.Fatal("convert-back context succeeded without a context")
	}
	if _, ok := SendContext(account, issuance, 1, account, 1); ok {
		t.Fatal("send context succeeded without a context")
	}
	if _, ok := ClawbackContext(account, issuance, 1, account); ok {
		t.Fatal("clawback context succeeded without a context")
	}
	if VerifyRevealed(1, value, participant, participant, nil) ||
		VerifyConvert(convertProof, pub, value) ||
		VerifyConvertBack(convertBackProof, pub, ciphertext, commitment, 1, value) ||
		VerifySend(sendProof, participant, participant, participant, nil, ciphertext, commitment, commitment, value) ||
		VerifyClawback(clawbackProof, pub, ciphertext, 1, value) {
		t.Fatal("proof verification succeeded without a context")
	}
	if _, ok := RerandomizeCiphertext(ciphertext, pub, value); ok {
		t.Fatal("rerandomization succeeded without a context")
	}
	if _, _, ok := GenerateKeyPair(); ok {
		t.Fatal("key generation succeeded without a context")
	}
	if _, ok := GenerateBlindingFactor(); ok {
		t.Fatal("blinding generation succeeded without a context")
	}
	if _, ok := EncryptAmount(1, pub, value); ok {
		t.Fatal("encryption succeeded without a context")
	}
	if _, ok := GenerateConvertProof(pub, value, value); ok {
		t.Fatal("convert proof generation succeeded without a context")
	}
	if _, ok := PedersenCommitment(1, value); ok {
		t.Fatal("commitment generation succeeded without a context")
	}
	if _, ok := GenerateConvertBackProof(value, pub, value, 1, commitment, 1, ciphertext, value); ok {
		t.Fatal("convert-back proof generation succeeded without a context")
	}
	if _, ok := GenerateSendProof(value, 1, participants, value, value, commitment, commitment, 1, ciphertext, value); ok {
		t.Fatal("send proof generation succeeded without a context")
	}
	if _, ok := GenerateClawbackProof(value, pub, value, 1, ciphertext); ok {
		t.Fatal("clawback proof generation succeeded without a context")
	}
}

func mustKeyPair(t *testing.T) ([32]byte, []byte) {
	t.Helper()
	private, public, ok := GenerateKeyPair()
	if !ok {
		t.Fatal("generate key pair")
	}
	return private, public
}

func mustBlind(t *testing.T) [32]byte {
	t.Helper()
	blind, ok := GenerateBlindingFactor()
	if !ok {
		t.Fatal("generate blinding factor")
	}
	return blind
}

func mustEncrypt(t *testing.T, amount uint64, public []byte, blind [32]byte) []byte {
	t.Helper()
	ciphertext, ok := EncryptAmount(amount, public, blind)
	if !ok {
		t.Fatal("encrypt amount")
	}
	return ciphertext
}

func TestNativeBackendProofsAndArithmetic(t *testing.T) {
	if !Available() {
		t.Fatal("native backend unavailable")
	}
	holderPrivate, holderPublic := mustKeyPair(t)
	_, issuerPublic := mustKeyPair(t)
	_, auditorPublic := mustKeyPair(t)

	var account [20]byte
	var issuance [24]byte
	for i := range account {
		account[i] = byte(i + 1)
	}
	for i := range issuance {
		issuance[i] = byte(i + 2)
	}

	context, ok := ConvertContext(account, issuance, 7)
	if !ok {
		t.Fatal("convert context")
	}
	convertProof, ok := GenerateConvertProof(holderPublic, holderPrivate, context)
	if !ok || !VerifyConvert(convertProof, holderPublic, context) {
		t.Fatal("convert proof")
	}
	wrongContext := context
	wrongContext[0] ^= 1
	if VerifyConvert(convertProof, holderPublic, wrongContext) {
		t.Fatal("convert proof accepted wrong context")
	}

	amountBlind := mustBlind(t)
	holderAmount := mustEncrypt(t, 40, holderPublic, amountBlind)
	issuerAmount := mustEncrypt(t, 40, issuerPublic, amountBlind)
	auditorAmount := mustEncrypt(t, 40, auditorPublic, amountBlind)
	auditor := Participant{PublicKey: auditorPublic, Ciphertext: auditorAmount}
	if !VerifyRevealed(40, amountBlind, Participant{holderPublic, holderAmount}, Participant{issuerPublic, issuerAmount}, &auditor) {
		t.Fatal("revealed amount")
	}
	if VerifyRevealed(41, amountBlind, Participant{holderPublic, holderAmount}, Participant{issuerPublic, issuerAmount}, &auditor) {
		t.Fatal("revealed amount accepted wrong amount")
	}

	secondBlind := mustBlind(t)
	secondAmount := mustEncrypt(t, 2, holderPublic, secondBlind)
	if sum, ok := AddCiphertexts(holderAmount, secondAmount); !ok || !ValidCiphertext(sum) {
		t.Fatal("ciphertext addition")
	}
	if difference, ok := SubtractCiphertexts(holderAmount, secondAmount); !ok || !ValidCiphertext(difference) {
		t.Fatal("ciphertext subtraction")
	}
	if zero, ok := CanonicalZero(holderPublic, account, issuance); !ok || !ValidCiphertext(zero) {
		t.Fatal("canonical zero")
	}

	balanceBlind := mustBlind(t)
	spending := mustEncrypt(t, 100, holderPublic, balanceBlind)
	balanceCommitment, ok := PedersenCommitment(100, balanceBlind)
	if !ok {
		t.Fatal("balance commitment")
	}
	backContext, ok := ConvertBackContext(account, issuance, 8, 3)
	if !ok {
		t.Fatal("convert back context")
	}
	backProof, ok := GenerateConvertBackProof(holderPrivate, holderPublic, backContext, 40, balanceCommitment, 100, spending, balanceBlind)
	if !ok || !VerifyConvertBack(backProof, holderPublic, spending, balanceCommitment, 40, backContext) {
		t.Fatal("convert back proof")
	}
	if VerifyConvertBack(backProof, holderPublic, spending, balanceCommitment, 41, backContext) {
		t.Fatal("convert back proof accepted wrong amount")
	}
}
