package fieldpresence_test

import (
	"encoding/hex"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
	clawbacktx "github.com/LeJamon/go-xrpl/internal/tx/clawback"
	depositpreauthtx "github.com/LeJamon/go-xrpl/internal/tx/depositpreauth"
	escrowtx "github.com/LeJamon/go-xrpl/internal/tx/escrow"
	paychantx "github.com/LeJamon/go-xrpl/internal/tx/paychan"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

const (
	accountZero = "rrrrrrrrrrrrrrrrrrrrrhoLvTp"
	mptID       = "000000000000000000000001000000000000000000000001"
)

func TestTypedAccountFieldPresence(t *testing.T) {
	alice := jtx.NewAccount("presence-alice")
	bob := jtx.NewAccount("presence-bob")
	finishAfter := uint32(1)
	positiveMPT := state.NewMPTAmountWithIssuanceID(100, alice.Address, mptID)
	positiveIOU := txcore.NewIssuedAmountFromFloat64(1, "USD", bob.Address)
	channel := strings.Repeat("01", 32)
	claimBalance := txcore.NewXRPAmount(1)
	validClaimSignature := signClaim(t, alice, channel, uint64(claimBalance.Drops()))

	tests := []struct {
		name      string
		field     string
		values    []string
		wantCodes []ter.Result
		build     func(string) txcore.Transaction
		check     func(txcore.Transaction) error
	}{
		{
			name:      "IOU Clawback Holder",
			field:     "Holder",
			values:    []string{"", "", accountZero, bob.Address},
			wantCodes: []ter.Result{ter.TesSUCCESS, ter.TemMALFORMED, ter.TemMALFORMED, ter.TemMALFORMED},
			build: func(holder string) txcore.Transaction {
				transaction := clawbacktx.NewClawback(alice.Address, positiveIOU)
				transaction.Holder = holder
				return transaction
			},
			check: func(transaction txcore.Transaction) error {
				return transaction.(*clawbacktx.Clawback).PreflightRules(amendment.AllSupportedRules())
			},
		},
		{
			name:      "MPT Clawback Holder",
			field:     "Holder",
			values:    []string{"", "", accountZero, bob.Address},
			wantCodes: []ter.Result{ter.TemMALFORMED, ter.TesSUCCESS, ter.TesSUCCESS, ter.TesSUCCESS},
			build: func(holder string) txcore.Transaction {
				return clawbacktx.NewMPTokenClawback(alice.Address, holder, positiveMPT)
			},
			check: func(transaction txcore.Transaction) error {
				return transaction.(*clawbacktx.Clawback).PreflightRules(amendment.AllSupportedRules())
			},
		},
		{
			name:      "EscrowCreate Destination",
			field:     "Destination",
			values:    []string{"", "", accountZero, bob.Address},
			wantCodes: []ter.Result{ter.TemDST_NEEDED, ter.TesSUCCESS, ter.TesSUCCESS, ter.TesSUCCESS},
			build: func(destination string) txcore.Transaction {
				transaction := escrowtx.NewEscrowCreate(alice.Address, destination, txcore.NewXRPAmount(1))
				transaction.FinishAfter = &finishAfter
				return transaction
			},
			check: func(transaction txcore.Transaction) error { return transaction.Validate() },
		},
		{
			name:      "EscrowFinish Owner",
			field:     "Owner",
			values:    []string{"", "", accountZero, bob.Address},
			wantCodes: []ter.Result{ter.TemMALFORMED, ter.TesSUCCESS, ter.TesSUCCESS, ter.TesSUCCESS},
			build: func(owner string) txcore.Transaction {
				return escrowtx.NewEscrowFinish(alice.Address, owner, 1)
			},
			check: func(transaction txcore.Transaction) error { return transaction.Validate() },
		},
		{
			name:      "EscrowCancel Owner",
			field:     "Owner",
			values:    []string{"", "", accountZero, bob.Address},
			wantCodes: []ter.Result{ter.TemMALFORMED, ter.TesSUCCESS, ter.TesSUCCESS, ter.TesSUCCESS},
			build: func(owner string) txcore.Transaction {
				return escrowtx.NewEscrowCancel(alice.Address, owner, 1)
			},
			check: func(transaction txcore.Transaction) error { return transaction.Validate() },
		},
		{
			name:      "PaymentChannelCreate Destination",
			field:     "Destination",
			values:    []string{"", "", accountZero, bob.Address},
			wantCodes: []ter.Result{ter.TemDST_NEEDED, ter.TesSUCCESS, ter.TesSUCCESS, ter.TesSUCCESS},
			build: func(destination string) txcore.Transaction {
				return paychantx.NewPaymentChannelCreate(
					alice.Address, destination, txcore.NewXRPAmount(1), 3600, alice.PublicKeyHex())
			},
			check: func(transaction txcore.Transaction) error { return transaction.Validate() },
		},
		{
			name:      "DepositPreauth Authorize",
			field:     "Authorize",
			values:    []string{"", "", accountZero, bob.Address},
			wantCodes: []ter.Result{ter.TemMALFORMED, ter.TemINVALID_ACCOUNT_ID, ter.TemINVALID_ACCOUNT_ID, ter.TesSUCCESS},
			build: func(authorize string) txcore.Transaction {
				transaction := depositpreauthtx.NewDepositPreauth(alice.Address)
				transaction.Authorize = authorize
				return transaction
			},
			check: func(transaction txcore.Transaction) error { return transaction.Validate() },
		},
		{
			name:      "DepositPreauth Unauthorize",
			field:     "Unauthorize",
			values:    []string{"", "", accountZero, bob.Address},
			wantCodes: []ter.Result{ter.TemMALFORMED, ter.TemINVALID_ACCOUNT_ID, ter.TemINVALID_ACCOUNT_ID, ter.TesSUCCESS},
			build: func(unauthorize string) txcore.Transaction {
				transaction := depositpreauthtx.NewDepositPreauth(alice.Address)
				transaction.Unauthorize = unauthorize
				return transaction
			},
			check: func(transaction txcore.Transaction) error { return transaction.Validate() },
		},
		{
			name:      "PaymentChannelClaim Signature",
			field:     "Signature",
			values:    []string{"", "", "00", validClaimSignature},
			wantCodes: []ter.Result{ter.TesSUCCESS, ter.TemBAD_SIGNATURE, ter.TemBAD_SIGNATURE, ter.TesSUCCESS},
			build: func(signature string) txcore.Transaction {
				transaction := paychantx.NewPaymentChannelClaim(alice.Address, channel)
				transaction.Balance = &claimBalance
				transaction.PublicKey = alice.PublicKeyHex()
				transaction.Signature = signature
				return transaction
			},
			check: func(transaction txcore.Transaction) error { return transaction.Validate() },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for i, value := range test.values {
				name := []string{"absent", "present empty", "account zero", "valid"}[i]
				t.Run(name, func(t *testing.T) {
					transaction := test.build(value)
					if i > 0 {
						transaction.GetCommon().SetPresentFields(map[string]bool{test.field: true})
					}
					require.Equal(t, test.wantCodes[i], errorCode(t, test.check(transaction)))
				})
			}
		})
	}
}

