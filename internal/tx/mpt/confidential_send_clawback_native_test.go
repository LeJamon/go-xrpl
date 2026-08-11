//go:build mptcrypto && cgo

package mpt

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/mptcrypto"
	"github.com/LeJamon/go-xrpl/drops"
	ledgercore "github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/applystate"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

type confidentialPreclaimView struct {
	entries        map[[32]byte][]byte
	rules          *amendment.Rules
	failMutationAt int
	mutations      int
}

func cloneConfidentialView(view *confidentialPreclaimView) *confidentialPreclaimView {
	entries := make(map[[32]byte][]byte, len(view.entries))
	for key, value := range view.entries {
		entries[key] = append([]byte(nil), value...)
	}
	return &confidentialPreclaimView{entries: entries, rules: view.rules}
}

func (v *confidentialPreclaimView) Read(k keylet.Keylet) ([]byte, error) {
	return v.entries[k.Key], nil
}

func (v *confidentialPreclaimView) Exists(k keylet.Keylet) (bool, error) {
	_, ok := v.entries[k.Key]
	return ok, nil
}

var errConfidentialMutation = errors.New("injected confidential mutation failure")

func (v *confidentialPreclaimView) Insert(k keylet.Keylet, data []byte) error {
	v.mutations++
	if v.failMutationAt == v.mutations {
		return errConfidentialMutation
	}
	v.entries[k.Key] = data
	return nil
}

func (v *confidentialPreclaimView) Update(k keylet.Keylet, data []byte) error {
	v.mutations++
	if v.failMutationAt == v.mutations {
		return errConfidentialMutation
	}
	v.entries[k.Key] = data
	return nil
}

func (v *confidentialPreclaimView) Erase(k keylet.Keylet) error {
	v.mutations++
	if v.failMutationAt == v.mutations {
		return errConfidentialMutation
	}
	delete(v.entries, k.Key)
	return nil
}

func (v *confidentialPreclaimView) ApplyAtomically(apply func(ledgercore.Writer) error) error {
	staged := cloneConfidentialView(v)
	staged.failMutationAt = v.failMutationAt
	staged.mutations = v.mutations
	if err := apply(staged); err != nil {
		v.mutations = staged.mutations
		return err
	}
	v.entries = staged.entries
	v.mutations = staged.mutations
	return nil
}

func (*confidentialPreclaimView) AdjustDropsDestroyed(drops.XRPAmount) error { return nil }
func (*confidentialPreclaimView) TxExists([32]byte) (bool, error)            { return false, nil }
func (v *confidentialPreclaimView) Rules() *amendment.Rules                  { return v.rules }
func (*confidentialPreclaimView) LedgerSeq() uint32                          { return 0 }

func (v *confidentialPreclaimView) ForEach(fn func([32]byte, []byte) bool) error {
	for key, data := range v.entries {
		if !fn(key, data) {
			break
		}
	}
	return nil
}

func (*confidentialPreclaimView) Succ([32]byte) ([32]byte, []byte, bool, error) {
	return [32]byte{}, nil, false, nil
}

