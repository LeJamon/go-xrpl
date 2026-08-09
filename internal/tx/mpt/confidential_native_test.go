//go:build mptcrypto && cgo

package mpt

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/crypto/mptcrypto"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

type confidentialTestView struct {
	entries         map[[32]byte][]byte
	rules           *amendment.Rules
	balanceOverride *int64
}

func (v *confidentialTestView) Read(k keylet.Keylet) ([]byte, error) { return v.entries[k.Key], nil }
func (v *confidentialTestView) Exists(k keylet.Keylet) (bool, error) {
	_, ok := v.entries[k.Key]
	return ok, nil
}
func (v *confidentialTestView) Insert(k keylet.Keylet, data []byte) error {
	v.entries[k.Key] = data
	return nil
}
func (v *confidentialTestView) Update(k keylet.Keylet, data []byte) error {
	v.entries[k.Key] = data
	return nil
}
func (v *confidentialTestView) Erase(k keylet.Keylet) error {
	delete(v.entries, k.Key)
	return nil
}
func (*confidentialTestView) AdjustDropsDestroyed(drops.XRPAmount) error { return nil }
func (*confidentialTestView) TxExists([32]byte) (bool, error)            { return false, nil }
func (v *confidentialTestView) Rules() *amendment.Rules                  { return v.rules }
func (*confidentialTestView) LedgerSeq() uint32                          { return 1 }
func (v *confidentialTestView) BalanceHookMPT(_ [20]byte, _ [24]byte, amount int64) int64 {
	if v.balanceOverride != nil {
		return *v.balanceOverride
	}
	return amount
}
func (v *confidentialTestView) ForEach(fn func([32]byte, []byte) bool) error {
	for k, data := range v.entries {
		if !fn(k, data) {
			break
		}
	}
	return nil
}
func (*confidentialTestView) Succ([32]byte) ([32]byte, []byte, bool, error) {
	return [32]byte{}, nil, false, nil
}

func confidentialHex(value []byte) string { return hex.EncodeToString(value) }

func confidentialKeyPair(t *testing.T) ([32]byte, []byte) {
	t.Helper()
	private, public, ok := mptcrypto.GenerateKeyPair()
	if !ok {
		t.Fatal("GenerateKeyPair")
	}
	return private, public
}

func confidentialBlind(t *testing.T) [32]byte {
	t.Helper()
	blind, ok := mptcrypto.GenerateBlindingFactor()
	if !ok {
		t.Fatal("GenerateBlindingFactor")
	}
	return blind
}

func confidentialEncrypt(t *testing.T, amount uint64, public []byte, blind [32]byte) []byte {
	t.Helper()
	ciphertext, ok := mptcrypto.EncryptAmount(amount, public, blind)
	if !ok {
		t.Fatal("EncryptAmount")
	}
	return ciphertext
}

func confidentialFixture(t *testing.T, holderPublic, issuerPublic []byte, amount, confidential uint64) (*confidentialTestView, [24]byte, [20]byte) {
	t.Helper()
	issuer := [20]byte{1}
	holder := [20]byte{2}
	id := keylet.MakeMPTID(3, issuer)
	issuance := &state.MPTokenIssuanceData{
		Issuer: issuer, Sequence: 3, OutstandingAmount: 100,
		ConfidentialOutstandingAmount: confidential,
		Flags:                         entry.LsfMPTCanHoldConfidentialBalance,
		IssuerEncryptionKey:           issuerPublic,
	}
	token := &state.MPTokenData{Account: holder, MPTokenIssuanceID: id, MPTAmount: amount, HolderEncryptionKey: holderPublic}
	issuanceData, err := state.SerializeMPTokenIssuance(issuance)
	if err != nil {
		t.Fatal(err)
	}
	tokenData, err := state.SerializeMPToken(token)
	if err != nil {
		t.Fatal(err)
	}
	issuanceKey := keylet.MPTIssuance(id)
	return &confidentialTestView{
		entries: map[[32]byte][]byte{
			issuanceKey.Key: issuanceData,
			keylet.MPToken(issuanceKey.Key, holder).Key: tokenData,
		},
		rules: amendment.NewRulesBuilder().Enable(amendment.FeatureConfidentialTransfer).Build(),
	}, id, holder
}