func TestPresentEmptyWireRoundTrips(t *testing.T) {
	all.RegisterAll()
	alice := jtx.NewAccount("wire-presence-alice")
	finishAfter := uint32(1)
	balance := txcore.NewXRPAmount(1)

	tests := []struct {
		name  string
		field string
		build func() txcore.Transaction
		check func(txcore.Transaction) ter.Result
	}{
		{
			name:  "Clawback Holder",
			field: "Holder",
			build: func() txcore.Transaction {
				return clawbacktx.NewClawback(alice.Address, txcore.NewIssuedAmountFromFloat64(1, "USD", accountZero))
			},
			check: func(transaction txcore.Transaction) ter.Result {
				return errorCode(t, transaction.(*clawbacktx.Clawback).PreflightRules(amendment.AllSupportedRules()))
			},
		},
		{
			name:  "MPT Clawback Holder",
			field: "Holder",
			build: func() txcore.Transaction {
				return clawbacktx.NewMPTokenClawback(
					alice.Address, "", state.NewMPTAmountWithIssuanceID(1, alice.Address, mptID))
			},
			check: func(transaction txcore.Transaction) ter.Result {
				return errorCode(t, transaction.(*clawbacktx.Clawback).PreflightRules(amendment.AllSupportedRules()))
			},
		},
		{
			name:  "EscrowCreate Destination",
			field: "Destination",
			build: func() txcore.Transaction {
				transaction := escrowtx.NewEscrowCreate(alice.Address, "", txcore.NewXRPAmount(1))
				transaction.FinishAfter = &finishAfter
				return transaction
			},
			check: func(transaction txcore.Transaction) ter.Result {
				return errorCode(t, transaction.Validate())
			},
		},
		{
			name:  "EscrowFinish Owner",
			field: "Owner",
			build: func() txcore.Transaction { return escrowtx.NewEscrowFinish(alice.Address, "", 1) },
			check: func(transaction txcore.Transaction) ter.Result {
				return errorCode(t, transaction.Validate())
			},
		},
		{
			name:  "EscrowCancel Owner",
			field: "Owner",
			build: func() txcore.Transaction { return escrowtx.NewEscrowCancel(alice.Address, "", 1) },
			check: func(transaction txcore.Transaction) ter.Result {
				return errorCode(t, transaction.Validate())
			},
		},
		{
			name:  "PaymentChannelCreate Destination",
			field: "Destination",
			build: func() txcore.Transaction {
				return paychantx.NewPaymentChannelCreate(
					alice.Address, "", txcore.NewXRPAmount(1), 3600, alice.PublicKeyHex())
			},
			check: func(transaction txcore.Transaction) ter.Result {
				return errorCode(t, transaction.Validate())
			},
		},
		{
			name:  "PaymentChannelClaim Signature",
			field: "Signature",
			build: func() txcore.Transaction {
				transaction := paychantx.NewPaymentChannelClaim(alice.Address, strings.Repeat("01", 32))
				transaction.Balance = &balance
				transaction.PublicKey = alice.PublicKeyHex()
				return transaction
			},
			check: func(transaction txcore.Transaction) ter.Result {
				return errorCode(t, transaction.Validate())
			},
		},
		{
			name:  "DepositPreauth Authorize",
			field: "Authorize",
			build: func() txcore.Transaction { return depositpreauthtx.NewDepositPreauth(alice.Address) },
			check: func(transaction txcore.Transaction) ter.Result {
				return errorCode(t, transaction.Validate())
			},
		},
		{
			name:  "DepositPreauth Unauthorize",
			field: "Unauthorize",
			build: func() txcore.Transaction { return depositpreauthtx.NewDepositPreauth(alice.Address) },
			check: func(transaction txcore.Transaction) ter.Result {
				return errorCode(t, transaction.Validate())
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transaction := test.build()
			transaction.GetCommon().SetPresentFields(map[string]bool{test.field: true})
			prepareWire(transaction, alice.PublicKeyHex())

			flat, err := transaction.Flatten()
			require.NoError(t, err)
			require.Contains(t, flat, test.field)
			require.Equal(t, "", flat[test.field])
			blob, err := binarycodec.EncodeBytes(flat)
			require.NoError(t, err)

			parsed, err := txcore.ParseFromBinary(blob)
			require.NoError(t, err)
			require.True(t, parsed.GetCommon().HasField(test.field))
			parsedFlat, err := parsed.Flatten()
			require.NoError(t, err)
			require.Equal(t, "", parsedFlat[test.field])
			reencoded, err := binarycodec.EncodeBytes(parsedFlat)
			require.NoError(t, err)
			require.Equal(t, blob, reencoded)

			want := ter.TesSUCCESS
			if test.name == "Clawback Holder" {
				want = ter.TemMALFORMED
			} else if test.field == "Signature" {
				want = ter.TemBAD_SIGNATURE
			} else if test.field == "Authorize" || test.field == "Unauthorize" {
				want = ter.TemINVALID_ACCOUNT_ID
			}
			require.Equal(t, want, test.check(parsed))
		})
	}
}