func mustSerializeConfidentialIssuance(t *testing.T, issuance *state.MPTokenIssuanceData) []byte {
	t.Helper()
	data, err := state.SerializeMPTokenIssuance(issuance)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustSerializeConfidentialHolding(t *testing.T, token *state.MPTokenData) []byte {
	t.Helper()
	data, err := state.SerializeMPToken(token)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func confidentialMPTIDString(id [24]byte) string {
	return strings.ToUpper(hex.EncodeToString(id[:]))
}

func nativeValidationCode(t *testing.T, err error) ter.Result {
	t.Helper()
	if err == nil {
		t.Fatal("expected validation error")
	}
	var result *ter.ResultError
	if !errors.As(err, &result) {
		t.Fatalf("validation error = %T %v", err, err)
	}
	return result.Code
}

func mustNativeKeyPair(t *testing.T) ([32]byte, []byte) {
	t.Helper()
	private, public, ok := mptcrypto.GenerateKeyPair()
	if !ok {
		t.Fatal("generate key pair")
	}
	return private, public
}

func mustNativeBlind(t *testing.T) [32]byte {
	t.Helper()
	blind, ok := mptcrypto.GenerateBlindingFactor()
	if !ok {
		t.Fatal("generate blinding factor")
	}
	return blind
}

func mustNativeEncrypt(t *testing.T, amount uint64, public []byte, blind [32]byte) []byte {
	t.Helper()
	ciphertext, ok := mptcrypto.EncryptAmount(amount, public, blind)
	if !ok {
		t.Fatal("encrypt amount")
	}
	return ciphertext
}

func mustNativeZero(t *testing.T, public []byte, account [20]byte, id [24]byte) []byte {
	t.Helper()
	zero, ok := mptcrypto.CanonicalZero(public, account, id)
	if !ok {
		t.Fatal("canonical zero")
	}
	return zero
}

func putConfidentialAccount(t *testing.T, view *confidentialPreclaimView, account [20]byte) *state.AccountRoot {
	t.Helper()
	root := &state.AccountRoot{Account: state.EncodeAccountIDSafe(account), Balance: 1_000_000_000, Sequence: 1}
	data, err := state.SerializeAccountRoot(root)
	if err != nil {
		t.Fatalf("SerializeAccountRoot: %v", err)
	}
	view.entries[keylet.Account(account).Key] = data
	return root
}

func putNativeCredential(t *testing.T, view *confidentialPreclaimView, subject, credentialIssuer [20]byte, credentialType []byte, expiration uint32, accepted bool) keylet.Keylet {
	t.Helper()
	flags := uint32(0)
	if accepted {
		flags = entry.LsfAccepted
	}
	credentialKey := keylet.Credential(subject, credentialIssuer, credentialType)
	encoded, err := binarycodec.Encode(map[string]any{
		"LedgerEntryType":   "Credential",
		"Subject":           state.EncodeAccountIDSafe(subject),
		"Issuer":            state.EncodeAccountIDSafe(credentialIssuer),
		"CredentialType":    hex.EncodeToString(credentialType),
		"Expiration":        expiration,
		"Flags":             flags,
		"IssuerNode":        "0",
		"SubjectNode":       "0",
		"PreviousTxnID":     strings.Repeat("0", 64),
		"PreviousTxnLgrSeq": uint32(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	view.entries[credentialKey.Key] = raw
	return credentialKey
}

func putNativeDomainCredential(t *testing.T, view *confidentialPreclaimView, domainID [32]byte, domainOwner, subject, credentialIssuer [20]byte, credentialType []byte, expiration uint32) {
	t.Helper()
	if _, exists := view.entries[keylet.PermissionedDomainByID(domainID).Key]; !exists {
		domain, err := state.SerializePermissionedDomain(&state.PermissionedDomainData{
			Owner: domainOwner, Sequence: 1,
			AcceptedCredentials: []state.PermissionedDomainCredential{{Issuer: credentialIssuer, CredentialType: credentialType}},
		}, state.EncodeAccountIDSafe(domainOwner))
		if err != nil {
			t.Fatal(err)
		}
		view.entries[keylet.PermissionedDomainByID(domainID).Key] = domain
	}
	putNativeCredential(t, view, subject, credentialIssuer, credentialType, expiration, true)
}

func nativeApplyContext(view *confidentialPreclaimView, account [20]byte, root *state.AccountRoot) *tx.ApplyContext {
	return &tx.ApplyContext{
		View:      view,
		Account:   root,
		AccountID: account,
		Config:    tx.EngineConfig{Rules: view.rules},
		Metadata:  &tx.Metadata{},
		Log:       xrpllog.Discard(),
		Ctx:       context.Background(),
	}
}

func TestConfidentialMPTPreclaimsUseParentCloseTime(t *testing.T) {
	issuer := [20]byte{1}
	holder := [20]byte{2}
	credentialIssuer := [20]byte{8}
	id := keylet.MakeMPTID(4, issuer)
	domainID := [32]byte{4, 5, 6}
	domainHex := strings.ToUpper(hex.EncodeToString(domainID[:]))
	_, holderPublic := mustNativeKeyPair(t)
	_, issuerPublic := mustNativeKeyPair(t)
	blind := mustNativeBlind(t)
	holderBalance := mustNativeEncrypt(t, 10, holderPublic, blind)
	issuerBalance := mustNativeEncrypt(t, 10, issuerPublic, blind)
	view := &confidentialPreclaimView{entries: make(map[[32]byte][]byte), rules: amendment.NewRulesBuilder().Enable(amendment.FeatureConfidentialTransfer).Build()}
	putConfidentialAccount(t, view, holder)
	issuance := &state.MPTokenIssuanceData{
		Issuer: issuer, Sequence: 4, Flags: entry.LsfMPTCanHoldConfidentialBalance | entry.LsfMPTRequireAuth,
		OutstandingAmount: 10, ConfidentialOutstandingAmount: 10, IssuerEncryptionKey: issuerPublic, DomainID: &domainHex,
	}
	holding := &state.MPTokenData{
		Account: holder, MPTokenIssuanceID: id, MPTAmount: 10, HolderEncryptionKey: holderPublic,
		ConfidentialBalanceInbox: holderBalance, ConfidentialBalanceSpending: holderBalance, IssuerEncryptedBalance: issuerBalance,
	}
	view.entries[keylet.MPTIssuance(id).Key] = mustSerializeConfidentialIssuance(t, issuance)
	view.entries[keylet.MPToken(keylet.MPTIssuance(id).Key, holder).Key] = mustSerializeConfidentialHolding(t, holding)
	putNativeDomainCredential(t, view, domainID, issuer, holder, credentialIssuer, []byte("KYC"), 100)
	sequence := uint32(7)
	base := *tx.NewBaseTx(tx.TypeConfidentialMPTConvert, state.EncodeAccountIDSafe(holder))
	base.Sequence = &sequence
	convert := &ConfidentialMPTConvert{
		BaseTx: base, MPTokenIssuanceID: confidentialMPTIDString(id), MPTAmount: 1,
		HolderEncryptedAmount: hex.EncodeToString(holderBalance), IssuerEncryptedAmount: hex.EncodeToString(issuerBalance), BlindingFactor: hex.EncodeToString(blind[:]),
	}
	mergeBase := *tx.NewBaseTx(tx.TypeConfidentialMPTMergeInbox, state.EncodeAccountIDSafe(holder))
	mergeBase.Sequence = &sequence
	merge := &ConfidentialMPTMergeInbox{BaseTx: mergeBase, MPTokenIssuanceID: confidentialMPTIDString(id)}
	convertBackBase := *tx.NewBaseTx(tx.TypeConfidentialMPTConvertBack, state.EncodeAccountIDSafe(holder))
	convertBackBase.Sequence = &sequence
	convertBack := &ConfidentialMPTConvertBack{
		BaseTx: convertBackBase, MPTokenIssuanceID: confidentialMPTIDString(id), MPTAmount: 1,
		HolderEncryptedAmount: hex.EncodeToString(holderBalance), IssuerEncryptedAmount: hex.EncodeToString(issuerBalance), BlindingFactor: hex.EncodeToString(blind[:]),
		ZKProof: hex.EncodeToString(make([]byte, mptcrypto.ConvertBackProofSize)), BalanceCommitment: hex.EncodeToString(make([]byte, mptcrypto.CommitmentSize)),
	}
	tests := []struct {
		name       string
		preclaim   func(tx.EngineConfig) ter.Result
		atBoundary ter.Result
	}{
		{name: "convert", preclaim: func(config tx.EngineConfig) ter.Result { return convert.Preclaim(view, config) }, atBoundary: ter.TecBAD_PROOF},
		{name: "merge inbox", preclaim: func(config tx.EngineConfig) ter.Result { return merge.Preclaim(view, config) }, atBoundary: ter.TesSUCCESS},
		{name: "convert back", preclaim: func(config tx.EngineConfig) ter.Result { return convertBack.Preclaim(view, config) }, atBoundary: ter.TecBAD_PROOF},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.preclaim(tx.EngineConfig{ParentCloseTime: 100}); got != test.atBoundary {
				t.Fatalf("Preclaim at expiration = %v, want %v", got, test.atBoundary)
			}
			if got := test.preclaim(tx.EngineConfig{ParentCloseTime: 101}); got != ter.TecNO_AUTH {
				t.Fatalf("Preclaim after expiration = %v, want %v", got, ter.TecNO_AUTH)
			}
		})
	}
}

func TestConfidentialMPTSendNativePreclaimFailures(t *testing.T) {
	issuer := [20]byte{1}
	sender := [20]byte{2}
	destination := [20]byte{3}
	id := keylet.MakeMPTID(4, issuer)
	senderPrivate, senderPublic := mustNativeKeyPair(t)
	_, destinationPublic := mustNativeKeyPair(t)
	_, issuerPublic := mustNativeKeyPair(t)
	_, auditorPublic := mustNativeKeyPair(t)
	balanceBlind := mustNativeBlind(t)
	amountBlind := mustNativeBlind(t)
	issuance := &state.MPTokenIssuanceData{
		Issuer: issuer, Sequence: 4,
		Flags:                         entry.LsfMPTCanTransfer | entry.LsfMPTCanHoldConfidentialBalance,
		OutstandingAmount:             100,
		ConfidentialOutstandingAmount: 100,
		IssuerEncryptionKey:           issuerPublic,
		AuditorEncryptionKey:          auditorPublic,
	}
	senderToken := &state.MPTokenData{
		Account: sender, MPTokenIssuanceID: id, HolderEncryptionKey: senderPublic,
		ConfidentialBalanceInbox: mustNativeZero(t, senderPublic, sender, id), ConfidentialBalanceSpending: mustNativeEncrypt(t, 100, senderPublic, balanceBlind),
		IssuerEncryptedBalance: mustNativeEncrypt(t, 100, issuerPublic, balanceBlind), AuditorEncryptedBalance: mustNativeEncrypt(t, 100, auditorPublic, balanceBlind),
		ConfidentialBalanceVersion: 9,
	}
	destinationToken := &state.MPTokenData{
		Account: destination, MPTokenIssuanceID: id, HolderEncryptionKey: destinationPublic,
		ConfidentialBalanceInbox: mustNativeZero(t, destinationPublic, destination, id), ConfidentialBalanceSpending: mustNativeZero(t, destinationPublic, destination, id),
		IssuerEncryptedBalance: mustNativeZero(t, issuerPublic, destination, id), AuditorEncryptedBalance: mustNativeZero(t, auditorPublic, destination, id),
	}
	view := &confidentialPreclaimView{entries: make(map[[32]byte][]byte), rules: amendment.NewRulesBuilder().Enable(amendment.FeatureConfidentialTransfer).Build()}
	putConfidentialAccount(t, view, sender)
	putConfidentialAccount(t, view, destination)
	issuanceKey := keylet.MPTIssuance(id)
	senderKey := keylet.MPToken(issuanceKey.Key, sender)
	destinationKey := keylet.MPToken(issuanceKey.Key, destination)
	view.entries[issuanceKey.Key] = mustSerializeConfidentialIssuance(t, issuance)
	view.entries[senderKey.Key] = mustSerializeConfidentialHolding(t, senderToken)
	view.entries[destinationKey.Key] = mustSerializeConfidentialHolding(t, destinationToken)
	participants := []mptcrypto.Participant{
		{PublicKey: senderPublic, Ciphertext: mustNativeEncrypt(t, 40, senderPublic, amountBlind)},
		{PublicKey: destinationPublic, Ciphertext: mustNativeEncrypt(t, 40, destinationPublic, amountBlind)},
		{PublicKey: issuerPublic, Ciphertext: mustNativeEncrypt(t, 40, issuerPublic, amountBlind)},
		{PublicKey: auditorPublic, Ciphertext: mustNativeEncrypt(t, 40, auditorPublic, amountBlind)},
	}
	amountCommitment, _ := mptcrypto.PedersenCommitment(40, amountBlind)
	balanceCommitment, _ := mptcrypto.PedersenCommitment(100, balanceBlind)
	sequence := uint32(7)
	proofContext, _ := mptcrypto.SendContext(sender, id, sequence, destination, senderToken.ConfidentialBalanceVersion)
	proof, ok := mptcrypto.GenerateSendProof(senderPrivate, 40, participants, amountBlind, proofContext, amountCommitment, balanceCommitment, 100, senderToken.ConfidentialBalanceSpending, balanceBlind)
	if !ok {
		t.Fatal("send proof")
	}
	auditorAmount := hex.EncodeToString(participants[3].Ciphertext)
	valid := &ConfidentialMPTSend{
		BaseTx: *tx.NewBaseTx(tx.TypeConfidentialMPTSend, state.EncodeAccountIDSafe(sender)), MPTokenIssuanceID: confidentialMPTIDString(id), Destination: state.EncodeAccountIDSafe(destination),
		SenderEncryptedAmount: hex.EncodeToString(participants[0].Ciphertext), DestinationEncryptedAmount: hex.EncodeToString(participants[1].Ciphertext), IssuerEncryptedAmount: hex.EncodeToString(participants[2].Ciphertext),
		AuditorEncryptedAmount: &auditorAmount, ZKProof: hex.EncodeToString(proof), AmountCommitment: hex.EncodeToString(amountCommitment), BalanceCommitment: hex.EncodeToString(balanceCommitment),
	}
	valid.Sequence = &sequence

	serializeAccount := func(t *testing.T, view *confidentialPreclaimView, id [20]byte, edit func(*state.AccountRoot)) {
		root, err := state.ParseAccountRoot(view.entries[keylet.Account(id).Key])
		if err != nil {
			t.Fatal(err)
		}
		edit(root)
		view.entries[keylet.Account(id).Key], err = state.SerializeAccountRoot(root)
		if err != nil {
			t.Fatal(err)
		}
	}
	serializeIssuance := func(t *testing.T, view *confidentialPreclaimView, edit func(*state.MPTokenIssuanceData)) {
		value, err := state.ParseMPTokenIssuance(view.entries[issuanceKey.Key])
		if err != nil {
			t.Fatal(err)
		}
		edit(value)
		view.entries[issuanceKey.Key] = mustSerializeConfidentialIssuance(t, value)
	}
	serializeHolding := func(t *testing.T, view *confidentialPreclaimView, key keylet.Keylet, edit func(*state.MPTokenData)) {
		value, err := state.ParseMPToken(view.entries[key.Key])
		if err != nil {
			t.Fatal(err)
		}
		edit(value)
		view.entries[key.Key] = mustSerializeConfidentialHolding(t, value)
	}
	tests := []struct {
		name string
		want ter.Result
		edit func(*testing.T, *confidentialPreclaimView, *ConfidentialMPTSend)
	}{
		{name: "missing source", want: ter.TerNO_ACCOUNT, edit: func(_ *testing.T, v *confidentialPreclaimView, _ *ConfidentialMPTSend) {
			delete(v.entries, keylet.Account(sender).Key)
		}},
		{name: "missing destination", want: ter.TecNO_TARGET, edit: func(_ *testing.T, v *confidentialPreclaimView, _ *ConfidentialMPTSend) {
			delete(v.entries, keylet.Account(destination).Key)
		}},
		{name: "destination tag", want: ter.TecDST_TAG_NEEDED, edit: func(t *testing.T, v *confidentialPreclaimView, _ *ConfidentialMPTSend) {
			serializeAccount(t, v, destination, func(a *state.AccountRoot) { a.Flags |= state.LsfRequireDestTag })
		}},
		{name: "missing issuance", want: ter.TecOBJECT_NOT_FOUND, edit: func(_ *testing.T, v *confidentialPreclaimView, _ *ConfidentialMPTSend) {
			delete(v.entries, issuanceKey.Key)
		}},
		{name: "transfer disabled", want: ter.TecNO_AUTH, edit: func(t *testing.T, v *confidentialPreclaimView, _ *ConfidentialMPTSend) {
			serializeIssuance(t, v, func(i *state.MPTokenIssuanceData) { i.Flags &^= entry.LsfMPTCanTransfer })
		}},
		{name: "privacy disabled", want: ter.TecNO_PERMISSION, edit: func(t *testing.T, v *confidentialPreclaimView, _ *ConfidentialMPTSend) {
			serializeIssuance(t, v, func(i *state.MPTokenIssuanceData) { i.Flags &^= entry.LsfMPTCanHoldConfidentialBalance })
		}},
		{name: "transfer fee", want: ter.TecNO_PERMISSION, edit: func(t *testing.T, v *confidentialPreclaimView, _ *ConfidentialMPTSend) {
			serializeIssuance(t, v, func(i *state.MPTokenIssuanceData) { i.TransferFee = 1 })
		}},
		{name: "issuer key missing", want: ter.TecNO_PERMISSION, edit: func(t *testing.T, v *confidentialPreclaimView, _ *ConfidentialMPTSend) {
			serializeIssuance(t, v, func(i *state.MPTokenIssuanceData) { i.IssuerEncryptionKey = nil })
		}},
		{name: "issuer key malformed", want: ter.TecINTERNAL, edit: func(t *testing.T, v *confidentialPreclaimView, _ *ConfidentialMPTSend) {
			serializeIssuance(t, v, func(i *state.MPTokenIssuanceData) { i.IssuerEncryptionKey = []byte{2} })
		}},
		{name: "auditor key malformed", want: ter.TecINTERNAL, edit: func(t *testing.T, v *confidentialPreclaimView, _ *ConfidentialMPTSend) {
			serializeIssuance(t, v, func(i *state.MPTokenIssuanceData) { i.AuditorEncryptionKey = []byte{2} })
		}},
		{name: "auditor amount missing", want: ter.TecNO_PERMISSION, edit: func(_ *testing.T, _ *confidentialPreclaimView, c *ConfidentialMPTSend) {
			c.AuditorEncryptedAmount = nil
		}},
		{name: "unexpected auditor amount", want: ter.TecNO_PERMISSION, edit: func(t *testing.T, v *confidentialPreclaimView, _ *ConfidentialMPTSend) {
			serializeIssuance(t, v, func(i *state.MPTokenIssuanceData) { i.AuditorEncryptionKey = nil })
		}},
		{name: "sender holding missing", want: ter.TecOBJECT_NOT_FOUND, edit: func(_ *testing.T, v *confidentialPreclaimView, _ *ConfidentialMPTSend) {
			delete(v.entries, senderKey.Key)
		}},
		{name: "destination holding missing", want: ter.TecOBJECT_NOT_FOUND, edit: func(_ *testing.T, v *confidentialPreclaimView, _ *ConfidentialMPTSend) {
			delete(v.entries, destinationKey.Key)
		}},
		{name: "sender confidential state missing", want: ter.TecNO_PERMISSION, edit: func(t *testing.T, v *confidentialPreclaimView, _ *ConfidentialMPTSend) {
			serializeHolding(t, v, senderKey, func(h *state.MPTokenData) { h.ConfidentialBalanceSpending = nil })
		}},
		{name: "sender holder key malformed", want: ter.TecINTERNAL, edit: func(t *testing.T, v *confidentialPreclaimView, _ *ConfidentialMPTSend) {
			serializeHolding(t, v, senderKey, func(h *state.MPTokenData) { h.HolderEncryptionKey = []byte{2} })
		}},
		{name: "sender spending malformed", want: ter.TecINTERNAL, edit: func(t *testing.T, v *confidentialPreclaimView, _ *ConfidentialMPTSend) {
			serializeHolding(t, v, senderKey, func(h *state.MPTokenData) { h.ConfidentialBalanceSpending = []byte{2} })
		}},
		{name: "destination confidential state missing", want: ter.TecNO_PERMISSION, edit: func(t *testing.T, v *confidentialPreclaimView, _ *ConfidentialMPTSend) {
			serializeHolding(t, v, destinationKey, func(h *state.MPTokenData) { h.ConfidentialBalanceInbox = nil })
		}},
		{name: "destination holder key malformed", want: ter.TecINTERNAL, edit: func(t *testing.T, v *confidentialPreclaimView, _ *ConfidentialMPTSend) {
			serializeHolding(t, v, destinationKey, func(h *state.MPTokenData) { h.HolderEncryptionKey = []byte{2} })
		}},
		{name: "auditor balance missing", want: ter.TefINTERNAL, edit: func(t *testing.T, v *confidentialPreclaimView, _ *ConfidentialMPTSend) {
			serializeHolding(t, v, destinationKey, func(h *state.MPTokenData) { h.AuditorEncryptedBalance = nil })
		}},
		{name: "global frozen", want: ter.TecLOCKED, edit: func(t *testing.T, v *confidentialPreclaimView, _ *ConfidentialMPTSend) {
			serializeIssuance(t, v, func(i *state.MPTokenIssuanceData) { i.Flags |= entry.LsfMPTLocked })
		}},
		{name: "sender frozen", want: ter.TecLOCKED, edit: func(t *testing.T, v *confidentialPreclaimView, _ *ConfidentialMPTSend) {
			serializeHolding(t, v, senderKey, func(h *state.MPTokenData) { h.Flags |= entry.LsfMPTLocked })
		}},
		{name: "destination frozen", want: ter.TecLOCKED, edit: func(t *testing.T, v *confidentialPreclaimView, _ *ConfidentialMPTSend) {
			serializeHolding(t, v, destinationKey, func(h *state.MPTokenData) { h.Flags |= entry.LsfMPTLocked })
		}},
		{name: "sender unauthorized", want: ter.TecNO_AUTH, edit: func(t *testing.T, v *confidentialPreclaimView, _ *ConfidentialMPTSend) {
			serializeIssuance(t, v, func(i *state.MPTokenIssuanceData) { i.Flags |= entry.LsfMPTRequireAuth })
		}},
		{name: "destination unauthorized", want: ter.TecNO_AUTH, edit: func(t *testing.T, v *confidentialPreclaimView, _ *ConfidentialMPTSend) {
			serializeIssuance(t, v, func(i *state.MPTokenIssuanceData) { i.Flags |= entry.LsfMPTRequireAuth })
			serializeHolding(t, v, senderKey, func(h *state.MPTokenData) { h.Flags |= entry.LsfMPTAuthorized })
		}},
		{name: "bad credentials", want: ter.TecBAD_CREDENTIALS, edit: func(_ *testing.T, _ *confidentialPreclaimView, c *ConfidentialMPTSend) {
			c.CredentialIDs = []string{strings.Repeat("01", 32)}
		}},
		{name: "wrong credential subject", want: ter.TecBAD_CREDENTIALS, edit: func(t *testing.T, v *confidentialPreclaimView, c *ConfidentialMPTSend) {
			credentialKey := putNativeCredential(t, v, destination, [20]byte{8}, []byte("KYC"), 100, true)
			c.CredentialIDs = []string{hex.EncodeToString(credentialKey.Key[:])}
		}},
		{name: "unaccepted credential", want: ter.TecBAD_CREDENTIALS, edit: func(t *testing.T, v *confidentialPreclaimView, c *ConfidentialMPTSend) {
			credentialKey := putNativeCredential(t, v, sender, [20]byte{8}, []byte("KYC"), 100, false)
			c.CredentialIDs = []string{hex.EncodeToString(credentialKey.Key[:])}
		}},
		{name: "deposit auth", want: ter.TecNO_PERMISSION, edit: func(t *testing.T, v *confidentialPreclaimView, _ *ConfidentialMPTSend) {
			serializeAccount(t, v, destination, func(a *state.AccountRoot) { a.Flags |= state.LsfDepositAuth })
		}},
		{name: "credential deposit preauth missing", want: ter.TecNO_PERMISSION, edit: func(t *testing.T, v *confidentialPreclaimView, c *ConfidentialMPTSend) {
			credentialKey := putNativeCredential(t, v, sender, [20]byte{8}, []byte("KYC"), 100, true)
			c.CredentialIDs = []string{hex.EncodeToString(credentialKey.Key[:])}
			serializeAccount(t, v, destination, func(a *state.AccountRoot) { a.Flags |= state.LsfDepositAuth })
		}},
		{name: "encrypted amount exceeds spending balance", want: ter.TecBAD_PROOF, edit: func(t *testing.T, _ *confidentialPreclaimView, c *ConfidentialMPTSend) {
			c.SenderEncryptedAmount = hex.EncodeToString(mustNativeEncrypt(t, 110, senderPublic, amountBlind))
			c.DestinationEncryptedAmount = hex.EncodeToString(mustNativeEncrypt(t, 110, destinationPublic, amountBlind))
			c.IssuerEncryptedAmount = hex.EncodeToString(mustNativeEncrypt(t, 110, issuerPublic, amountBlind))
			auditorAmount := hex.EncodeToString(mustNativeEncrypt(t, 110, auditorPublic, amountBlind))
			c.AuditorEncryptedAmount = &auditorAmount
			commitment, ok := mptcrypto.PedersenCommitment(110, amountBlind)
			if !ok {
				t.Fatal("amount commitment")
			}
			c.AmountCommitment = hex.EncodeToString(commitment)
		}},
		{name: "bad proof", want: ter.TecBAD_PROOF, edit: func(_ *testing.T, _ *confidentialPreclaimView, c *ConfidentialMPTSend) {
			raw, _ := hex.DecodeString(c.ZKProof)
			raw[0] ^= 1
			c.ZKProof = hex.EncodeToString(raw)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := *valid
			candidateView := cloneConfidentialView(view)
			test.edit(t, candidateView, &candidate)
			if got := candidate.Preclaim(candidateView, tx.EngineConfig{}); got != test.want {
				t.Fatalf("Preclaim = %v, want %v", got, test.want)
			}
		})
	}
	t.Run("account deposit preauth succeeds", func(t *testing.T) {
		candidate := *valid
		candidateView := cloneConfidentialView(view)
		serializeAccount(t, candidateView, destination, func(a *state.AccountRoot) { a.Flags |= state.LsfDepositAuth })
		candidateView.entries[keylet.DepositPreauth(destination, sender).Key] = []byte{1}
		if got := candidate.Preclaim(candidateView, tx.EngineConfig{}); got != ter.TesSUCCESS {
			t.Fatalf("Preclaim = %v, want %v", got, ter.TesSUCCESS)
		}
	})
	t.Run("credential deposit preauth succeeds", func(t *testing.T) {
		candidate := *valid
		candidateView := cloneConfidentialView(view)
		credentialIssuer := [20]byte{8}
		credentialType := []byte("KYC")
		credentialKey := putNativeCredential(t, candidateView, sender, credentialIssuer, credentialType, 100, true)
		candidate.CredentialIDs = []string{hex.EncodeToString(credentialKey.Key[:])}
		serializeAccount(t, candidateView, destination, func(a *state.AccountRoot) { a.Flags |= state.LsfDepositAuth })
		preauth := keylet.DepositPreauthCredentials(destination, []keylet.CredentialPair{{Issuer: credentialIssuer, CredentialType: credentialType}})
		candidateView.entries[preauth.Key] = []byte{1}
		if got := candidate.Preclaim(candidateView, tx.EngineConfig{}); got != ter.TesSUCCESS {
			t.Fatalf("Preclaim = %v, want %v", got, ter.TesSUCCESS)
		}
	})
}

func validNativeSend(t *testing.T) ConfidentialMPTSend {
	t.Helper()
	issuer := [20]byte{1}
	sender := [20]byte{2}
	destination := [20]byte{3}
	id := keylet.MakeMPTID(4, issuer)
	_, senderPublic := mustNativeKeyPair(t)
	_, destinationPublic := mustNativeKeyPair(t)
	_, issuerPublic := mustNativeKeyPair(t)
	blind := mustNativeBlind(t)
	amountCommitment, ok := mptcrypto.PedersenCommitment(40, blind)
	if !ok {
		t.Fatal("amount commitment")
	}
	balanceCommitment, ok := mptcrypto.PedersenCommitment(100, mustNativeBlind(t))
	if !ok {
		t.Fatal("balance commitment")
	}
	return ConfidentialMPTSend{
		BaseTx:                     *tx.NewBaseTx(tx.TypeConfidentialMPTSend, state.EncodeAccountIDSafe(sender)),
		MPTokenIssuanceID:          confidentialMPTIDString(id),
		Destination:                state.EncodeAccountIDSafe(destination),
		SenderEncryptedAmount:      hex.EncodeToString(mustNativeEncrypt(t, 40, senderPublic, blind)),
		DestinationEncryptedAmount: hex.EncodeToString(mustNativeEncrypt(t, 40, destinationPublic, blind)),
		IssuerEncryptedAmount:      hex.EncodeToString(mustNativeEncrypt(t, 40, issuerPublic, blind)),
		ZKProof:                    hex.EncodeToString(make([]byte, mptcrypto.SendProofSize)),
		AmountCommitment:           hex.EncodeToString(amountCommitment),
		BalanceCommitment:          hex.EncodeToString(balanceCommitment),
	}
}

func TestConfidentialMPTSendNativePreflight(t *testing.T) {
	valid := validNativeSend(t)
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid send: %v", err)
	}
	issuerID, _ := parseConfidentialID(valid.MPTokenIssuanceID)
	issuer := [20]byte(issuerID[4:])

	tests := []struct {
		name string
		edit func(*ConfidentialMPTSend)
		want ter.Result
	}{
		{name: "sender is issuer", edit: func(c *ConfidentialMPTSend) { c.Account = state.EncodeAccountIDSafe(issuer) }, want: ter.TemMALFORMED},
		{name: "sender is destination", edit: func(c *ConfidentialMPTSend) { c.Destination = c.Account }, want: ter.TemMALFORMED},
		{name: "destination is issuer", edit: func(c *ConfidentialMPTSend) { c.Destination = state.EncodeAccountIDSafe(issuer) }, want: ter.TemMALFORMED},
		{name: "ciphertext length precedes proof", edit: func(c *ConfidentialMPTSend) { c.SenderEncryptedAmount = "00"; c.ZKProof = "00" }, want: ter.TemBAD_CIPHERTEXT},
		{name: "proof length precedes commitments", edit: func(c *ConfidentialMPTSend) { c.ZKProof = "00"; c.BalanceCommitment = makeHex(33, 0) }, want: ter.TemMALFORMED},
		{name: "balance commitment", edit: func(c *ConfidentialMPTSend) { c.BalanceCommitment = makeHex(33, 0) }, want: ter.TemMALFORMED},
		{name: "amount commitment", edit: func(c *ConfidentialMPTSend) { c.AmountCommitment = makeHex(33, 0) }, want: ter.TemMALFORMED},
		{name: "invalid ciphertext point", edit: func(c *ConfidentialMPTSend) { c.IssuerEncryptedAmount = makeHex(66, 0) }, want: ter.TemBAD_CIPHERTEXT},
		{name: "empty credentials", edit: func(c *ConfidentialMPTSend) {
			c.CredentialIDs = []string{}
			c.SetPresentFields(map[string]bool{"CredentialIDs": true})
		}, want: ter.TemMALFORMED},
		{name: "too many credentials", edit: func(c *ConfidentialMPTSend) { c.CredentialIDs = make([]string, 9) }, want: ter.TemMALFORMED},
		{name: "case-insensitive duplicate credential", edit: func(c *ConfidentialMPTSend) {
			c.CredentialIDs = []string{"abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789", "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789"}
		}, want: ter.TemMALFORMED},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.edit(&candidate)
			if got := nativeValidationCode(t, candidate.Validate()); got != test.want {
				t.Fatalf("Validate = %v, want %v", got, test.want)
			}
		})
	}
}

