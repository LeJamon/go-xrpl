package service_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/feetrack"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// TestGetAutofillFee_NoLoad pins the rippled baseline: with a fresh
// LoadFeeTrack the local factor is LoadBase, so GetAutofillFee returns
// the per-tx-type feeDefault unchanged. Mirrors rippled getCurrentNetworkFee
// (TransactionSign.cpp:849-861) when isLoadedLocal() is false.
func TestGetAutofillFee_NoLoad(t *testing.T) {
	svc, err := service.New(defaultServiceConfig())
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("service.Start: %v", err)
	}

	parsed, err := tx.ParseJSON([]byte(`{"TransactionType":"Payment","Account":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh","Destination":"rN7n7otQDd6FczFgLdSqtcsAUxDkw6fzRH","Amount":"1000000"}`))
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}

	fee, err := svc.GetAutofillFee(parsed, false, 10, 1)
	if err != nil {
		t.Fatalf("GetAutofillFee: %v", err)
	}
	// Standalone genesis config: base fee = 10 drops. Payment carries
	// no per-tx multiplier so feeDefault = base fee.
	if fee != 10 {
		t.Fatalf("no-load autofill fee = %d; want baseFee=10", fee)
	}
}

func TestGetAutofillFee_ConfidentialMPT(t *testing.T) {
	svc, err := service.New(defaultServiceConfig())
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("service.Start: %v", err)
	}

	for _, txType := range []tx.Type{
		tx.TypeConfidentialMPTConvert,
		tx.TypeConfidentialMPTMergeInbox,
		tx.TypeConfidentialMPTConvertBack,
		tx.TypeConfidentialMPTSend,
		tx.TypeConfidentialMPTClawback,
	} {
		t.Run(txType.String(), func(t *testing.T) {
			parsed := tx.NewBaseTx(txType, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh")
			fee, err := svc.GetAutofillFee(parsed, false, 10, 1)
			if err != nil {
				t.Fatalf("GetAutofillFee: %v", err)
			}
			if fee != 100 {
				t.Fatalf("confidential MPT autofill fee = %d; want 100", fee)
			}
		})
	}
}

func TestGetAutofillFee_ConfidentialMPTSigners(t *testing.T) {
	svc, err := service.New(defaultServiceConfig())
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("service.Start: %v", err)
	}

	tests := []struct {
		name    string
		prepare func(*tx.BaseTx)
		want    uint64
	}{
		{
			name: "transaction multisigned",
			prepare: func(parsed *tx.BaseTx) {
				parsed.Signers = make([]tx.SignerWrapper, 3)
			},
			want: 130,
		},
		{
			name: "sponsor single signed",
			prepare: func(parsed *tx.BaseTx) {
				parsed.SponsorSignature = &tx.SponsorSignature{SigningPubKey: "key"}
			},
			want: 100,
		},
		{
			name: "sponsor multisigned",
			prepare: func(parsed *tx.BaseTx) {
				parsed.SponsorSignature = &tx.SponsorSignature{
					Signers: make([]tx.SignerWrapper, 2),
				}
			},
			want: 120,
		},
		{
			name: "transaction and sponsor multisigned",
			prepare: func(parsed *tx.BaseTx) {
				parsed.Signers = make([]tx.SignerWrapper, 3)
				parsed.SponsorSignature = &tx.SponsorSignature{
					Signers: make([]tx.SignerWrapper, 2),
				}
			},
			want: 150,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parsed := tx.NewBaseTx(tx.TypeConfidentialMPTSend, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh")
			test.prepare(parsed)
			fee, err := svc.GetAutofillFee(parsed, false, 10, 1)
			if err != nil {
				t.Fatalf("GetAutofillFee: %v", err)
			}
			if fee != test.want {
				t.Fatalf("confidential MPT autofill fee = %d; want %d", fee, test.want)
			}
		})
	}
}

func TestGetAutofillFee_MalformedTransactionUsesReferenceFee(t *testing.T) {
	svc, err := service.New(defaultServiceConfig())
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("service.Start: %v", err)
	}

	fee, err := svc.GetAutofillFee(nil, false, 10, 1)
	if err != nil {
		t.Fatalf("GetAutofillFee(nil): %v", err)
	}
	if fee != 10 {
		t.Fatalf("malformed transaction autofill fee = %d; want reference fee 10", fee)
	}
}

