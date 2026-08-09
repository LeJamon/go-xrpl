package did_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/keylet"
)

func getDIDEntry(t *testing.T, env *jtx.TestEnv, account *jtx.Account) *state.DIDData {
	t.Helper()
	key := keylet.DID(account.ID)
	data, err := env.LedgerEntry(key)
	require.NoError(t, err)
	if data == nil {
		return nil
	}
	entry, err := state.ParseDID(data)
	require.NoError(t, err)
	return entry
}

func requireDIDAbsent(t *testing.T, env *jtx.TestEnv, account *jtx.Account) {
	t.Helper()
	data, err := env.LedgerEntry(keylet.DID(account.ID))
	require.NoError(t, err)
	require.Nil(t, data)
}

func checkVL(t *testing.T, fieldName, hexValue, expected string) {
	t.Helper()
	decoded, err := hex.DecodeString(strings.ToLower(hexValue))
	if err != nil {
		t.Fatalf("Failed to decode %s hex value %q: %v", fieldName, hexValue, err)
	}
	if string(decoded) != expected {
		t.Errorf("%s mismatch: got %q, want %q", fieldName, string(decoded), expected)
	}
}

func requireFieldPresent(t *testing.T, fieldName, value string) {
	t.Helper()
	if value == "" {
		t.Errorf("Expected %s to be present, but it is absent", fieldName)
	}
}

func requireFieldAbsent(t *testing.T, fieldName, value string) {
	t.Helper()
	if value != "" {
		t.Errorf("Expected %s to be absent, but got %q", fieldName, value)
	}
}

func setupEnv(t *testing.T, fixEmptyDID bool) *jtx.TestEnv {
	t.Helper()
	env := jtx.NewTestEnv(t)
	if !fixEmptyDID {
		env.DisableFeature("fixEmptyDID")
	}
	return env
}

func runWithFeatureSets(t *testing.T, testFn func(t *testing.T, fixEmptyDID bool)) {
	t.Run("AllFeatures", func(t *testing.T) {
		testFn(t, true)
	})
	t.Run("WithoutFixEmptyDID", func(t *testing.T) {
		testFn(t, false)
	})
}
