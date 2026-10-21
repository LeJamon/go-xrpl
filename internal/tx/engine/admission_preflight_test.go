package engine

import (
	"bytes"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	binarytypes "github.com/LeJamon/go-xrpl/codec/binarycodec/types"
	"github.com/LeJamon/go-xrpl/crypto/ed25519"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	checktx "github.com/LeJamon/go-xrpl/internal/tx/check"
	lendingtx "github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	txsign "github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/protocol"
)

const (
	admissionPreflightAccount      = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	admissionPreflightDestination  = "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"
	admissionPreflightCheckID      = "ABABABABABABABABABABABABABABABABABABABABABABABABABABABABABABABAB"
	admissionPreflightLoanBrokerID = "CDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCDCD"
	admissionPreflightPublicKey    = "0330E7FC9D56BB25D6893BA3F317AE5BCF33B3291BD63DB32654A313222F7FD020"
)

func signedAdmissionTransaction(t *testing.T, txType string, sourceAccount string, extra map[string]any) txcore.Transaction {
	t.Helper()

	privateKey, publicKey, err := ed25519.Algorithm{}.DeriveKeypair(bytes.Repeat([]byte{0x42}, 16), false)
	if err != nil {
		t.Fatalf("derive keypair: %v", err)
	}
	var transaction txcore.Transaction
	switch txType {
	case "Payment":
		amount, ok := extra["Amount"].(binarytypes.RawXRPAmount)
		if !ok {
			t.Fatalf("payment fixture amount has type %T, want RawXRPAmount", extra["Amount"])
		}
		transaction = payment.NewPayment(sourceAccount, admissionPreflightDestination, txcore.NewXRPAmount(int64(amount.Drops)))
		transaction.(*payment.Payment).Amount = txcore.NewXRPAmount(int64(amount.Drops))
	case "CheckCreate":
		amount := extra["Amount"].(binarytypes.RawXRPAmount)
		transaction = checktx.NewCheckCreate(sourceAccount, admissionPreflightDestination, txcore.NewXRPAmount(int64(amount.Drops)))
	case "CheckCash":
		amount := extra["Amount"].(binarytypes.RawXRPAmount)
		cash := checktx.NewCheckCash(sourceAccount, admissionPreflightCheckID)
		cash.SetExactAmount(txcore.NewXRPAmount(int64(amount.Drops)))
		transaction = cash
	case "LoanBrokerCoverDeposit":
		amount := extra["Amount"].(binarytypes.RawXRPAmount)
		transaction = lendingtx.NewLoanBrokerCoverDeposit(sourceAccount, admissionPreflightLoanBrokerID, txcore.NewXRPAmount(int64(amount.Drops)))
	case "LoanBrokerCoverWithdraw":
		amount := extra["Amount"].(binarytypes.RawXRPAmount)
		transaction = lendingtx.NewLoanBrokerCoverWithdraw(sourceAccount, admissionPreflightLoanBrokerID, txcore.NewXRPAmount(int64(amount.Drops)))
	case "AccountSet":
		transaction = txcore.NewBaseTx(txcore.TypeAccountSet, sourceAccount)
	default:
		t.Fatalf("unsupported fixture transaction type %q", txType)
	}
	common := transaction.GetCommon()
	common.Fee = "10"
	sequence := uint32(1)
	common.Sequence = &sequence
	common.SigningPubKey = publicKey
	if txType == "Payment" {
		if destination, ok := extra["Destination"].(string); ok {
			transaction.(*payment.Payment).Destination = destination
		}
	}
	signature, err := txsign.SignTransaction(transaction, privateKey)
	if err != nil {
		t.Fatalf("sign %s: %v", txType, err)
	}
	common.TxnSignature = signature
	wire, err := txcore.SerializeTransaction(transaction)
	if err != nil {
		t.Fatalf("encode signed %s: %v", txType, err)
	}
	if err := txcore.BindRawBytes(transaction, wire); err != nil {
		t.Fatalf("bind signed %s: %v", txType, err)
	}
	if reason := txsign.CheckSTTxSignature(transaction, allRules(), true); reason != "" {
		t.Fatalf("signed %s failed cryptographic admission check: %s", txType, reason)
	}
	return transaction
}

func TestPreflightAdmissionOverCapNativeAmountReturnsTER(t *testing.T) {
	transaction := signedAdmissionTransaction(t, "Payment", admissionPreflightAccount, map[string]any{
		"Destination": admissionPreflightDestination,
		"Amount": binarytypes.RawXRPAmount{
			Drops: uint64(binarytypes.MaxDrops) + 1,
		},
	})

	if got := preflightEngine(allRules()).preflight(transaction); got != ter.TemBAD_AMOUNT {
		t.Fatalf("preflight over-cap native amount = %v, want temBAD_AMOUNT", got)
	}
}

func TestPreflightAdmissionOverCapTypeAmountsReturnTERBeforeAndAfterCleanup320(t *testing.T) {
	for _, txType := range []string{
		"CheckCreate",
		"CheckCash",
		"LoanBrokerCoverDeposit",
		"LoanBrokerCoverWithdraw",
	} {
		t.Run(txType, func(t *testing.T) {
			for _, tc := range []struct {
				name  string
				rules *amendment.Rules
			}{
				{name: "cleanup320 enabled", rules: allRules()},
				{name: "cleanup320 disabled", rules: rulesWithout("fixCleanup3_2_0")},
			} {
				t.Run(tc.name, func(t *testing.T) {
					transaction := signedAdmissionTransaction(t, txType, admissionPreflightAccount, map[string]any{
						"Amount": binarytypes.RawXRPAmount{Drops: uint64(binarytypes.MaxDrops) + 1},
					})
					if got := preflightEngine(tc.rules).preflight(transaction); got != ter.TemBAD_AMOUNT {
						t.Fatalf("preflight %s amount with %s = %v, want temBAD_AMOUNT", txType, tc.name, got)
					}
				})
			}
		})
	}
}

func TestPreflightAdmissionZeroSourceAccountReturnsTER(t *testing.T) {
	transaction := signedAdmissionTransaction(t, "AccountSet", protocol.ZeroAccount, nil)

	if got := preflightEngine(allRules()).preflight(transaction); got != ter.TemBAD_SRC_ACCOUNT {
		t.Fatalf("preflight zero source account = %v, want temBAD_SRC_ACCOUNT", got)
	}
}

func TestPreflightAdmissionOverCapFeeReturnsBadFee(t *testing.T) {
	allFields := map[string]any{
		"TransactionType": "AccountSet",
		"Account":         admissionPreflightAccount,
		"Fee": binarytypes.RawXRPAmount{
			Drops: uint64(binarytypes.MaxDrops) + 1,
		},
		"Sequence":      uint32(1),
		"SigningPubKey": admissionPreflightPublicKey,
	}
	transaction, err := txcore.ParseFromDecodedFields(allFields)
	if err != nil {
		t.Fatalf("parse over-cap fee: %v", err)
	}
	if transaction.GetCommon().Fee != "100000000000000001" {
		t.Fatalf("parsed over-cap fee = %q, want preserved decimal value", transaction.GetCommon().Fee)
	}
	if got := preflightEngine(allRules()).preflight(transaction); got != ter.TemBAD_FEE {
		t.Fatalf("preflight over-cap fee = %v, want temBAD_FEE", got)
	}
}
