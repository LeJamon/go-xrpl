package engine

import (
	"testing"

	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func TestPreflightMultiSignStructureUsesDelegateIdentity(t *testing.T) {
	const (
		account     = "r4aKxtVB6Va8BM4s9LKzEXAuGnxuDh5EME"
		delegate    = "rBKjMimqYjHX6BUSMNA4CScwRRTXMgRz6W"
		destination = "r3gYkHRkq7umjZrVmqFb51BYVTGYtFEosH"
	)

	for _, test := range []struct {
		name     string
		delegate string
		signer   string
		want     ter.Result
	}{
		{name: "delegated account signer", delegate: delegate, signer: account, want: ter.TesSUCCESS},
		{name: "delegated self signer", delegate: delegate, signer: delegate, want: ter.TemINVALID},
		{name: "ordinary self signer", signer: account, want: ter.TemINVALID},
	} {
		t.Run(test.name, func(t *testing.T) {
			transaction := payment.NewPayment(account, destination, txcore.NewXRPAmount(100_000))
			transaction.Fee = "30"
			transaction.SetSequence(1)
			transaction.SigningPubKey = ""
			transaction.Delegate = test.delegate
			transaction.Signers = []txcore.SignerWrapper{{
				Signer: txcore.Signer{Account: test.signer},
			}}

			if got := preflightEngine(allRules()).preflight(transaction); got != test.want {
				t.Fatalf("preflight = %v, want %v", got, test.want)
			}
		})
	}
}
