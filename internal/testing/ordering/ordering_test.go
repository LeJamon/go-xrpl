// Package ordering_test contains transaction ordering tests ported from rippled.
// Reference: rippled/src/test/app/Transaction_ordering_test.cpp
package ordering_test

import (
	"testing"

	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/stretchr/testify/require"
)

// TestOrdering_CorrectOrder tests that transactions submitted in sequence order
// are applied correctly.
// Reference: rippled Transaction_ordering_test.cpp testCorrectOrder()
func TestOrdering_CorrectOrder(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	aliceSeq := env.Seq(alice)

	// Submit tx1 at sequence N
	env.Noop(alice)
	env.Close()
	require.Equal(t, aliceSeq+1, env.Seq(alice))

	// Submit tx2 at sequence N+1
	env.Noop(alice)
	env.Close()
	require.Equal(t, aliceSeq+2, env.Seq(alice))
}

// TestOrdering_IncorrectOrder tests that submitting transactions out of order
// still results in correct application after the transaction queue processes them.
// Reference: rippled Transaction_ordering_test.cpp testIncorrectOrder()
//
// This requires the transaction queue to hold terPRE_SEQ transactions and
// replay them when the gap is filled by earlier-sequence transactions.
func TestOrdering_IncorrectOrder(t *testing.T) {
	t.Skip("Requires transaction queue with terPRE_SEQ retry support (not implemented)")
}

// TestOrdering_IncorrectOrderMultipleIntermediaries tests that submitting
// multiple future-sequence transactions results in correct application
// once the first transaction fills the gap.
// Reference: rippled Transaction_ordering_test.cpp testIncorrectOrderMultipleIntermediaries()
func TestOrdering_IncorrectOrderMultipleIntermediaries(t *testing.T) {
	t.Skip("Requires transaction queue with terPRE_SEQ retry support (not implemented)")
}
