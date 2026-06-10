// Package batch provides test builder helpers for Batch transactions.
// These mirror rippled's test/jtx/batch.h infrastructure.
package batch

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/LeJamon/go-xrpl/crypto/ed25519"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
	"github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/account"
	batchtx "github.com/LeJamon/go-xrpl/internal/tx/batch"
	"github.com/LeJamon/go-xrpl/internal/tx/check"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ticket"
	"github.com/LeJamon/go-xrpl/keylet"
)

// CalcBatchFee calculates the expected batch fee.
// Formula: (numSigners + 2) * baseFee + baseFee * numInnerTxns
// Reference: rippled test/jtx/batch.h calcBatchFee()
func CalcBatchFee(baseFee uint64, numSigners uint32, numInnerTxns uint32) uint64 {
	return (uint64(numSigners)+2)*baseFee + baseFee*uint64(numInnerTxns)
}

// CalcBatchFeeFromEnv calculates the expected batch fee using the env's base fee.
func CalcBatchFeeFromEnv(env *testing.TestEnv, numSigners uint32, numInnerTxns uint32) uint64 {
	return CalcBatchFee(env.BaseFee(), numSigners, numInnerTxns)
}

// BatchBuilder provides a fluent interface for building Batch transactions in tests.
// Reference: rippled test/jtx/batch.h outer() + inner() classes
type BatchBuilder struct {
	account   *testing.Account
	seq       *uint32
	ticketSeq *uint32
	fee       uint64
	flag      uint32
	innerTxns []batchtx.RawTransaction
	signers   []batchtx.BatchSigner
	signSpecs []signSpec
	baseFee   uint64
}

// signSpec records how to produce a real BatchSigner signature once the batch
// digest is known at Build() time. For single-sign signers there is one entry;
// for multi-sign signers there is one per nested signer. signerAccounts holds
// the nested signer accounts (whose IDs are appended to the multi-sign message);
// signingKeys holds the accounts whose private keys actually sign.
type signSpec struct {
	index          int
	multi          bool
	signerAccounts []*testing.Account
	signingKeys    []*testing.Account
}

// NewBatchBuilder creates a new BatchBuilder for the given account.
// Reference: rippled test/jtx/batch.h outer()
func NewBatchBuilder(account *testing.Account, seq uint32, fee uint64, flag uint32) *BatchBuilder {
	return &BatchBuilder{
		account: account,
		seq:     &seq,
		fee:     fee,
		flag:    flag,
		baseFee: 10, // default
	}
}

// AddInnerTx adds an inner transaction object directly to the batch.
// The transaction should have Fee="0", SigningPubKey="", and tfInnerBatchTxn flag set.
// Reference: rippled test/jtx/batch.h inner class - stores full STObject
func (b *BatchBuilder) AddInnerTx(txn tx.Transaction) *BatchBuilder {
	b.innerTxns = append(b.innerTxns, batchtx.RawTransaction{
		RawTransaction: batchtx.RawTransactionData{
			InnerTx: txn,
		},
	})
	return b
}

// AddSigner adds a single-sign batch signer whose master key signs the digest.
// The signature passed here is a placeholder; the real signature over the batch
// digest is computed in Build().
// Reference: rippled test/jtx/batch.h sig class
func (b *BatchBuilder) AddSigner(account *testing.Account, signature string) *BatchBuilder {
	b.signSpecs = append(b.signSpecs, signSpec{
		index:       len(b.signers),
		signingKeys: []*testing.Account{account},
	})
	b.signers = append(b.signers, batchtx.BatchSigner{
		BatchSigner: batchtx.BatchSignerData{
			Account:           account.Address,
			SigningPubKey:     account.PublicKeyHex(),
			BatchTxnSignature: signature,
		},
	})
	return b
}

