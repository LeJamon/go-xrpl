package service_test

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	delegatetx "github.com/LeJamon/go-xrpl/internal/tx/delegate"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func TestService_SimulateDelegatedMultiSignDryRun(t *testing.T) {
	cfg := defaultServiceConfig()
	cfg.Startup = service.StartupConfig{Mode: service.StartupFresh}
	cfg.GenesisConfig.Amendments = append(
		cfg.GenesisConfig.Amendments,
		amendment.FeaturePermissionDelegationV1_1,
		amendment.FeatureSponsor,
	)
	svc, err := service.New(cfg)
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("service.Start: %v", err)
	}
	defer svc.Stop()

	env := jtx.NewTestEnv(t)
	master := jtx.MasterAccount()
	owner := jtx.NewAccount("owner")
	delegate := jtx.NewAccount("delegate")
	destination := jtx.NewAccount("destination")
	otherSigner := jtx.NewAccount("other-signer")

	masterSeq := accountSeq(t, svc, master.Address)
	for i, account := range []*jtx.Account{owner, delegate, destination, otherSigner} {
		fund := payment.Pay(master, account, 100_000_000).Sequence(masterSeq + uint32(i)).Build()
		mustApply(t, svc, signedBlob(t, env, fund, master))
	}
	closeLedger(t, svc)

	ownerSeq := accountSeq(t, svc, owner.Address)
	delegateSet := delegatetx.NewDelegateSet(owner.Address)
	delegateSet.Authorize = delegate.Address
	delegateSet.Permissions = append(delegateSet.Permissions, delegatetx.NewPermission("Payment"))
	delegateSet.Fee = strconv.FormatUint(env.BaseFee(), 10)
	delegateSet.Sequence = &ownerSeq
	mustApply(t, svc, signedBlob(t, env, delegateSet, owner))

	delegateSeq := accountSeq(t, svc, delegate.Address)
	signerList := jtx.NewSignerListSetTx(delegate, 1, []jtx.TestSigner{{Account: otherSigner, Weight: 1}})
	signerList.GetCommon().Fee = strconv.FormatUint(env.BaseFee(), 10)
	signerList.GetCommon().Sequence = &delegateSeq
	mustApply(t, svc, signedBlob(t, env, signerList, delegate))
	closeLedger(t, svc)

	newSimulated := func() tx.Transaction {
		txn := payment.Pay(owner, destination, 1_000).Build()
		common := txn.GetCommon()
		common.Delegate = delegate.Address
		common.Fee = strconv.FormatUint(2*env.BaseFee(), 10)
		sequence := accountSeq(t, svc, owner.Address)
		common.Sequence = &sequence
		return txn
	}

	t.Run("delegate self-signer reaches authorization", func(t *testing.T) {
		simulated := newSimulated()
		simulated.GetCommon().Signers = []tx.SignerWrapper{{Signer: tx.Signer{Account: delegate.Address}}}

		result, err := svc.SimulateTransaction(simulated)
		if err != nil {
			t.Fatalf("SimulateTransaction: %v", err)
		}
		if result.Result != ter.TefBAD_SIGNATURE || result.Applied {
			t.Fatalf("simulate result = %v, applied = %v; want tefBAD_SIGNATURE, false", result.Result, result.Applied)
		}
	})

	t.Run("single and multi-signing is invalid", func(t *testing.T) {
		simulated := newSimulated()
		common := simulated.GetCommon()
		common.SigningPubKey = owner.PublicKeyHex()
		common.Signers = []tx.SignerWrapper{{Signer: tx.Signer{Account: otherSigner.Address}}}

		result, err := svc.SimulateTransaction(simulated)
		if err != nil {
			t.Fatalf("SimulateTransaction: %v", err)
		}
		if result.Result != ter.TemINVALID || result.Applied {
			t.Fatalf("simulate result = %v, applied = %v; want temINVALID, false", result.Result, result.Applied)
		}
	})

	t.Run("empty signer array reaches quorum check", func(t *testing.T) {
		simulated := newSimulated()
		fields, err := simulated.Flatten()
		if err != nil {
			t.Fatalf("Flatten: %v", err)
		}
		fields["Signers"] = []any{}
		fields["SigningPubKey"] = ""
		fields["TxnSignature"] = ""
		encoded, err := json.Marshal(fields)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		parsed, err := tx.ParseJSON(encoded)
		if err != nil {
			t.Fatalf("ParseJSON: %v", err)
		}

		result, err := svc.SimulateTransaction(parsed)
		if err != nil {
			t.Fatalf("SimulateTransaction: %v", err)
		}
		if result.Result != ter.TefBAD_QUORUM || result.Applied {
			t.Fatalf("simulate result = %v, applied = %v; want tefBAD_QUORUM, false", result.Result, result.Applied)
		}
	})

	t.Run("empty sponsor signer array reaches quorum check", func(t *testing.T) {
		simulated := newSimulated()
		common := simulated.GetCommon()
		common.Signers = []tx.SignerWrapper{{Signer: tx.Signer{Account: otherSigner.Address}}}
		common.Sponsor = delegate.Address
		sponsorFlags := tx.SpfSponsorFee
		common.SponsorFlags = &sponsorFlags
		fields, err := simulated.Flatten()
		if err != nil {
			t.Fatalf("Flatten: %v", err)
		}
		fields["SponsorSignature"] = map[string]any{"Signers": []any{}}
		encoded, err := json.Marshal(fields)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		parsed, err := tx.ParseJSON(encoded)
		if err != nil {
			t.Fatalf("ParseJSON: %v", err)
		}

		result, err := svc.SimulateTransaction(parsed)
		if err != nil {
			t.Fatalf("SimulateTransaction: %v", err)
		}
		if result.Result != ter.TefBAD_QUORUM || result.Applied {
			t.Fatalf("simulate result = %v, applied = %v; want tefBAD_QUORUM, false", result.Result, result.Applied)
		}
	})
}