func TestConfidentialMPTConvertLifecycle(t *testing.T) {
	holderPrivate, holderPublic := confidentialKeyPair(t)
	_, issuerPublic := confidentialKeyPair(t)
	view, id, holder := confidentialFixture(t, nil, issuerPublic, 100, 0)
	blind := confidentialBlind(t)
	contextHash, ok := mptcrypto.ConvertContext(holder, id, 7)
	if !ok {
		t.Fatal("ConvertContext")
	}
	proof, ok := mptcrypto.GenerateConvertProof(holderPublic, holderPrivate, contextHash)
	if !ok {
		t.Fatal("GenerateConvertProof")
	}
	holderKey := confidentialHex(holderPublic)
	transaction := &ConfidentialMPTConvert{
		BaseTx:                *tx.NewBaseTx(tx.TypeConfidentialMPTConvert, state.EncodeAccountIDSafe(holder)),
		MPTokenIssuanceID:     confidentialHex(id[:]),
		MPTAmount:             40,
		HolderEncryptionKey:   &holderKey,
		HolderEncryptedAmount: confidentialHex(confidentialEncrypt(t, 40, holderPublic, blind)),
		IssuerEncryptedAmount: confidentialHex(confidentialEncrypt(t, 40, issuerPublic, blind)),
		BlindingFactor:        confidentialHex(blind[:]),
		ZKProof:               stringPtr(confidentialHex(proof)),
	}
	transaction.Sequence = uint32Ptr(7)
	if err := transaction.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	bad := *transaction
	bad.BlindingFactor = "00"
	if err := bad.Validate(); err != nil {
		t.Fatalf("short BlindingFactor was rejected in preflight: %v", err)
	}
	if got := bad.Preclaim(view, tx.EngineConfig{}); got != ter.TecBAD_PROOF {
		t.Fatalf("short BlindingFactor Preclaim = %v, want tecBAD_PROOF", got)
	}
	limitedBalance := int64(39)
	view.balanceOverride = &limitedBalance
	view.rules = amendment.NewRulesBuilder().
		Enable(amendment.FeatureConfidentialTransfer).
		Enable(amendment.FeatureMPTokensV2).
		Build()
	if got := transaction.Preclaim(view, tx.EngineConfig{}); got != ter.TecINSUFFICIENT_FUNDS {
		t.Fatalf("balance-hook-limited Preclaim = %v, want tecINSUFFICIENT_FUNDS", got)
	}
	view.balanceOverride = nil
	if got := transaction.Preclaim(view, tx.EngineConfig{}); got != ter.TesSUCCESS {
		t.Fatalf("Preclaim = %v", got)
	}
	if got := transaction.Apply(&tx.ApplyContext{View: view, AccountID: holder, Ctx: context.Background()}); got != ter.TesSUCCESS {
		t.Fatalf("Apply = %v", got)
	}

	issuance, _, token, _, _, got := readConfidentialState(view, id, transaction.Account)
	if got != ter.TesSUCCESS {
		t.Fatal(got)
	}
	if issuance.OutstandingAmount != 100 || issuance.ConfidentialOutstandingAmount != 40 || token.MPTAmount != 60 {
		t.Fatalf("post-Convert issuance=%+v token=%+v", issuance, token)
	}
	if len(token.ConfidentialBalanceInbox) == 0 || len(token.ConfidentialBalanceSpending) == 0 ||
		len(token.IssuerEncryptedBalance) == 0 || token.ConfidentialBalanceVersion != 0 {
		t.Fatalf("post-Convert confidential state=%+v", token)
	}

	merge := &ConfidentialMPTMergeInbox{
		BaseTx: *tx.NewBaseTx(tx.TypeConfidentialMPTMergeInbox, transaction.Account), MPTokenIssuanceID: transaction.MPTokenIssuanceID,
	}
	beforeInbox := append([]byte(nil), token.ConfidentialBalanceInbox...)
	if got := merge.Preclaim(view, tx.EngineConfig{}); got != ter.TesSUCCESS {
		t.Fatalf("Merge Preclaim = %v", got)
	}
	if got := merge.Apply(&tx.ApplyContext{View: view, AccountID: holder, Ctx: context.Background()}); got != ter.TesSUCCESS {
		t.Fatalf("Merge Apply = %v", got)
	}
	_, _, token, _, _, got = readConfidentialState(view, id, transaction.Account)
	if got != ter.TesSUCCESS || token.ConfidentialBalanceVersion != 1 ||
		len(token.ConfidentialBalanceInbox) == 0 || string(token.ConfidentialBalanceInbox) == string(beforeInbox) {
		t.Fatalf("post-Merge result=%v token=%+v", got, token)
	}
}