// TestGetAutofillFee_LoadedLocal verifies the LoadFeeTrack integration:
// raising the local factor inflates the autofilled fee by exactly
// localFee/LoadBase, matching rippled scaleFeeLoad (LoadFeeTrack.cpp:106-110).
func TestGetAutofillFee_LoadedLocal(t *testing.T) {
	svc, err := service.New(defaultServiceConfig())
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("service.Start: %v", err)
	}

	ft := svc.FeeTrack()
	if ft == nil {
		t.Fatal("FeeTrack must be non-nil after Start")
	}
	// Two raises clear the latch and apply one increment: local = 320.
	ft.RaiseLocalFee()
	ft.RaiseLocalFee()
	if got := ft.LocalFee(); got != 320 {
		t.Fatalf("post-raise local fee = %d; want 320", got)
	}

	parsed, err := tx.ParseJSON([]byte(`{"TransactionType":"Payment","Account":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh","Destination":"rN7n7otQDd6FczFgLdSqtcsAUxDkw6fzRH","Amount":"1000000"}`))
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}

	fee, err := svc.GetAutofillFee(parsed, false, 10, 1)
	if err != nil {
		t.Fatalf("GetAutofillFee: %v", err)
	}
	// feeDefault=10, factor=320/256 = 5/4 → expect 12 (mulDiv truncates).
	want := uint64(10) * 320 / uint64(feetrack.LoadBase)
	if fee != want {
		t.Fatalf("loaded autofill fee = %d; want %d", fee, want)
	}
}

// TestGetAutofillFee_Unlimited verifies the isUnlimited(role) carve-out:
// when only the local factor is elevated and stays under 4x remote, an
// unlimited caller pays the remote-rate factor. Mirrors LoadFeeTrack.cpp:97-100.
func TestGetAutofillFee_Unlimited(t *testing.T) {
	svc, err := service.New(defaultServiceConfig())
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("service.Start: %v", err)
	}

	ft := svc.FeeTrack()
	ft.RaiseLocalFee()
	ft.RaiseLocalFee() // local=320, remote=256 → unlimited collapses factor to remote.

	parsed, err := tx.ParseJSON([]byte(`{"TransactionType":"Payment","Account":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh","Destination":"rN7n7otQDd6FczFgLdSqtcsAUxDkw6fzRH","Amount":"1000000"}`))
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}

	fee, err := svc.GetAutofillFee(parsed, true, 10, 1)
	if err != nil {
		t.Fatalf("GetAutofillFee: %v", err)
	}
	if fee != 10 {
		t.Fatalf("unlimited under moderate local load = %d; want baseFee=10", fee)
	}
}

// TestGetAutofillFee_Unlimited_HitsCeiling exercises the issue-acceptance
// criterion that admin/identified callers still hit the ceiling check.
// Drive local fee high enough that even after scaling, the result
// exceeds feeDefault * mult/div, and assert that *svcerr.HighFeeError
// surfaces regardless of the unlimited flag.
func TestGetAutofillFee_Unlimited_HitsCeiling(t *testing.T) {
	svc, err := service.New(defaultServiceConfig())
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("service.Start: %v", err)
	}

	ft := svc.FeeTrack()
	// Drive the local factor far above 4x remote so the unlimited
	// carve-out drops away and scaleFeeLoad applies in full.
	for range 40 {
		ft.RaiseLocalFee()
	}
	if got := ft.LocalFee(); got < 4*ft.RemoteFee() {
		t.Fatalf("setup: local %d not >= 4*remote %d", got, ft.RemoteFee())
	}

	parsed, err := tx.ParseJSON([]byte(`{"TransactionType":"Payment","Account":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh","Destination":"rN7n7otQDd6FczFgLdSqtcsAUxDkw6fzRH","Amount":"1000000"}`))
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}

	if _, err := svc.GetAutofillFee(parsed, true, 10, 1); err == nil {
		t.Fatal("expected HighFeeError once scaled fee exceeds ceiling; got nil")
	}
}