func TestConfidentialMPTSendJSONRejectsMalformedCredentialIDs(t *testing.T) {
	tests := []struct {
		name string
		id   string
	}{
		{name: "not hex", id: strings.Repeat("z", 64)},
		{name: "too short", id: strings.Repeat("01", 31)},
		{name: "too long", id: strings.Repeat("01", 33)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := validNativeSend(t)
			candidate.CredentialIDs = []string{test.id}
			data, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			var decoded ConfidentialMPTSend
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			if got := nativeValidationCode(t, decoded.Validate()); got != ter.TemMALFORMED {
				t.Fatalf("Validate = %v, want %v", got, ter.TemMALFORMED)
			}
		})
	}
}

func TestConfidentialMPTClawbackNativePreflight(t *testing.T) {
	issuer := [20]byte{1}
	holder := [20]byte{2}
	id := keylet.MakeMPTID(4, issuer)
	valid := ConfidentialMPTClawback{
		BaseTx:            *tx.NewBaseTx(tx.TypeConfidentialMPTClawback, state.EncodeAccountIDSafe(issuer)),
		MPTokenIssuanceID: confidentialMPTIDString(id),
		Holder:            state.EncodeAccountIDSafe(holder),
		MPTAmount:         1,
		ZKProof:           makeHex(mptcrypto.ClawbackProofSize, 1),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid clawback: %v", err)
	}
	tests := []struct {
		name string
		edit func(*ConfidentialMPTClawback)
		want ter.Result
	}{
		{name: "nonissuer", edit: func(c *ConfidentialMPTClawback) { c.Account = state.EncodeAccountIDSafe([20]byte{3}) }, want: ter.TemMALFORMED},
		{name: "self", edit: func(c *ConfidentialMPTClawback) { c.Holder = c.Account }, want: ter.TemMALFORMED},
		{name: "zero amount", edit: func(c *ConfidentialMPTClawback) { c.MPTAmount = 0 }, want: ter.TemBAD_AMOUNT},
		{name: "over max amount", edit: func(c *ConfidentialMPTClawback) { c.MPTAmount = ^uint64(0) }, want: ter.TemBAD_AMOUNT},
		{name: "proof length", edit: func(c *ConfidentialMPTClawback) { c.ZKProof = "00" }, want: ter.TemMALFORMED},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.edit(&candidate)
			if got := nativeValidationCode(t, candidate.Validate()); got != test.want {
				t.Fatalf("Validate = %v, want %v", got, test.want)
			}
		})
	}
}

