package batch

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	batchtx "github.com/LeJamon/go-xrpl/internal/tx/batch"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

var innerWrappers = []string{
	"RawTransaction", "CreatedNode", "ModifiedNode", "DeletedNode", "TemplateEntry",
	"EmitDetails", "Memo", "FinalFields", "NewFields", "PreviousFields", "TransactionMetaData",
}

func TestBatchInnerWrappers(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, wrapper := range innerWrappers {
			t.Run(fmt.Sprintf("fix=%t/%s", enabled, wrapper), func(t *testing.T) {
				env := newBatchEnv(t)
				env.EnableFeature("BatchV1_1")
				if enabled {
					env.EnableFeature("fixBatchV1_2")
				} else {
					env.DisableFeature("fixBatchV1_2")
				}
				alice, bob := jtx.NewAccount("alice"), jtx.NewAccount("bob")
				env.Fund(alice, bob)
				env.Close()
				require.True(t, env.Rules().Enabled(amendment.FeatureBatchV1_1))
				require.Equal(t, enabled, env.Rules().Enabled(amendment.FeatureFixBatchV1_2))
				seq, fee := env.Seq(alice), CalcBatchFeeFromEnv(env, 0, 2)
				beforeAlice, beforeBob := env.Balance(alice), env.Balance(bob)
				batch := NewBatchBuilder(alice, seq, fee, batchtx.BatchFlagAllOrNothing).
					AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
					AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+2)).MustBuild()
				for i := range batch.RawTransactions {
					batch.RawTransactions[i].Wrapper = wrapper
				}
				env.VerifySignatures = true
				env.SignWith(batch, alice)
				blob, err := tx.SerializeTransaction(batch)
				require.NoError(t, err)
				parsed, err := tx.ParseFromBinary(blob)
				require.NoError(t, err)
				beforeState, err := env.Ledger().StateMapHash()
				require.NoError(t, err)
				result := env.SubmitWithOptions(parsed, jtx.SubmitOptions{SkipFee: true, SkipSequence: true, SkipNetworkID: true, SkipSignature: true})
				if enabled && wrapper != "RawTransaction" {
					jtx.RequireTxFail(t, result, "temMALFORMED")
					require.False(t, result.Applied)
					afterState, err := env.Ledger().StateMapHash()
					require.NoError(t, err)
					require.Equal(t, beforeState, afterState)
					jtx.RequireBalance(t, env, alice, beforeAlice)
					jtx.RequireBalance(t, env, bob, beforeBob)
					jtx.RequireSequence(t, env, alice, seq)
					return
				}
				jtx.RequireTxSuccess(t, result)
				require.True(t, result.Applied)
				env.Close()
				jtx.RequireBalance(t, env, alice, beforeAlice-uint64(jtx.XRP(2))-fee)
				jtx.RequireBalance(t, env, bob, beforeBob+uint64(jtx.XRP(2)))
				jtx.RequireSequence(t, env, alice, seq+3)
				requireBatchLedgerData(t, env, parsed.(*batchtx.Batch), result, ter.TesSUCCESS, ter.TesSUCCESS)
			})
		}
	}
}

func TestBatchWrapperWireIdentity(t *testing.T) {
	alice, bob := jtx.NewAccount("alice"), jtx.NewAccount("bob")
	batch := NewBatchBuilder(alice, 1, 40, batchtx.BatchFlagAllOrNothing).
		AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, 2)).
		AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, 3)).MustBuild()
	canonicalHash, err := tx.ComputeTransactionHash(batch)
	require.NoError(t, err)
	canonicalFields, err := batch.Flatten()
	require.NoError(t, err)
	canonicalSigning, err := binarycodec.EncodeForSigning(canonicalFields)
	require.NoError(t, err)
	batchSigning, err := batch.BatchSigningMessage()
	require.NoError(t, err)

	for _, wrapper := range innerWrappers {
		t.Run(wrapper, func(t *testing.T) {
			batch.RawTransactions[0].Wrapper = wrapper
			fields, err := batch.Flatten()
			require.NoError(t, err)
			blob, err := binarycodec.EncodeBytes(fields)
			require.NoError(t, err)
			jsonBytes, err := json.Marshal(fields)
			require.NoError(t, err)
			fromJSON, err := tx.ParseJSON(jsonBytes)
			require.NoError(t, err)
			fromBinary, err := tx.ParseFromBinary(blob)
			require.NoError(t, err)
			for _, parsed := range []tx.Transaction{fromJSON, fromBinary} {
				parsedBatch := parsed.(*batchtx.Batch)
				require.Equal(t, wrapper, parsedBatch.RawTransactions[0].Wrapper)
				flat, err := parsed.Flatten()
				require.NoError(t, err)
				encoded, err := binarycodec.EncodeBytes(flat)
				require.NoError(t, err)
				require.Equal(t, blob, encoded)
				serialized, err := tx.SerializeTransaction(parsed)
				require.NoError(t, err)
				require.Equal(t, blob, serialized)
				hash, err := tx.ComputeTransactionHash(parsed)
				require.NoError(t, err)
				currentHash, err := tx.ComputeCurrentTransactionHash(parsed)
				require.NoError(t, err)
				require.Equal(t, hash, currentHash)
				signing, err := binarycodec.EncodeForSigning(flat)
				require.NoError(t, err)
				if wrapper == "RawTransaction" {
					require.Equal(t, canonicalHash, hash)
					require.Equal(t, canonicalSigning, signing)
				} else {
					require.NotEqual(t, canonicalHash, hash)
					require.NotEqual(t, canonicalSigning, signing)
				}
				innerSigning, err := parsedBatch.BatchSigningMessage()
				require.NoError(t, err)
				require.Equal(t, batchSigning, innerSigning)
				jsonWrapper, err := json.Marshal(parsedBatch.RawTransactions[0])
				require.NoError(t, err)
				var decoded map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(jsonWrapper, &decoded))
				require.Contains(t, decoded, wrapper)
				require.Len(t, decoded, 1)
			}
		})
	}
}

func TestBatchWrapperChangeInvalidatesOuterSignature(t *testing.T) {
	env := newBatchEnv(t)
	env.DisableFeature("fixBatchV1_2")
	alice, bob := jtx.NewAccount("alice"), jtx.NewAccount("bob")
	env.Fund(alice, bob)
	env.Close()
	require.False(t, env.Rules().Enabled(amendment.FeatureFixBatchV1_2))
	seq := env.Seq(alice)
	batch := NewBatchBuilder(alice, seq, CalcBatchFeeFromEnv(env, 0, 2), batchtx.BatchFlagAllOrNothing).
		AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+1)).
		AddInnerTx(MakeInnerPaymentXRP(alice, bob, 1, seq+2)).MustBuild()
	env.VerifySignatures = true
	env.SignWith(batch, alice)
	batch.RawTransactions[0].Wrapper = "CreatedNode"
	beforeAlice, beforeBob := env.Balance(alice), env.Balance(bob)
	result := env.SubmitWithOptions(batch, jtx.SubmitOptions{SkipSignature: true})
	jtx.RequireTxFail(t, result, "temINVALID")
	jtx.RequireBalance(t, env, alice, beforeAlice)
	jtx.RequireBalance(t, env, bob, beforeBob)
	jtx.RequireSequence(t, env, alice, seq)
}
