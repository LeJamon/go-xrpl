package keylet

import (
	"encoding/binary"
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

func be16(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }
func be32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }

// TestLoanBrokerKeylet pins the LoanBroker keylet derivation:
// sha512Half(be16('l') || owner || be32(seq)) with type ltLOAN_BROKER (0x88).
// Reference: rippled Indexes.cpp loanbroker(AccountID, uint32).
func TestLoanBrokerKeylet(t *testing.T) {
	var owner [20]byte
	for i := range owner {
		owner[i] = byte(i)
	}
	const seq = 5

	k := LoanBroker(owner, seq)
	if k.Type != entry.TypeLoanBroker {
		t.Errorf("Type = %#x, want %#x", uint16(k.Type), uint16(entry.TypeLoanBroker))
	}

	// Independent re-derivation via the raw namespace-byte formula.
	want := sha512half.Sum(be16('l'), owner[:], be32(seq))
	if k.Key != want {
		t.Errorf("key mismatch\n got  %x\n want %x", k.Key, want)
	}

	// Golden vector: guards against a Sha512Half or input-ordering regression.
	const golden = "c34d756fa5e3ff89b7034121dff7e6a7ada16e0ffdcd987386179315951d54c8"
	if got := hex.EncodeToString(k.Key[:]); got != golden {
		t.Errorf("golden mismatch\n got  %s\n want %s", got, golden)
	}
}

// TestLoanKeylet pins the Loan keylet derivation:
// sha512Half(be16('L') || loanBrokerID || be32(loanSeq)) with type ltLOAN (0x89).
// Reference: rippled Indexes.cpp loan(uint256, uint32).
func TestLoanKeylet(t *testing.T) {
	var lbID [32]byte
	for i := range lbID {
		lbID[i] = byte(i)
	}
	const seq = 7

	k := Loan(lbID, seq)
	if k.Type != entry.TypeLoan {
		t.Errorf("Type = %#x, want %#x", uint16(k.Type), uint16(entry.TypeLoan))
	}

	want := sha512half.Sum(be16('L'), lbID[:], be32(seq))
	if k.Key != want {
		t.Errorf("key mismatch\n got  %x\n want %x", k.Key, want)
	}

	const golden = "b255d481a831e2fd5895d6bc033c07866de7d56c3b8fb843405d9484e3e58c94"
	if got := hex.EncodeToString(k.Key[:]); got != golden {
		t.Errorf("golden mismatch\n got  %s\n want %s", got, golden)
	}
}

// TestLoanNamespacesDistinct ensures the LoanBroker ('l') and Loan ('L')
// namespaces do not collide with each other despite the ASCII case pairing.
func TestLoanNamespacesDistinct(t *testing.T) {
	var id [32]byte
	for i := range id {
		id[i] = byte(i)
	}
	var owner [20]byte
	copy(owner[:], id[:20])
	if LoanBroker(owner, 1).Key == Loan(id, 1).Key {
		t.Error("LoanBroker and Loan keylets collided")
	}
}