func TestConfidentialMPTSendNativePreclaimAndApply(t *testing.T) {
	for _, withAuditor := range []bool{false, true} {
		t.Run(map[bool]string{false: "without auditor", true: "with auditor"}[withAuditor], func(t *testing.T) {
			issuer := [20]byte{1}
			sender := [20]byte{2}
			destination := [20]byte{3}
			id := keylet.MakeMPTID(4, issuer)
			domainID := [32]byte{4, 5, 6}
			domainHex := strings.ToUpper(hex.EncodeToString(domainID[:]))
			credentialIssuer := [20]byte{8}
			credentialType := []byte("KYC")
			senderPrivate, senderPublic := mustNativeKeyPair(t)
			_, destinationPublic := mustNativeKeyPair(t)
			_, issuerPublic := mustNativeKeyPair(t)
			_, auditorPublic := mustNativeKeyPair(t)
			balanceBlind := mustNativeBlind(t)
			amountBlind := mustNativeBlind(t)

			issuance := &state.MPTokenIssuanceData{
				Issuer: issuer, Sequence: 4,
				Flags:                         entry.LsfMPTCanTransfer | entry.LsfMPTCanHoldConfidentialBalance | entry.LsfMPTRequireAuth,
				OutstandingAmount:             100,
				ConfidentialOutstandingAmount: 100,
				IssuerEncryptionKey:           issuerPublic,
				DomainID:                      &domainHex,
			}
			if withAuditor {
				issuance.AuditorEncryptionKey = auditorPublic
			}
			senderToken := &state.MPTokenData{
				Account:                     sender,
				MPTokenIssuanceID:           id,
				HolderEncryptionKey:         senderPublic,
				ConfidentialBalanceInbox:    mustNativeZero(t, senderPublic, sender, id),
				ConfidentialBalanceSpending: mustNativeEncrypt(t, 100, senderPublic, balanceBlind),
				IssuerEncryptedBalance:      mustNativeEncrypt(t, 100, issuerPublic, balanceBlind),
				ConfidentialBalanceVersion:  9,
				MPTAmount:                   7,
			}
			destinationToken := &state.MPTokenData{
				Account:                     destination,
				MPTokenIssuanceID:           id,
				HolderEncryptionKey:         destinationPublic,
				ConfidentialBalanceInbox:    mustNativeZero(t, destinationPublic, destination, id),
				ConfidentialBalanceSpending: mustNativeZero(t, destinationPublic, destination, id),
				IssuerEncryptedBalance:      mustNativeZero(t, issuerPublic, destination, id),
				ConfidentialBalanceVersion:  5,
				MPTAmount:                   11,
			}
			if withAuditor {
				senderToken.AuditorEncryptedBalance = mustNativeEncrypt(t, 100, auditorPublic, balanceBlind)
				destinationToken.AuditorEncryptedBalance = mustNativeZero(t, auditorPublic, destination, id)
			}

			rules := amendment.NewRulesBuilder().Enable(amendment.FeatureConfidentialTransfer).Build()
			view := &confidentialPreclaimView{entries: make(map[[32]byte][]byte), rules: rules}
			senderRoot := putConfidentialAccount(t, view, sender)
			putConfidentialAccount(t, view, destination)
			view.entries[keylet.MPTIssuance(id).Key] = mustSerializeConfidentialIssuance(t, issuance)
			senderKey := keylet.MPToken(keylet.MPTIssuance(id).Key, sender)
			destinationKey := keylet.MPToken(keylet.MPTIssuance(id).Key, destination)
			view.entries[senderKey.Key] = mustSerializeConfidentialHolding(t, senderToken)
			view.entries[destinationKey.Key] = mustSerializeConfidentialHolding(t, destinationToken)
			putNativeDomainCredential(t, view, domainID, issuer, sender, credentialIssuer, credentialType, 100)
			putNativeDomainCredential(t, view, domainID, issuer, destination, credentialIssuer, credentialType, 100)

			participants := []mptcrypto.Participant{
				{PublicKey: senderPublic, Ciphertext: mustNativeEncrypt(t, 40, senderPublic, amountBlind)},
				{PublicKey: destinationPublic, Ciphertext: mustNativeEncrypt(t, 40, destinationPublic, amountBlind)},
				{PublicKey: issuerPublic, Ciphertext: mustNativeEncrypt(t, 40, issuerPublic, amountBlind)},
			}
			if withAuditor {
				participants = append(participants, mptcrypto.Participant{PublicKey: auditorPublic, Ciphertext: mustNativeEncrypt(t, 40, auditorPublic, amountBlind)})
			}
			amountCommitment, ok := mptcrypto.PedersenCommitment(40, amountBlind)
			if !ok {
				t.Fatal("amount commitment")
			}
			balanceCommitment, ok := mptcrypto.PedersenCommitment(100, balanceBlind)
			if !ok {
				t.Fatal("balance commitment")
			}
			sequence := uint32(7)
			proofContext, ok := mptcrypto.SendContext(sender, id, sequence, destination, senderToken.ConfidentialBalanceVersion)
			if !ok {
				t.Fatal("send context")
			}
			proof, ok := mptcrypto.GenerateSendProof(senderPrivate, 40, participants, amountBlind, proofContext, amountCommitment, balanceCommitment, 100, senderToken.ConfidentialBalanceSpending, balanceBlind)
			if !ok {
				t.Fatal("send proof")
			}
			transaction := &ConfidentialMPTSend{
				BaseTx:                     *tx.NewBaseTx(tx.TypeConfidentialMPTSend, state.EncodeAccountIDSafe(sender)),
				MPTokenIssuanceID:          confidentialMPTIDString(id),
				Destination:                state.EncodeAccountIDSafe(destination),
				SenderEncryptedAmount:      hex.EncodeToString(participants[0].Ciphertext),
				DestinationEncryptedAmount: hex.EncodeToString(participants[1].Ciphertext),
				IssuerEncryptedAmount:      hex.EncodeToString(participants[2].Ciphertext),
				ZKProof:                    hex.EncodeToString(proof),
				AmountCommitment:           hex.EncodeToString(amountCommitment),
				BalanceCommitment:          hex.EncodeToString(balanceCommitment),
			}
			transaction.Sequence = &sequence
			if withAuditor {
				encoded := hex.EncodeToString(participants[3].Ciphertext)
				transaction.AuditorEncryptedAmount = &encoded
			}
			if err := transaction.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if got := transaction.Preclaim(view, tx.EngineConfig{ParentCloseTime: 100}); got != ter.TesSUCCESS {
				t.Fatalf("Preclaim = %v", got)
			}
			if got := transaction.Preclaim(view, tx.EngineConfig{ParentCloseTime: 101}); got != ter.TecNO_AUTH {
				t.Fatalf("Preclaim after domain credential expiration = %v, want %v", got, ter.TecNO_AUTH)
			}
			t.Run("missing destination account", func(t *testing.T) {
				missingDestinationView := cloneConfidentialView(view)
				delete(missingDestinationView.entries, keylet.Account(destination).Key)
				if got := transaction.Apply(nativeApplyContext(missingDestinationView, sender, senderRoot)); got != ter.TecINTERNAL {
					t.Fatalf("Apply = %v, want %v", got, ter.TecINTERNAL)
				}
				if missingDestinationView.mutations != 0 {
					t.Fatalf("Apply performed %d mutations", missingDestinationView.mutations)
				}
			})
			if got := transaction.Apply(nativeApplyContext(view, sender, senderRoot)); got != ter.TesSUCCESS {
				t.Fatalf("Apply = %v", got)
			}

			updatedSender, err := state.ParseMPToken(view.entries[senderKey.Key])
			if err != nil {
				t.Fatal(err)
			}
			updatedDestination, err := state.ParseMPToken(view.entries[destinationKey.Key])
			if err != nil {
				t.Fatal(err)
			}
			if updatedSender.ConfidentialBalanceVersion != 10 || updatedDestination.ConfidentialBalanceVersion != 5 {
				t.Fatalf("versions = sender %d, destination %d", updatedSender.ConfidentialBalanceVersion, updatedDestination.ConfidentialBalanceVersion)
			}
			if updatedSender.MPTAmount != 7 || updatedDestination.MPTAmount != 11 {
				t.Fatal("send changed public balances")
			}
			updatedIssuance, err := state.ParseMPTokenIssuance(view.entries[keylet.MPTIssuance(id).Key])
			if err != nil {
				t.Fatal(err)
			}
			if updatedIssuance.OutstandingAmount != 100 || updatedIssuance.ConfidentialOutstandingAmount != 100 {
				t.Fatal("send changed issuance amounts")
			}
			expectedSenderSpending, ok := mptcrypto.SubtractCiphertexts(senderToken.ConfidentialBalanceSpending, participants[0].Ciphertext)
			if !ok || string(updatedSender.ConfidentialBalanceSpending) != string(expectedSenderSpending) {
				t.Fatal("sender spending balance was not reduced")
			}
			expectedSenderIssuer, ok := mptcrypto.SubtractCiphertexts(senderToken.IssuerEncryptedBalance, participants[2].Ciphertext)
			if !ok || string(updatedSender.IssuerEncryptedBalance) != string(expectedSenderIssuer) {
				t.Fatal("sender issuer mirror was not reduced")
			}
			var challenge [32]byte
			copy(challenge[:], proof[:32])
			expectedReceive, ok := mptcrypto.RerandomizeCiphertext(participants[1].Ciphertext, destinationPublic, challenge)
			if !ok {
				t.Fatal("rerandomize expected destination amount")
			}
			expectedInbox, ok := mptcrypto.AddCiphertexts(destinationToken.ConfidentialBalanceInbox, expectedReceive)
			if !ok || string(updatedDestination.ConfidentialBalanceInbox) != string(expectedInbox) {
				t.Fatal("destination inbox was not rerandomized before addition")
			}
			expectedIssuerReceive, ok := mptcrypto.RerandomizeCiphertext(participants[2].Ciphertext, issuerPublic, challenge)
			if !ok {
				t.Fatal("rerandomize expected issuer amount")
			}
			expectedDestinationIssuer, ok := mptcrypto.AddCiphertexts(destinationToken.IssuerEncryptedBalance, expectedIssuerReceive)
			if !ok || string(updatedDestination.IssuerEncryptedBalance) != string(expectedDestinationIssuer) {
				t.Fatal("destination issuer mirror was not rerandomized before addition")
			}
			if withAuditor {
				expectedSenderAuditor, ok := mptcrypto.SubtractCiphertexts(senderToken.AuditorEncryptedBalance, participants[3].Ciphertext)
				if !ok || string(updatedSender.AuditorEncryptedBalance) != string(expectedSenderAuditor) {
					t.Fatal("sender auditor mirror was not reduced")
				}
				expectedAuditorReceive, ok := mptcrypto.RerandomizeCiphertext(participants[3].Ciphertext, auditorPublic, challenge)
				if !ok {
					t.Fatal("rerandomize expected auditor amount")
				}
				expectedAuditor, ok := mptcrypto.AddCiphertexts(destinationToken.AuditorEncryptedBalance, expectedAuditorReceive)
				if !ok || string(updatedDestination.AuditorEncryptedBalance) != string(expectedAuditor) {
					t.Fatal("destination auditor balance was not rerandomized before addition")
				}
			}
		})
	}
}