// AddSignerWithRegKey adds a single-sign batch signer using a regular key.
// The 'account' is the BatchSigner.Account, and 'regKey' provides the signing key.
// The real signature is computed in Build().
// Reference: rippled test/jtx/batch.h sig(Reg{account, regKey})
func (b *BatchBuilder) AddSignerWithRegKey(account, regKey *testing.Account, signature string) *BatchBuilder {
	b.signSpecs = append(b.signSpecs, signSpec{
		index:       len(b.signers),
		signingKeys: []*testing.Account{regKey},
	})
	b.signers = append(b.signers, batchtx.BatchSigner{
		BatchSigner: batchtx.BatchSignerData{
			Account:           account.Address,
			SigningPubKey:     regKey.PublicKeyHex(),
			BatchTxnSignature: signature,
		},
	})
	return b
}

// AddMismatchedSigner adds a single-sign batch signer that presents pubKey's
// public key but is signed with signingKey's private key. The signature is
// cryptographically valid for signingKey yet is verified against the unrelated
// pubKey, so it fails — reproducing rippled's temBAD_SIGNATURE vector where the
// SigningPubKey and TxnSignature come from different keys (Batch_test.cpp:568-575).
func (b *BatchBuilder) AddMismatchedSigner(account, pubKey, signingKey *testing.Account) *BatchBuilder {
	b.signSpecs = append(b.signSpecs, signSpec{
		index:       len(b.signers),
		signingKeys: []*testing.Account{signingKey},
	})
	b.signers = append(b.signers, batchtx.BatchSigner{
		BatchSigner: batchtx.BatchSignerData{
			Account:           account.Address,
			SigningPubKey:     pubKey.PublicKeyHex(),
			BatchTxnSignature: "DEADBEEF", // placeholder; real sig computed in Build()
		},
	})
	return b
}

// AddGarbageSigner adds a single-sign batch signer with account's public key but
// a fixed garbage signature that never verifies (no real signature is computed
// in Build()). Reproduces a corrupted-signature submission.
func (b *BatchBuilder) AddGarbageSigner(account *testing.Account) *BatchBuilder {
	b.signers = append(b.signers, batchtx.BatchSigner{
		BatchSigner: batchtx.BatchSignerData{
			Account:           account.Address,
			SigningPubKey:     account.PublicKeyHex(),
			BatchTxnSignature: "DEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEF",
		},
	})
	return b
}

// AddMultiSignBatchSigner adds a multi-sign batch signer with nested Signers.
// The 'masterAccount' is the account being multi-signed for, and 'signerAccounts'
// are the individual signers providing their keys.
// Nested signers are sorted by binary AccountID, the order the verifier requires.
// Reference: rippled test/jtx/batch.h msig(masterAccount, {signer1, signer2, ...})
func (b *BatchBuilder) AddMultiSignBatchSigner(masterAccount *testing.Account, signerAccounts []*testing.Account) *BatchBuilder {
	sorted := make([]*testing.Account, len(signerAccounts))
	copy(sorted, signerAccounts)
	// Nested signers must be in strictly-increasing binary AccountID order, the
	// order the verifier enforces (Batch.verifyBatchMultiSign). base58 address
	// order can disagree with binary order, so sort by the raw 20-byte ID.
	sort.Slice(sorted, func(i, j int) bool {
		return bytes.Compare(sorted[i].ID[:], sorted[j].ID[:]) < 0
	})
	nestedSigners := make([]tx.SignerWrapper, len(sorted))
	for i, s := range sorted {
		nestedSigners[i] = tx.SignerWrapper{
			Signer: tx.Signer{
				Account:       s.Address,
				SigningPubKey: s.PublicKeyHex(),
				TxnSignature:  "DEADBEEF", // placeholder; real sig computed in Build()
			},
		}
	}
	b.signSpecs = append(b.signSpecs, signSpec{
		index:          len(b.signers),
		multi:          true,
		signerAccounts: sorted,
		signingKeys:    sorted,
	})
	b.signers = append(b.signers, batchtx.BatchSigner{
		BatchSigner: batchtx.BatchSignerData{
			Account:       masterAccount.Address,
			SigningPubKey: "", // empty = multi-sign
			Signers:       nestedSigners,
		},
	})
	return b
}

