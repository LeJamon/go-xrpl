package sign

import (
	"testing"

	txcore "github.com/LeJamon/go-xrpl/internal/tx"
)

func sponsorSignedTx(t *testing.T) txcore.Transaction {
	t.Helper()

	primaryPriv, primaryPub, primaryAddress := cpKeypair(t, 0x71)
	_, _, sponsorAddress := cpKeypair(t, 0x72)
	transaction := txcore.NewBaseTx(txcore.TypeAccountSet, primaryAddress)
	sequence := uint32(7)
	sponsorFlags := txcore.SpfSponsorFee
	transaction.Common.Sequence = &sequence
	transaction.Common.Fee = "30"
	transaction.Common.SigningPubKey = primaryPub
	transaction.Common.Sponsor = sponsorAddress
	transaction.Common.SponsorFlags = &sponsorFlags

	signature, err := SignTransaction(transaction, primaryPriv)
	if err != nil {
		t.Fatalf("sign primary: %v", err)
	}
	transaction.Common.TxnSignature = signature
	return transaction
}

func TestSponsorSignatureSigningPayloadGoldenAndExcluded(t *testing.T) {
	transaction := sponsorSignedTx(t)

	without, err := getSigningPayload(transaction)
	if err != nil {
		t.Fatalf("payload without sponsor signature: %v", err)
	}
	const want = "535458001200032400000007204A0000000168400000000000001E" +
		"7321ED3B647A635E9ACF162B1F262885774A7C373F66FE5AFEE2F413BED997496287" +
		"4F81148DB791CC6DA37E274C9C65C62EA7A95821DABB6A801B14EF6D30FF728633B" +
		"134E5C7A9E7B0C5E6F37FF77F"
	if without != want {
		t.Fatalf("signing payload = %s", without)
	}

	sponsorPriv, sponsorPub, _ := cpKeypair(t, 0x72)
	sponsorSignature, err := SignSponsor(transaction, sponsorPub, sponsorPriv)
	if err != nil {
		t.Fatalf("sign sponsor: %v", err)
	}
	transaction.GetCommon().SponsorSignature = sponsorSignature

	with, err := getSigningPayload(transaction)
	if err != nil {
		t.Fatalf("payload with sponsor signature: %v", err)
	}
	if with != without {
		t.Fatalf("SponsorSignature changed signing payload:\n with=%s\n without=%s", with, without)
	}
	if err := VerifySponsorSignature(transaction, sponsorSignature, false); err != nil {
		t.Fatalf("valid sponsor signature rejected: %v", err)
	}
}

func TestSponsorSignatureBindsTransactionFields(t *testing.T) {
	transaction := sponsorSignedTx(t)
	sponsorPriv, sponsorPub, _ := cpKeypair(t, 0x72)
	sponsorSignature, err := SignSponsor(transaction, sponsorPub, sponsorPriv)
	if err != nil {
		t.Fatalf("sign sponsor: %v", err)
	}
	transaction.GetCommon().SponsorSignature = sponsorSignature

	common := transaction.GetCommon()
	common.Fee = "31"
	if err := VerifySponsorSignature(transaction, sponsorSignature, false); err == nil {
		t.Fatal("sponsor signature accepted after Fee mutation")
	}
	common.Fee = "30"

	mutatedSequence := uint32(8)
	common.Sequence = &mutatedSequence
	if err := VerifySponsorSignature(transaction, sponsorSignature, false); err == nil {
		t.Fatal("sponsor signature accepted after Sequence mutation")
	}
	common.Sequence = func() *uint32 {
		sequence := uint32(7)
		return &sequence
	}()

	_, _, otherSponsor := cpKeypair(t, 0x73)
	common.Sponsor = otherSponsor
	if err := VerifySponsorSignature(transaction, sponsorSignature, false); err == nil {
		t.Fatal("sponsor signature accepted after Sponsor mutation")
	}
}

func TestSponsorSignatureMultiSignValidation(t *testing.T) {
	transaction := sponsorSignedTx(t)

	signer1Priv, signer1Pub, signer1Address := cpKeypair(t, 0x74)
	signer2Priv, signer2Pub, signer2Address := cpKeypair(t, 0x75)
	signature1, err := SignTransactionForMultiSignTarget(transaction, signer1Address, signer1Priv)
	if err != nil {
		t.Fatalf("sign sponsor signer 1: %v", err)
	}
	signature2, err := SignTransactionForMultiSignTarget(transaction, signer2Address, signer2Priv)
	if err != nil {
		t.Fatalf("sign sponsor signer 2: %v", err)
	}
	signers := []txcore.SignerWrapper{
		{Signer: txcore.Signer{Account: signer1Address, SigningPubKey: signer1Pub, TxnSignature: signature1}},
		{Signer: txcore.Signer{Account: signer2Address, SigningPubKey: signer2Pub, TxnSignature: signature2}},
	}
	sortSigners(signers)

	sponsorSignature := &txcore.SponsorSignature{Signers: signers}
	if err := VerifySponsorSignature(transaction, sponsorSignature, false); err != nil {
		t.Fatalf("valid sponsor multisignature rejected: %v", err)
	}

	unsorted := append([]txcore.SignerWrapper(nil), signers...)
	unsorted[0], unsorted[1] = unsorted[1], unsorted[0]
	if err := VerifySponsorSignature(
		transaction,
		&txcore.SponsorSignature{Signers: unsorted},
		false,
	); err == nil {
		t.Fatal("unsorted sponsor multisigners accepted")
	}

	duplicate := []txcore.SignerWrapper{signers[0], signers[0]}
	if err := VerifySponsorSignature(
		transaction,
		&txcore.SponsorSignature{Signers: duplicate},
		false,
	); err == nil {
		t.Fatal("duplicate sponsor multisigners accepted")
	}

	signedTwice := &txcore.SponsorSignature{
		SigningPubKey: signer1Pub,
		TxnSignature:  signature1,
		Signers:       signers,
	}
	if err := VerifySponsorSignature(transaction, signedTwice, false); err == nil {
		t.Fatal("single- and multi-signed sponsor object accepted")
	}
}

func TestCalculateDefaultBaseFeeIncludesSponsorMultisigners(t *testing.T) {
	config := txcore.EngineConfig{BaseFee: 10}
	testCases := []struct {
		name           string
		outerSigners   int
		sponsorSigners int
		want           uint64
	}{
		{name: "ordinary single sign", want: 10},
		{name: "ordinary multisign", outerSigners: 2, want: 30},
		{name: "direct sponsor signature adds no unit", sponsorSigners: 0, want: 10},
		{name: "sponsor multisign", sponsorSigners: 2, want: 30},
		{name: "outer and sponsor multisign", outerSigners: 2, sponsorSigners: 2, want: 50},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			transaction := txcore.NewBaseTx(txcore.TypeAccountSet, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh")
			if testCase.outerSigners > 0 {
				transaction.Common.Signers = make([]txcore.SignerWrapper, testCase.outerSigners)
			}
			if testCase.name == "direct sponsor signature adds no unit" {
				transaction.Common.SponsorSignature = &txcore.SponsorSignature{
					SigningPubKey: "ED01",
					TxnSignature:  "AA",
				}
			} else if testCase.sponsorSigners > 0 {
				transaction.Common.SponsorSignature = &txcore.SponsorSignature{
					Signers: make([]txcore.SignerWrapper, testCase.sponsorSigners),
				}
			}
			if got := CalculateDefaultBaseFee(transaction, config); got != testCase.want {
				t.Fatalf("CalculateDefaultBaseFee() = %d, want %d", got, testCase.want)
			}
		})
	}
}