func TestConfidentialMPTClawbackNativePreclaimAndApply(t *testing.T) {
	issuer := [20]byte{1}
	holder := [20]byte{2}
	id := keylet.MakeMPTID(4, issuer)
	issuerPrivate, issuerPublic := mustNativeKeyPair(t)
	_, holderPublic := mustNativeKeyPair(t)
	_, auditorPublic := mustNativeKeyPair(t)
	blind := mustNativeBlind(t)
	issuerBalance := mustNativeEncrypt(t, 75, issuerPublic, blind)
	issuance := &state.MPTokenIssuanceData{
		Issuer: issuer, Sequence: 4,
		Flags:                         entry.LsfMPTCanClawback | entry.LsfMPTCanHoldConfidentialBalance | entry.LsfMPTLocked | entry.LsfMPTRequireAuth,
		OutstandingAmount:             75,
		ConfidentialOutstandingAmount: 75,
		IssuerEncryptionKey:           issuerPublic,
		AuditorEncryptionKey:          auditorPublic,
	}
	holding := &state.MPTokenData{
		Account:                     holder,
		MPTokenIssuanceID:           id,
		Flags:                       entry.LsfMPTLocked,
		MPTAmount:                   13,
		HolderEncryptionKey:         holderPublic,
		ConfidentialBalanceInbox:    mustNativeEncrypt(t, 50, holderPublic, blind),
		ConfidentialBalanceSpending: mustNativeEncrypt(t, 25, holderPublic, blind),
		IssuerEncryptedBalance:      issuerBalance,
		AuditorEncryptedBalance:     mustNativeEncrypt(t, 75, auditorPublic, blind),
		ConfidentialBalanceVersion:  6,
	}
	rules := amendment.NewRulesBuilder().Enable(amendment.FeatureConfidentialTransfer).Build()
	view := &confidentialPreclaimView{entries: make(map[[32]byte][]byte), rules: rules}
	issuerRoot := putConfidentialAccount(t, view, issuer)
	putConfidentialAccount(t, view, holder)
	issuanceKey := keylet.MPTIssuance(id)
	holdingKey := keylet.MPToken(issuanceKey.Key, holder)
	view.entries[issuanceKey.Key] = mustSerializeConfidentialIssuance(t, issuance)
	view.entries[holdingKey.Key] = mustSerializeConfidentialHolding(t, holding)

	sequence := uint32(9)
	proofContext, ok := mptcrypto.ClawbackContext(issuer, id, sequence, holder)
	if !ok {
		t.Fatal("clawback context")
	}
	proof, ok := mptcrypto.GenerateClawbackProof(issuerPrivate, issuerPublic, proofContext, 75, issuerBalance)
	if !ok {
		t.Fatal("clawback proof")
	}
	holding.ConfidentialBalanceVersion = 99
	view.entries[holdingKey.Key] = mustSerializeConfidentialHolding(t, holding)
	transaction := &ConfidentialMPTClawback{
		BaseTx:            *tx.NewBaseTx(tx.TypeConfidentialMPTClawback, state.EncodeAccountIDSafe(issuer)),
		MPTokenIssuanceID: confidentialMPTIDString(id),
		Holder:            state.EncodeAccountIDSafe(holder),
		MPTAmount:         75,
		ZKProof:           hex.EncodeToString(proof),
	}
	transaction.Sequence = &sequence
	if err := transaction.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := transaction.Preclaim(view, tx.EngineConfig{}); got != ter.TesSUCCESS {
		t.Fatalf("Preclaim with locks and auth = %v", got)
	}
	if got := transaction.Apply(nativeApplyContext(view, issuer, issuerRoot)); got != ter.TesSUCCESS {
		t.Fatalf("Apply = %v", got)
	}

	updatedHolding, err := state.ParseMPToken(view.entries[holdingKey.Key])
	if err != nil {
		t.Fatal(err)
	}
	if updatedHolding.MPTAmount != 13 || updatedHolding.ConfidentialBalanceVersion != 100 {
		t.Fatalf("holder public amount/version = %d/%d", updatedHolding.MPTAmount, updatedHolding.ConfidentialBalanceVersion)
	}
	for name, ciphertext := range map[string][]byte{
		"inbox":    updatedHolding.ConfidentialBalanceInbox,
		"spending": updatedHolding.ConfidentialBalanceSpending,
	} {
		want := mustNativeZero(t, holderPublic, holder, id)
		if string(ciphertext) != string(want) {
			t.Fatalf("%s is not the canonical holder zero", name)
		}
	}
	if want := mustNativeZero(t, issuerPublic, holder, id); string(updatedHolding.IssuerEncryptedBalance) != string(want) {
		t.Fatal("issuer mirror is not the canonical issuer zero")
	}
	if want := mustNativeZero(t, auditorPublic, holder, id); string(updatedHolding.AuditorEncryptedBalance) != string(want) {
		t.Fatal("auditor mirror is not the canonical auditor zero")
	}
	updatedIssuance, err := state.ParseMPTokenIssuance(view.entries[issuanceKey.Key])
	if err != nil {
		t.Fatal(err)
	}
	if updatedIssuance.OutstandingAmount != 0 || updatedIssuance.ConfidentialOutstandingAmount != 0 {
		t.Fatalf("issuance amounts = %d/%d", updatedIssuance.OutstandingAmount, updatedIssuance.ConfidentialOutstandingAmount)
	}
}