// AddMultiSignBatchSignerWithRegKeys adds a multi-sign batch signer where some signers
// use regular keys. Each RegKeySigner specifies the account and the key used to sign.
// Nested signers are sorted by binary AccountID, the order the verifier requires.
// Reference: rippled test/jtx/batch.h msig(masterAccount, {Reg{account, regKey}, ...})
func (b *BatchBuilder) AddMultiSignBatchSignerWithRegKeys(masterAccount *testing.Account, signers []RegKeySigner) *BatchBuilder {
	sorted := make([]RegKeySigner, len(signers))
	copy(sorted, signers)
	// Sort by raw 20-byte AccountID — the strictly-increasing binary order the
	// verifier requires, which base58 address order does not always match.
	sort.Slice(sorted, func(i, j int) bool {
		return bytes.Compare(sorted[i].Account.ID[:], sorted[j].Account.ID[:]) < 0
	})
	nestedSigners := make([]tx.SignerWrapper, len(sorted))
	signerAccounts := make([]*testing.Account, len(sorted))
	signingKeys := make([]*testing.Account, len(sorted))
	for i, s := range sorted {
		nestedSigners[i] = tx.SignerWrapper{
			Signer: tx.Signer{
				Account:       s.Account.Address,
				SigningPubKey: s.SigningKey.PublicKeyHex(),
				TxnSignature:  "DEADBEEF", // placeholder; real sig computed in Build()
			},
		}
		signerAccounts[i] = s.Account
		signingKeys[i] = s.SigningKey
	}
	b.signSpecs = append(b.signSpecs, signSpec{
		index:          len(b.signers),
		multi:          true,
		signerAccounts: signerAccounts,
		signingKeys:    signingKeys,
	})
	b.signers = append(b.signers, batchtx.BatchSigner{
		BatchSigner: batchtx.BatchSignerData{
			Account:       masterAccount.Address,
			SigningPubKey: "", // empty = multi-sign
			Signers:       nestedSigners,
		},
	})
	return b
}

// RegKeySigner represents a signer that may use a different key than its master key.
// Account is the signer's account, SigningKey is the account whose public key is used.
// For master key signing, Account == SigningKey. For regular key signing, SigningKey is the reg key account.
type RegKeySigner struct {
	Account    *testing.Account
	SigningKey *testing.Account
}

// Build constructs the Batch transaction.
func (b *BatchBuilder) Build() *batchtx.Batch {
	batch := batchtx.NewBatch(b.account.Address)
	batch.Fee = fmt.Sprintf("%d", b.fee)
	if b.seq != nil {
		batch.SetSequence(*b.seq)
	}
	if b.ticketSeq != nil {
		batch.Common.TicketSequence = b.ticketSeq
	}
	batch.SetFlags(b.flag)
	batch.RawTransactions = b.innerTxns
	if len(b.signers) > 0 {
		batch.BatchSigners = b.signers
		b.signBatchSigners(batch)
	}
	return batch
}

// signBatchSigners replaces the placeholder BatchSigner signatures with real
// signatures over the batch digest, mirroring rippled's batch::sig / batch::msig
// test helpers. Specs whose digest cannot be computed (e.g. a deliberately
// malformed batch) are left with their placeholder signatures.
func (b *BatchBuilder) signBatchSigners(batch *batchtx.Batch) {
	digest, err := batch.BatchSigningMessage()
	if err != nil {
		return
	}
	for _, spec := range b.signSpecs {
		signer := &batch.BatchSigners[spec.index].BatchSigner
		if spec.multi {
			for j, key := range spec.signingKeys {
				nestedID := spec.signerAccounts[j].ID
				msg := append(append([]byte{}, digest...), nestedID[:]...)
				signer.Signers[j].Signer.TxnSignature = signBatchMessage(key, msg)
			}
			continue
		}
		signer.BatchTxnSignature = signBatchMessage(spec.signingKeys[0], digest)
	}
}

// signBatchMessage signs raw message bytes with the account's key, mirroring
// rippled's ripple::sign over the serializeBatch slice. The crypto scheme hashes
// the message internally.
func signBatchMessage(acc *testing.Account, msg []byte) string {
	priv := prefixedPrivateKeyHex(acc)
	if acc.IsEd25519() {
		sig, err := ed25519.ED25519().Sign(string(msg), priv)
		if err != nil {
			return ""
		}
		return sig
	}
	sig, err := secp256k1.SECP256K1().Sign(string(msg), priv)
	if err != nil {
		return ""
	}
	return sig
}