func TestPresentEmptyEngineResults(t *testing.T) {
	tests := []struct {
		name           string
		disableFeature string
		wantCode       string
		claimed        bool
		build          func(*jtx.Account) txcore.Transaction
	}{
		{
			name:     "IOU Clawback Holder",
			wantCode: "temMALFORMED",
			build: func(alice *jtx.Account) txcore.Transaction {
				transaction := clawbacktx.NewClawback(
					alice.Address, txcore.NewIssuedAmountFromFloat64(1, "USD", accountZero))
				transaction.SetPresentFields(map[string]bool{"Holder": true})
				return transaction
			},
		},
		{
			name:     "MPT Clawback Holder",
			wantCode: "terNO_ACCOUNT",
			build: func(alice *jtx.Account) txcore.Transaction {
				transaction := clawbacktx.NewMPTokenClawback(
					alice.Address, "", state.NewMPTAmountWithIssuanceID(1, alice.Address, mptID))
				transaction.SetPresentFields(map[string]bool{"Holder": true})
				return transaction
			},
		},
		{
			name:     "EscrowCreate Destination",
			wantCode: "tecNO_DST",
			claimed:  true,
			build: func(alice *jtx.Account) txcore.Transaction {
				finish := uint32(1)
				transaction := escrowtx.NewEscrowCreate(alice.Address, "", txcore.NewXRPAmount(1))
				transaction.FinishAfter = &finish
				transaction.SetPresentFields(map[string]bool{"Destination": true})
				return transaction
			},
		},
		{
			name:     "EscrowFinish Owner TokenEscrow",
			wantCode: "tecNO_TARGET",
			claimed:  true,
			build: func(alice *jtx.Account) txcore.Transaction {
				transaction := escrowtx.NewEscrowFinish(alice.Address, "", 1)
				transaction.SetPresentFields(map[string]bool{"Owner": true})
				return transaction
			},
		},
		{
			name:           "EscrowFinish Owner legacy",
			disableFeature: "TokenEscrow",
			wantCode:       "tecNO_TARGET",
			claimed:        true,
			build: func(alice *jtx.Account) txcore.Transaction {
				transaction := escrowtx.NewEscrowFinish(alice.Address, "", 1)
				transaction.SetPresentFields(map[string]bool{"Owner": true})
				return transaction
			},
		},
		{
			name:     "EscrowCancel Owner TokenEscrow",
			wantCode: "tecNO_TARGET",
			claimed:  true,
			build: func(alice *jtx.Account) txcore.Transaction {
				transaction := escrowtx.NewEscrowCancel(alice.Address, "", 1)
				transaction.SetPresentFields(map[string]bool{"Owner": true})
				return transaction
			},
		},
		{
			name:           "EscrowCancel Owner legacy",
			disableFeature: "TokenEscrow",
			wantCode:       "tecNO_TARGET",
			claimed:        true,
			build: func(alice *jtx.Account) txcore.Transaction {
				transaction := escrowtx.NewEscrowCancel(alice.Address, "", 1)
				transaction.SetPresentFields(map[string]bool{"Owner": true})
				return transaction
			},
		},
		{
			name:     "PaymentChannelCreate Destination",
			wantCode: "tecNO_DST",
			claimed:  true,
			build: func(alice *jtx.Account) txcore.Transaction {
				transaction := paychantx.NewPaymentChannelCreate(
					alice.Address, "", txcore.NewXRPAmount(1), 3600, alice.PublicKeyHex())
				transaction.SetPresentFields(map[string]bool{"Destination": true})
				return transaction
			},
		},
		{
			name:     "PaymentChannelClaim Signature",
			wantCode: "temBAD_SIGNATURE",
			build: func(alice *jtx.Account) txcore.Transaction {
				balance := txcore.NewXRPAmount(1)
				transaction := paychantx.NewPaymentChannelClaim(alice.Address, strings.Repeat("01", 32))
				transaction.Balance = &balance
				transaction.PublicKey = alice.PublicKeyHex()
				transaction.SetPresentFields(map[string]bool{"Signature": true})
				return transaction
			},
		},
		{
			name:     "DepositPreauth Authorize",
			wantCode: "temINVALID_ACCOUNT_ID",
			build: func(alice *jtx.Account) txcore.Transaction {
				transaction := depositpreauthtx.NewDepositPreauth(alice.Address)
				transaction.SetPresentFields(map[string]bool{"Authorize": true})
				return transaction
			},
		},
		{
			name:     "DepositPreauth Unauthorize",
			wantCode: "temINVALID_ACCOUNT_ID",
			build: func(alice *jtx.Account) txcore.Transaction {
				transaction := depositpreauthtx.NewDepositPreauth(alice.Address)
				transaction.SetPresentFields(map[string]bool{"Unauthorize": true})
				return transaction
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			if test.disableFeature != "" {
				env.DisableFeature(test.disableFeature)
				env.Close()
			}
			alice := jtx.NewAccount("engine-presence-alice")
			env.Fund(alice)
			beforeBalance, beforeSequence := env.Balance(alice), env.Seq(alice)

			result := env.Submit(test.build(alice))
			if test.claimed {
				jtx.RequireTxClaimed(t, result, test.wantCode)
				require.Equal(t, beforeSequence+1, env.Seq(alice))
				require.Equal(t, beforeBalance-env.BaseFee(), env.Balance(alice))
			} else {
				jtx.RequireTxFail(t, result, test.wantCode)
				require.Equal(t, beforeSequence, env.Seq(alice))
				require.Equal(t, beforeBalance, env.Balance(alice))
			}
		})
	}
}