type nativeClawbackFixture struct {
	transaction *ConfidentialMPTClawback
	view        *confidentialPreclaimView
	issuer      [20]byte
	holder      [20]byte
	id          [24]byte
	issuanceKey keylet.Keylet
	holdingKey  keylet.Keylet
	issuance    *state.MPTokenIssuanceData
	holding     *state.MPTokenData
}

func newNativeClawbackFixture(t *testing.T) *nativeClawbackFixture {
	t.Helper()
	issuer := [20]byte{1}
	holder := [20]byte{2}
	id := keylet.MakeMPTID(4, issuer)
	private, issuerPublic := mustNativeKeyPair(t)
	_, holderPublic := mustNativeKeyPair(t)
	blind := mustNativeBlind(t)
	issuerBalance := mustNativeEncrypt(t, 75, issuerPublic, blind)
	issuance := &state.MPTokenIssuanceData{
		Issuer: issuer, Sequence: 4,
		Flags:                         entry.LsfMPTCanClawback | entry.LsfMPTCanHoldConfidentialBalance,
		OutstandingAmount:             75,
		ConfidentialOutstandingAmount: 75,
		IssuerEncryptionKey:           issuerPublic,
	}
	holding := &state.MPTokenData{
		Account:                     holder,
		MPTokenIssuanceID:           id,
		HolderEncryptionKey:         holderPublic,
		ConfidentialBalanceInbox:    mustNativeEncrypt(t, 50, holderPublic, blind),
		ConfidentialBalanceSpending: mustNativeEncrypt(t, 25, holderPublic, blind),
		IssuerEncryptedBalance:      issuerBalance,
	}
	view := &confidentialPreclaimView{entries: make(map[[32]byte][]byte), rules: amendment.NewRulesBuilder().Enable(amendment.FeatureConfidentialTransfer).Build()}
	putConfidentialAccount(t, view, issuer)
	putConfidentialAccount(t, view, holder)
	issuanceKey := keylet.MPTIssuance(id)
	holdingKey := keylet.MPToken(issuanceKey.Key, holder)
	view.entries[issuanceKey.Key] = mustSerializeConfidentialIssuance(t, issuance)
	view.entries[holdingKey.Key] = mustSerializeConfidentialHolding(t, holding)
	sequence := uint32(9)
	proofContext, ok := mptcrypto.ClawbackContext(issuer, id, sequence, holder)
	if !ok {
		t.Fatal("clawback context")
	}
	proof, ok := mptcrypto.GenerateClawbackProof(private, issuerPublic, proofContext, 75, issuerBalance)
	if !ok {
		t.Fatal("clawback proof")
	}
	transaction := &ConfidentialMPTClawback{
		BaseTx:            *tx.NewBaseTx(tx.TypeConfidentialMPTClawback, state.EncodeAccountIDSafe(issuer)),
		MPTokenIssuanceID: confidentialMPTIDString(id),
		Holder:            state.EncodeAccountIDSafe(holder),
		MPTAmount:         75,
		ZKProof:           hex.EncodeToString(proof),
	}
	transaction.Sequence = &sequence
	return &nativeClawbackFixture{transaction: transaction, view: view, issuer: issuer, holder: holder, id: id, issuanceKey: issuanceKey, holdingKey: holdingKey, issuance: issuance, holding: holding}
}