func TestConfidentialMPTConvertBackAndProofFailure(t *testing.T) {
	holderPrivate, holderPublic := confidentialKeyPair(t)
	_, issuerPublic := confidentialKeyPair(t)
	view, id, holder := confidentialFixture(t, holderPublic, issuerPublic, 60, 40)
	balanceBlind := confidentialBlind(t)
	spending := confidentialEncrypt(t, 40, holderPublic, balanceBlind)
	issuerBalance := confidentialEncrypt(t, 40, issuerPublic, balanceBlind)
	issuanceKey := keylet.MPTIssuance(id)
	tokenKey := keylet.MPToken(issuanceKey.Key, holder)
	token, err := state.ParseMPToken(view.entries[tokenKey.Key])
	if err != nil {
		t.Fatal(err)
	}
	token.ConfidentialBalanceInbox, _ = mptcrypto.CanonicalZero(holderPublic, holder, id)
	token.ConfidentialBalanceSpending = spending
	token.IssuerEncryptedBalance = issuerBalance
	token.ConfidentialBalanceVersion = 3
	view.entries[tokenKey.Key], err = state.SerializeMPToken(token)
	if err != nil {
		t.Fatal(err)
	}

	amountBlind := confidentialBlind(t)
	commitment, ok := mptcrypto.PedersenCommitment(40, balanceBlind)
	if !ok {
		t.Fatal("PedersenCommitment")
	}
	contextHash, ok := mptcrypto.ConvertBackContext(holder, id, 8, 3)
	if !ok {
		t.Fatal("ConvertBackContext")
	}
	proof, ok := mptcrypto.GenerateConvertBackProof(holderPrivate, holderPublic, contextHash, 15, commitment, 40, spending, balanceBlind)
	if !ok {
		t.Fatal("GenerateConvertBackProof")
	}
	transaction := &ConfidentialMPTConvertBack{
		BaseTx:                *tx.NewBaseTx(tx.TypeConfidentialMPTConvertBack, state.EncodeAccountIDSafe(holder)),
		MPTokenIssuanceID:     confidentialHex(id[:]),
		MPTAmount:             15,
		HolderEncryptedAmount: confidentialHex(confidentialEncrypt(t, 15, holderPublic, amountBlind)),
		IssuerEncryptedAmount: confidentialHex(confidentialEncrypt(t, 15, issuerPublic, amountBlind)),
		BlindingFactor:        confidentialHex(amountBlind[:]),
		ZKProof:               confidentialHex(proof),
		BalanceCommitment:     confidentialHex(commitment),
	}
	transaction.Sequence = uint32Ptr(8)
	if err := transaction.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	bad := *transaction
	bad.BlindingFactor = "00"
	if err := bad.Validate(); err != nil {
		t.Fatalf("short BlindingFactor was rejected in preflight: %v", err)
	}
	if got := bad.Preclaim(view, tx.EngineConfig{}); got != ter.TecBAD_PROOF {
		t.Fatalf("short BlindingFactor Preclaim = %v, want tecBAD_PROOF", got)
	}

	if got := transaction.Preclaim(view, tx.EngineConfig{}); got != ter.TesSUCCESS {
		t.Fatalf("Preclaim = %v", got)
	}
	if got := transaction.Apply(&tx.ApplyContext{View: view, AccountID: holder, Ctx: context.Background()}); got != ter.TesSUCCESS {
		t.Fatalf("Apply = %v", got)
	}
	issuance, _, token, _, _, got := readConfidentialState(view, id, transaction.Account)
	if got != ter.TesSUCCESS || issuance.ConfidentialOutstandingAmount != 25 || issuance.OutstandingAmount != 100 ||
		token.MPTAmount != 75 || token.ConfidentialBalanceVersion != 4 {
		t.Fatalf("post-ConvertBack result=%v issuance=%+v token=%+v", got, issuance, token)
	}
}

func TestMPTokenIssuanceSetConfidentialKeys(t *testing.T) {
	_, holderPublic := confidentialKeyPair(t)
	_, issuerPublic := confidentialKeyPair(t)
	_, auditorPublic := confidentialKeyPair(t)
	view, id, _ := confidentialFixture(t, holderPublic, nil, 0, 0)
	view.rules = amendment.NewRulesBuilder().
		Enable(amendment.FeatureDynamicMPT).
		Enable(amendment.FeatureConfidentialTransfer).
		Build()
	issuerKey := confidentialHex(issuerPublic)
	auditorKey := confidentialHex(auditorPublic)
	transaction := NewMPTokenIssuanceSet(state.EncodeAccountIDSafe([20]byte{1}), confidentialHex(id[:]))
	transaction.SetFlags(MPTokenIssuanceSetFlagSetCanHoldConfidentialBalance)
	transaction.IssuerEncryptionKey = &issuerKey
	transaction.AuditorEncryptionKey = &auditorKey
	if err := transaction.PreflightRules(view.rules); err != nil {
		t.Fatalf("PreflightRules: %v", err)
	}
	if got := transaction.Preclaim(view, tx.EngineConfig{}); got != ter.TesSUCCESS {
		t.Fatalf("Preclaim = %v", got)
	}
	if got := transaction.Apply(&tx.ApplyContext{View: view, Ctx: context.Background(), Log: xrpllog.Discard()}); got != ter.TesSUCCESS {
		t.Fatalf("Apply = %v", got)
	}
	issuance, _, _, got := readConfidentialIssuance(view, id, transaction.Account)
	if got != ter.TesSUCCESS || issuance.Flags&entry.LsfMPTCanHoldConfidentialBalance == 0 ||
		string(issuance.IssuerEncryptionKey) != string(issuerPublic) || string(issuance.AuditorEncryptionKey) != string(auditorPublic) {
		t.Fatalf("post-Set result=%v issuance=%+v", got, issuance)
	}
	if got := transaction.Preclaim(view, tx.EngineConfig{}); got != ter.TecNO_PERMISSION {
		t.Fatalf("duplicate key Preclaim = %v", got)
	}

	auditorOnly := NewMPTokenIssuanceSet(transaction.Account, confidentialHex(id[:]))
	auditorOnly.AuditorEncryptionKey = &auditorKey
	if err := auditorOnly.PreflightRules(view.rules); err == nil {
		t.Fatal("AuditorEncryptionKey without IssuerEncryptionKey passed")
	}
}

func stringPtr(value string) *string { return &value }
func uint32Ptr(value uint32) *uint32 { return &value }
