//go:build mptcrypto && cgo

package mptcrypto

import "testing"

func mustKeyPair(t *testing.T) ([32]byte, []byte) {
	t.Helper()
	private, public, ok := generateKeyPair()
	if !ok {
		t.Fatal("generate key pair")
	}
	return private, public
}

func mustBlind(t *testing.T) [32]byte {
	t.Helper()
	blind, ok := generateBlinding()
	if !ok {
		t.Fatal("generate blinding factor")
	}
	return blind
}

func mustEncrypt(t *testing.T, amount uint64, public []byte, blind [32]byte) []byte {
	t.Helper()
	ciphertext, ok := encrypt(amount, public, blind)
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
	convertProof, ok := generateConvertProof(holderPublic, holderPrivate, context)
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
	balanceCommitment, ok := commitment(100, balanceBlind)
	if !ok {
		t.Fatal("balance commitment")
	}
	backContext, ok := ConvertBackContext(account, issuance, 8, 3)
	if !ok {
		t.Fatal("convert back context")
	}
	backProof, ok := generateConvertBackProof(holderPrivate, holderPublic, backContext, 40, balanceCommitment, 100, spending, balanceBlind)
	if !ok || !VerifyConvertBack(backProof, holderPublic, spending, balanceCommitment, 40, backContext) {
		t.Fatal("convert back proof")
	}
	if VerifyConvertBack(backProof, holderPublic, spending, balanceCommitment, 41, backContext) {
		t.Fatal("convert back proof accepted wrong amount")
	}
}