func TestConfidentialMPTClawbackNativePreclaimFailures(t *testing.T) {
	tests := []struct {
		name string
		want ter.Result
		edit func(*testing.T, *nativeClawbackFixture)
	}{
		{name: "missing issuer account", want: ter.TerNO_ACCOUNT, edit: func(_ *testing.T, f *nativeClawbackFixture) { delete(f.view.entries, keylet.Account(f.issuer).Key) }},
		{name: "missing holder account", want: ter.TecNO_TARGET, edit: func(_ *testing.T, f *nativeClawbackFixture) { delete(f.view.entries, keylet.Account(f.holder).Key) }},
		{name: "missing issuance", want: ter.TecOBJECT_NOT_FOUND, edit: func(_ *testing.T, f *nativeClawbackFixture) { delete(f.view.entries, f.issuanceKey.Key) }},
		{name: "missing clawback flag", want: ter.TecNO_PERMISSION, edit: func(t *testing.T, f *nativeClawbackFixture) {
			f.issuance.Flags &^= entry.LsfMPTCanClawback
			f.view.entries[f.issuanceKey.Key] = mustSerializeConfidentialIssuance(t, f.issuance)
		}},
		{name: "missing confidential flag", want: ter.TecNO_PERMISSION, edit: func(t *testing.T, f *nativeClawbackFixture) {
			f.issuance.Flags &^= entry.LsfMPTCanHoldConfidentialBalance
			f.view.entries[f.issuanceKey.Key] = mustSerializeConfidentialIssuance(t, f.issuance)
		}},
		{name: "missing issuer key", want: ter.TecNO_PERMISSION, edit: func(t *testing.T, f *nativeClawbackFixture) {
			f.issuance.IssuerEncryptionKey = nil
			f.view.entries[f.issuanceKey.Key] = mustSerializeConfidentialIssuance(t, f.issuance)
		}},
		{name: "malformed issuer key", want: ter.TecINTERNAL, edit: func(t *testing.T, f *nativeClawbackFixture) {
			f.issuance.IssuerEncryptionKey = []byte{2}
			f.view.entries[f.issuanceKey.Key] = mustSerializeConfidentialIssuance(t, f.issuance)
		}},
		{name: "missing holding", want: ter.TecOBJECT_NOT_FOUND, edit: func(_ *testing.T, f *nativeClawbackFixture) { delete(f.view.entries, f.holdingKey.Key) }},
		{name: "missing confidential holding fields", want: ter.TecNO_PERMISSION, edit: func(t *testing.T, f *nativeClawbackFixture) {
			f.holding.IssuerEncryptedBalance = nil
			f.view.entries[f.holdingKey.Key] = mustSerializeConfidentialHolding(t, f.holding)
		}},
		{name: "malformed issuer ciphertext", want: ter.TecINTERNAL, edit: func(t *testing.T, f *nativeClawbackFixture) {
			f.holding.IssuerEncryptedBalance = []byte{2}
			f.view.entries[f.holdingKey.Key] = mustSerializeConfidentialHolding(t, f.holding)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newNativeClawbackFixture(t)
			test.edit(t, fixture)
			if got := fixture.transaction.Preclaim(fixture.view, tx.EngineConfig{}); got != test.want {
				t.Fatalf("Preclaim = %v, want %v", got, test.want)
			}
		})
	}
}

