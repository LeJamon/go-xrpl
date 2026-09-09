package mptutil

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestCleanupIOUPseudoAuthorization(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		for _, marker := range []string{"ordinary", "AMMID", "VaultID", "LoanBrokerID"} {
			for _, hasLine := range []bool{false, true} {
				view := newMPTTestView()
				builder := amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported)
				if cleanup {
					builder.Enable(amendment.FeatureFixCleanup3_4_0)
				}
				view.rules = builder.Build()
				issuer, holder := [20]byte{1}, [20]byte{2}
				putTestAccount(t, view, issuer, state.LsfRequireAuth, [32]byte{})
				issuerAddress := state.EncodeAccountIDSafe(issuer)
				holderAddress := state.EncodeAccountIDSafe(holder)
				root := &state.AccountRoot{Account: holderAddress}
				switch marker {
				case "AMMID":
					root.AMMID = [32]byte{1}
				case "VaultID":
					root.VaultID = [32]byte{1}
				case "LoanBrokerID":
					root.LoanBrokerID = [32]byte{1}
				}
				raw, err := state.SerializeAccountRoot(root)
				if err != nil {
					t.Fatal(err)
				}
				view.data[keylet.Account(holder).Key] = raw
				if hasLine {
					line, err := state.SerializeRippleState(&state.RippleState{
						Balance:   state.NewIssuedAmountFromValue(0, 0, "USD", state.AccountOneAddress),
						LowLimit:  state.NewIssuedAmountFromValue(0, 0, "USD", issuerAddress),
						HighLimit: state.NewIssuedAmountFromValue(1000, 0, "USD", holderAddress),
					})
					if err != nil {
						t.Fatal(err)
					}
					view.data[keylet.Line(issuer, holder, "USD").Key] = line
				}
				want := ter.TecNO_AUTH
				if !hasLine {
					want = ter.TecNO_LINE
				} else if cleanup && marker != "ordinary" {
					want = ter.TesSUCCESS
				}
				for _, auth := range []AuthType{LegacyAuth, WeakAuth, StrongAuth} {
					got := RequireAssetAuthAt(view, tx.Asset{Currency: "USD", Issuer: issuerAddress}, holder, auth, 0)
					if got != want {
						t.Fatalf("cleanup=%v marker=%s line=%v auth=%v: got %v want %v", cleanup, marker, hasLine, auth, got, want)
					}
				}
			}
		}
	}
}