// prefixedPrivateKeyHex returns the key-type-prefixed private key hex expected by
// the crypto signing helpers (0xED for ed25519, 0x00 for secp256k1).
func prefixedPrivateKeyHex(acc *testing.Account) string {
	if acc.IsEd25519() {
		return "ED" + acc.PrivateKeyHex()
	}
	return "00" + acc.PrivateKeyHex()
}

// MakeFakeInnerTx creates a minimal valid inner transaction for validation-only tests.
// Returns a Payment that satisfies Batch.Validate() requirements.
func MakeFakeInnerTx() tx.Transaction {
	p := payment.NewPayment(
		"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", // genesis account
		"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		tx.NewXRPAmount(1),
	)
	p.Fee = "0"
	p.SigningPubKey = ""
	seq := uint32(1)
	p.Sequence = &seq
	flags := tx.TfInnerBatchTxn
	p.Flags = &flags
	return p
}

// MakeInnerPayment creates an inner Payment transaction suitable for batch inclusion.
// Sets Fee=0, SigningPubKey="", and adds tfInnerBatchTxn flag.
// Reference: rippled test/jtx/batch.h inner class with Payment
func MakeInnerPayment(from, to *testing.Account, amountDrops int64, seq uint32) *payment.Payment {
	p := payment.NewPayment(from.Address, to.Address, tx.NewXRPAmount(amountDrops))
	p.Fee = "0"
	p.SigningPubKey = ""
	p.SetSequence(seq)
	p.SetFlags(tx.TfInnerBatchTxn)
	return p
}

// MakeInnerPaymentXRPWithDelegate creates an inner XRP Payment with a Delegate field.
// Reference: rippled batch::inner(pay(...), seq) with tx[jss::Delegate] = delegate.human()
func MakeInnerPaymentXRPWithDelegate(from, to *testing.Account, xrp int64, seq uint32, delegate *testing.Account) *payment.Payment {
	p := MakeInnerPaymentXRP(from, to, xrp, seq)
	p.GetCommon().Delegate = delegate.Address
	return p
}

// MakeInnerPaymentXRP creates an inner Payment for an XRP amount in whole XRP units.
func MakeInnerPaymentXRP(from, to *testing.Account, xrp int64, seq uint32) *payment.Payment {
	return MakeInnerPayment(from, to, testing.XRP(xrp), seq)
}

// NewBatchBuilderWithTicket creates a new BatchBuilder where the outer batch uses a TicketSequence.
// Sequence is set to 0 and TicketSequence is set to the given ticketSeq.
// Reference: rippled batch::outer(alice, 0, batchFee, flag) + ticket::use(ticketSeq)
func NewBatchBuilderWithTicket(account *testing.Account, ticketSeq uint32, fee uint64, flag uint32) *BatchBuilder {
	zero := uint32(0)
	return &BatchBuilder{
		account:   account,
		seq:       &zero,
		fee:       fee,
		flag:      flag,
		baseFee:   10,
		ticketSeq: &ticketSeq,
	}
}

// MakeInnerPaymentXRPWithTicket creates an inner Payment for XRP that uses a TicketSequence.
// Sequence is set to 0 and TicketSequence is set to the given ticketSeq.
// Reference: rippled batch::inner(pay(...), 0, ticketSeq)
func MakeInnerPaymentXRPWithTicket(from, to *testing.Account, xrp int64, ticketSeq uint32) *payment.Payment {
	p := payment.NewPayment(from.Address, to.Address, tx.NewXRPAmount(testing.XRP(xrp)))
	p.Fee = "0"
	p.SigningPubKey = ""
	zero := uint32(0)
	p.Sequence = &zero
	p.SetFlags(tx.TfInnerBatchTxn)
	p.TicketSequence = &ticketSeq
	return p
}