func TestConfidentialMPTClawbackCommitFailureRollsBack(t *testing.T) {
	fixture := newNativeClawbackFixture(t)
	beforeIssuance := append([]byte(nil), fixture.view.entries[fixture.issuanceKey.Key]...)
	beforeHolding := append([]byte(nil), fixture.view.entries[fixture.holdingKey.Key]...)
	issuerRoot, err := state.ParseAccountRoot(fixture.view.entries[keylet.Account(fixture.issuer).Key])
	if err != nil {
		t.Fatal(err)
	}
	table := applystate.NewApplyStateTable(fixture.view, [32]byte{1}, 2, fixture.view.rules)
	ctx := &tx.ApplyContext{
		View: table, Account: issuerRoot, AccountID: fixture.issuer,
		Config: tx.EngineConfig{Rules: fixture.view.rules}, Metadata: &tx.Metadata{}, Log: xrpllog.Discard(), Ctx: context.Background(),
	}
	if got := fixture.transaction.Apply(ctx); got != ter.TesSUCCESS {
		t.Fatalf("Apply = %v", got)
	}
	fixture.view.failMutationAt = 2
	if _, err := table.Apply(); !errors.Is(err, errConfidentialMutation) {
		t.Fatalf("ApplyStateTable.Apply error = %v, want %v", err, errConfidentialMutation)
	}
	if string(fixture.view.entries[fixture.issuanceKey.Key]) != string(beforeIssuance) || string(fixture.view.entries[fixture.holdingKey.Key]) != string(beforeHolding) {
		t.Fatal("failed commit retained a partial confidential clawback update")
	}
}

func TestConfidentialMPTClawbackRequiresTheWholeEncryptedBalance(t *testing.T) {
	issuer := [20]byte{1}
	holder := [20]byte{2}
	id := keylet.MakeMPTID(4, issuer)
	private, public := mustNativeKeyPair(t)
	_, holderPublic := mustNativeKeyPair(t)
	ciphertext := mustNativeEncrypt(t, 75, public, mustNativeBlind(t))
	issuance := &state.MPTokenIssuanceData{Issuer: issuer, Sequence: 4, Flags: entry.LsfMPTCanClawback | entry.LsfMPTCanHoldConfidentialBalance, OutstandingAmount: 100, ConfidentialOutstandingAmount: 100, IssuerEncryptionKey: public}
	holding := &state.MPTokenData{Account: holder, MPTokenIssuanceID: id, HolderEncryptionKey: holderPublic, IssuerEncryptedBalance: ciphertext}
	view := &confidentialPreclaimView{entries: make(map[[32]byte][]byte), rules: amendment.NewRulesBuilder().Enable(amendment.FeatureConfidentialTransfer).Build()}
	putConfidentialAccount(t, view, issuer)
	putConfidentialAccount(t, view, holder)
	view.entries[keylet.MPTIssuance(id).Key] = mustSerializeConfidentialIssuance(t, issuance)
	view.entries[keylet.MPToken(keylet.MPTIssuance(id).Key, holder).Key] = mustSerializeConfidentialHolding(t, holding)
	sequence := uint32(9)
	proofContext, _ := mptcrypto.ClawbackContext(issuer, id, sequence, holder)
	proof, ok := mptcrypto.GenerateClawbackProof(private, public, proofContext, 75, ciphertext)
	if !ok {
		t.Fatal("generate full-balance proof")
	}
	transaction := &ConfidentialMPTClawback{BaseTx: *tx.NewBaseTx(tx.TypeConfidentialMPTClawback, state.EncodeAccountIDSafe(issuer)), MPTokenIssuanceID: confidentialMPTIDString(id), Holder: state.EncodeAccountIDSafe(holder), MPTAmount: 50, ZKProof: hex.EncodeToString(proof)}
	transaction.Sequence = &sequence
	if got := transaction.Preclaim(view, tx.EngineConfig{}); got != ter.TecBAD_PROOF {
		t.Fatalf("Preclaim partial amount = %v, want %v", got, ter.TecBAD_PROOF)
	}
	issuance.ConfidentialOutstandingAmount = 49
	view.entries[keylet.MPTIssuance(id).Key] = mustSerializeConfidentialIssuance(t, issuance)
	if got := transaction.Preclaim(view, tx.EngineConfig{}); got != ter.TecINSUFFICIENT_FUNDS {
		t.Fatalf("Preclaim insufficient global amount = %v, want %v", got, ter.TecINSUFFICIENT_FUNDS)
	}
}

func TestConfidentialMPTClawbackAuditorMismatchDoesNotWrite(t *testing.T) {
	issuer := [20]byte{1}
	holder := [20]byte{2}
	id := keylet.MakeMPTID(4, issuer)
	_, issuerPublic := mustNativeKeyPair(t)
	_, holderPublic := mustNativeKeyPair(t)
	_, auditorPublic := mustNativeKeyPair(t)
	issuance := &state.MPTokenIssuanceData{Issuer: issuer, Sequence: 4, Flags: entry.LsfMPTCanClawback | entry.LsfMPTCanHoldConfidentialBalance, OutstandingAmount: 1, ConfidentialOutstandingAmount: 1, IssuerEncryptionKey: issuerPublic}
	holding := &state.MPTokenData{
		Account:                     holder,
		MPTokenIssuanceID:           id,
		MPTAmount:                   7,
		HolderEncryptionKey:         holderPublic,
		ConfidentialBalanceInbox:    mustNativeZero(t, holderPublic, holder, id),
		ConfidentialBalanceSpending: mustNativeZero(t, holderPublic, holder, id),
		IssuerEncryptedBalance:      mustNativeZero(t, issuerPublic, holder, id),
		AuditorEncryptedBalance:     mustNativeZero(t, auditorPublic, holder, id),
	}
	view := &confidentialPreclaimView{entries: make(map[[32]byte][]byte), rules: amendment.NewRulesBuilder().Enable(amendment.FeatureConfidentialTransfer).Build()}
	issuerRoot := putConfidentialAccount(t, view, issuer)
	issuanceKey := keylet.MPTIssuance(id)
	holdingKey := keylet.MPToken(issuanceKey.Key, holder)
	view.entries[issuanceKey.Key] = mustSerializeConfidentialIssuance(t, issuance)
	view.entries[holdingKey.Key] = mustSerializeConfidentialHolding(t, holding)
	beforeIssuance := append([]byte(nil), view.entries[issuanceKey.Key]...)
	beforeHolding := append([]byte(nil), view.entries[holdingKey.Key]...)
	transaction := &ConfidentialMPTClawback{BaseTx: *tx.NewBaseTx(tx.TypeConfidentialMPTClawback, state.EncodeAccountIDSafe(issuer)), MPTokenIssuanceID: confidentialMPTIDString(id), Holder: state.EncodeAccountIDSafe(holder), MPTAmount: 1, ZKProof: makeHex(mptcrypto.ClawbackProofSize, 1)}
	if got := transaction.Apply(nativeApplyContext(view, issuer, issuerRoot)); got != ter.TecINTERNAL {
		t.Fatalf("Apply auditor mismatch = %v, want %v", got, ter.TecINTERNAL)
	}
	if string(view.entries[issuanceKey.Key]) != string(beforeIssuance) || string(view.entries[holdingKey.Key]) != string(beforeHolding) {
		t.Fatal("auditor mismatch wrote partial state")
	}
}