func TestPaymentChannelCreateSponsorOrdering(t *testing.T) {
	for _, test := range []struct {
		name           string
		disableSponsor bool
		wantCode       string
	}{
		{name: "Sponsor enabled", wantCode: "tecNO_DST"},
		{name: "Sponsor disabled", disableSponsor: true, wantCode: "tecINSUFFICIENT_RESERVE"},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			if test.disableSponsor {
				env.DisableFeature("Sponsor")
				env.Close()
			}
			alice := jtx.NewAccount("sponsor-order-alice")
			env.FundAmount(alice, env.ReserveBase())
			beforeBalance, beforeSequence := env.Balance(alice), env.Seq(alice)

			transaction := paychantx.NewPaymentChannelCreate(
				alice.Address, "", txcore.NewXRPAmount(1), 3600, alice.PublicKeyHex())
			transaction.SetPresentFields(map[string]bool{"Destination": true})
			jtx.RequireTxClaimed(t, env.Submit(transaction), test.wantCode)
			require.Equal(t, beforeSequence+1, env.Seq(alice))
			require.Equal(t, beforeBalance-env.BaseFee(), env.Balance(alice))
		})
	}
}

func signClaim(t *testing.T, account *jtx.Account, channel string, amount uint64) string {
	t.Helper()
	messageHex, err := binarycodec.EncodeForSigningClaim(map[string]any{
		"Channel": channel,
		"Amount":  strconv.FormatUint(amount, 10),
	})
	require.NoError(t, err)
	message, err := hex.DecodeString(messageHex)
	require.NoError(t, err)
	signature, err := (secp256k1.Algorithm{}).Sign(string(message), "00"+hex.EncodeToString(account.PrivateKey))
	require.NoError(t, err)
	return signature
}

func prepareWire(transaction txcore.Transaction, publicKey string) {
	common := transaction.GetCommon()
	sequence := uint32(1)
	common.Sequence = &sequence
	common.Fee = "10"
	common.SigningPubKey = publicKey
}

func errorCode(t *testing.T, err error) ter.Result {
	t.Helper()
	if err == nil {
		return ter.TesSUCCESS
	}
	result, ok := ter.AsResultError(err)
	require.True(t, ok, "unexpected error type: %v", err)
	return result.Code
}