// MakeInnerTicketCreate creates an inner TicketCreate transaction for batch inclusion.
// Sets Fee=0, SigningPubKey="", and adds tfInnerBatchTxn flag.
// Reference: rippled batch::inner(ticket::create(account, count), seq)
func MakeInnerTicketCreate(account *testing.Account, count uint32, seq uint32) *ticket.TicketCreate {
	tc := ticket.NewTicketCreate(account.Address, count)
	tc.Fee = "0"
	tc.SigningPubKey = ""
	tc.SetSequence(seq)
	tc.SetFlags(tx.TfInnerBatchTxn)
	return tc
}

// MakeInnerCheckCreate creates an inner CheckCreate transaction for batch inclusion.
// Sets Fee=0, SigningPubKey="", and adds tfInnerBatchTxn flag.
// Reference: rippled batch::inner(check::create(from, to, amount), seq)
func MakeInnerCheckCreate(from, to *testing.Account, sendMax tx.Amount, seq uint32) *check.CheckCreate {
	cc := check.NewCheckCreate(from.Address, to.Address, sendMax)
	cc.Fee = "0"
	cc.SigningPubKey = ""
	cc.SetSequence(seq)
	cc.SetFlags(tx.TfInnerBatchTxn)
	return cc
}

// MakeInnerCheckCreateWithTicket creates an inner CheckCreate that uses a TicketSequence.
// Sequence is set to 0 and TicketSequence is set to the given ticketSeq.
// Reference: rippled batch::inner(check::create(...), 0, ticketSeq)
func MakeInnerCheckCreateWithTicket(from, to *testing.Account, sendMax tx.Amount, ticketSeq uint32) *check.CheckCreate {
	cc := check.NewCheckCreate(from.Address, to.Address, sendMax)
	cc.Fee = "0"
	cc.SigningPubKey = ""
	zero := uint32(0)
	cc.Sequence = &zero
	cc.SetFlags(tx.TfInnerBatchTxn)
	cc.TicketSequence = &ticketSeq
	return cc
}

// MakeInnerCheckCash creates an inner CheckCash transaction with Amount for batch inclusion.
// Sets Fee=0, SigningPubKey="", and adds tfInnerBatchTxn flag.
// Reference: rippled batch::inner(check::cash(account, checkID, amount), seq)
func MakeInnerCheckCash(account *testing.Account, checkID string, amount tx.Amount, seq uint32) *check.CheckCash {
	cc := check.NewCheckCash(account.Address, checkID)
	cc.SetExactAmount(amount)
	cc.Fee = "0"
	cc.SigningPubKey = ""
	cc.SetSequence(seq)
	cc.SetFlags(tx.TfInnerBatchTxn)
	return cc
}

// MakeInnerAccountDelete creates an inner AccountDelete transaction for batch inclusion.
// Sets Fee=0, SigningPubKey="", and adds tfInnerBatchTxn flag.
// Reference: rippled batch::inner(acctdelete(account, dest), seq)
func MakeInnerAccountDelete(from, dest *testing.Account, seq uint32) *account.AccountDelete {
	ad := account.NewAccountDelete(from.Address, dest.Address)
	ad.Fee = "0"
	ad.SigningPubKey = ""
	ad.SetSequence(seq)
	ad.SetFlags(tx.TfInnerBatchTxn)
	return ad
}

// MakeInnerAccountSet creates an inner AccountSet (noop) transaction for batch inclusion.
// Sets Fee=0, SigningPubKey="", and adds tfInnerBatchTxn flag.
// Reference: rippled batch::inner(noop(account), seq) — noop is AccountSet with no flags.
func MakeInnerAccountSet(acc *testing.Account, seq uint32) *account.AccountSet {
	as := account.NewAccountSet(acc.Address)
	as.Fee = "0"
	as.SigningPubKey = ""
	as.SetSequence(seq)
	as.SetFlags(tx.TfInnerBatchTxn)
	return as
}

// GetCheckIndex returns the hex-encoded check keylet key for an account and sequence.
// This mirrors rippled's getCheckIndex(account, sequence) helper in Batch_test.cpp.
func GetCheckIndex(account *testing.Account, seq uint32) string {
	chk := keylet.Check(account.ID, seq)
	return hex.EncodeToString(chk.Key[:])
}
