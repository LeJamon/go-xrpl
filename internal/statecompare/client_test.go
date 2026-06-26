package statecompare

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
)

func TestGetEnvOrDefault(t *testing.T) {
	const key = "STATECOMPARE_TEST_GETENV"
	const def = "fallback"

	t.Run("unset returns default", func(t *testing.T) {
		if got := getEnvOrDefault(key, def); got != def {
			t.Errorf("getEnvOrDefault(unset) = %q, want %q", got, def)
		}
	})

	t.Run("empty returns default", func(t *testing.T) {
		t.Setenv(key, "")
		if got := getEnvOrDefault(key, def); got != def {
			t.Errorf("getEnvOrDefault(empty) = %q, want %q", got, def)
		}
	})

	t.Run("set returns value", func(t *testing.T) {
		t.Setenv(key, "custom")
		if got := getEnvOrDefault(key, def); got != "custom" {
			t.Errorf("getEnvOrDefault(set) = %q, want %q", got, "custom")
		}
	})
}

func TestConfigFromEnvDefaults(t *testing.T) {
	// getEnvOrDefault treats an empty value the same as unset, so clearing each
	// var exercises the default branch while letting t.Setenv restore the
	// caller's environment afterwards.
	for _, k := range []string{
		"POSTGRES_HOST", "POSTGRES_PORT", "POSTGRES_DB",
		"POSTGRES_USER", "POSTGRES_PASSWORD", "POSTGRES_SSLMODE",
	} {
		t.Setenv(k, "")
	}

	want := Config{
		Host:     "localhost",
		Port:     "5432",
		Database: "xrpl_state",
		User:     "postgres",
		Password: "postgres",
		SSLMode:  "disable",
	}
	if got := ConfigFromEnv(); got != want {
		t.Errorf("ConfigFromEnv() = %+v, want %+v", got, want)
	}
}

func TestConfigFromEnvOverrides(t *testing.T) {
	t.Setenv("POSTGRES_HOST", "db.example.com")
	t.Setenv("POSTGRES_PORT", "6543")
	t.Setenv("POSTGRES_DB", "ledgers")
	t.Setenv("POSTGRES_USER", "alice")
	t.Setenv("POSTGRES_PASSWORD", "s3cret")
	t.Setenv("POSTGRES_SSLMODE", "require")

	want := Config{
		Host:     "db.example.com",
		Port:     "6543",
		Database: "ledgers",
		User:     "alice",
		Password: "s3cret",
		SSLMode:  "require",
	}
	if got := ConfigFromEnv(); got != want {
		t.Errorf("ConfigFromEnv() = %+v, want %+v", got, want)
	}
}

func TestValidateRangeFromGreaterThanTo(t *testing.T) {
	// from > to is a degenerate (empty) range: it returns early before any DB
	// access, so a Client with a nil *sql.DB is sufficient and must not panic.
	c := &Client{}
	ok, missing, err := c.ValidateRange(context.Background(), 10, 5)
	if err != nil {
		t.Fatalf("ValidateRange returned error: %v", err)
	}
	if !ok {
		t.Errorf("ValidateRange(10, 5) ok = false, want true for an empty range")
	}
	if missing != 0 {
		t.Errorf("ValidateRange(10, 5) missing = %d, want 0", missing)
	}
}

// testMetaBlob serializes a minimal metadata STObject carrying the given
// sfTransactionIndex, the way the engine writes it at ledger close.
func testMetaBlob(t *testing.T, txIndex uint32) []byte {
	t.Helper()
	hexStr, err := binarycodec.Encode(map[string]any{
		"TransactionResult": "tesSUCCESS",
		"TransactionIndex":  txIndex,
	})
	if err != nil {
		t.Fatalf("encode metadata: %v", err)
	}
	b, err := hex.DecodeString(hexStr)
	if err != nil {
		t.Fatalf("decode hex: %v", err)
	}
	return b
}

func TestMetaTransactionIndex(t *testing.T) {
	for _, want := range []uint32{0, 1, 5, 60, 1000} {
		got, err := metaTransactionIndex(testMetaBlob(t, want))
		if err != nil {
			t.Fatalf("metaTransactionIndex(%d): %v", want, err)
		}
		if got != want {
			t.Errorf("metaTransactionIndex = %d, want %d", got, want)
		}
	}
}

func TestMetaTransactionIndexErrors(t *testing.T) {
	if _, err := metaTransactionIndex(nil); err == nil {
		t.Error("empty metadata: want error, got nil")
	}
	hexStr, err := binarycodec.Encode(map[string]any{"TransactionResult": "tesSUCCESS"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	noIdx, err := hex.DecodeString(hexStr)
	if err != nil {
		t.Fatalf("decode hex: %v", err)
	}
	if _, err := metaTransactionIndex(noIdx); err == nil {
		t.Error("metadata missing TransactionIndex: want error, got nil")
	}
}

// TestOrderByTransactionIndex feeds transactions in transaction-tree (hash)
// order — unrelated to apply order — and asserts they come back in
// sfTransactionIndex order so a single replay pass matches mainnet.
func TestOrderByTransactionIndex(t *testing.T) {
	txs := []Transaction{
		{TxHash: [32]byte{0xAA}, MetaBlob: testMetaBlob(t, 2)},
		{TxHash: [32]byte{0xBB}, MetaBlob: testMetaBlob(t, 0)},
		{TxHash: [32]byte{0xCC}, MetaBlob: testMetaBlob(t, 1)},
	}
	if err := orderByTransactionIndex(txs); err != nil {
		t.Fatalf("orderByTransactionIndex: %v", err)
	}

	wantFirstByte := []byte{0xBB, 0xCC, 0xAA} // indices 0, 1, 2
	for i := range txs {
		if txs[i].TxIndex != i {
			t.Errorf("txs[%d].TxIndex = %d, want %d", i, txs[i].TxIndex, i)
		}
		if txs[i].TxHash[0] != wantFirstByte[i] {
			t.Errorf("txs[%d].TxHash[0] = %#x, want %#x", i, txs[i].TxHash[0], wantFirstByte[i])
		}
	}
}

func TestOrderByTransactionIndexBadMeta(t *testing.T) {
	txs := []Transaction{{TxHash: [32]byte{0x01}, MetaBlob: nil}}
	if err := orderByTransactionIndex(txs); err == nil {
		t.Error("nil metadata: want error, got nil")
	}
}
