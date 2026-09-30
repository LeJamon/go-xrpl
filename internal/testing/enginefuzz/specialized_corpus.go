package enginefuzz

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	ammtest "github.com/LeJamon/go-xrpl/internal/testing/amm"
	batchtest "github.com/LeJamon/go-xrpl/internal/testing/batch"
	escrowtest "github.com/LeJamon/go-xrpl/internal/testing/escrow"
	mpttest "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	nfttest "github.com/LeJamon/go-xrpl/internal/testing/nft"
	"github.com/LeJamon/go-xrpl/internal/testing/offer"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	permissioneddex "github.com/LeJamon/go-xrpl/internal/testing/permissioneddex"
	permissioneddomain "github.com/LeJamon/go-xrpl/internal/testing/permissioneddomain"
	"github.com/LeJamon/go-xrpl/internal/testing/trustset"
	"github.com/LeJamon/go-xrpl/internal/tx"
	accounttx "github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/LeJamon/go-xrpl/internal/tx/amm"
	"github.com/LeJamon/go-xrpl/internal/tx/batch"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	mpttx "github.com/LeJamon/go-xrpl/internal/tx/mpt"
	"github.com/LeJamon/go-xrpl/internal/tx/nftoken"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	txsign "github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	vaulttx "github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
)

type specializedReport struct {
	Name             string
	Profile          amendmentProfile
	Steps            int
	Applied          int
	TransactionCalls int
	InvariantChecks  int
	FeeClaims        int
	Closes           int
	PseudoApplied    int
}

type specializedState struct {
	t              testing.TB
	env            *jtx.TestEnv
	profile        amendmentProfile
	expectedSupply uint64
	ledgerDrops    uint64
	report         *specializedReport
}

type specializedTrace struct {
	Name    string
	Profile amendmentProfile
	Run     func(testing.TB) *specializedState
}

func newSpecializedState(t testing.TB, profile amendmentProfile, name string) *specializedState {
	t.Helper()
	env := jtx.NewTestEnv(t)
	applyAmendmentProfile(t, env, profile)
	return &specializedState{
		t:       t,
		env:     env,
		profile: profile,
		report:  &specializedReport{Name: name, Profile: profile},
	}
}

func (s *specializedState) establishBaseline() {
	s.t.Helper()
	total, err := totalLedgerXRP(s.env)
	if err != nil {
		s.t.Fatalf("specialized trace %s profile=%s: baseline XRP total: %v", s.report.Name, s.profile, err)
	}
	s.expectedSupply = total
	s.ledgerDrops = s.env.LastClosedLedger().TotalDrops()
}

func (s *specializedState) close() {
	s.t.Helper()
	s.env.Close()
	s.checkClosed()
}

func (s *specializedState) closeToParentCloseTime(target uint32) {
	s.t.Helper()
	s.env.CloseToParentCloseTime(target)
	s.checkClosed()
}

func (s *specializedState) checkClosed() {
	s.t.Helper()
	total, err := totalLedgerXRP(s.env)
	if err != nil {
		s.t.Fatalf("specialized trace %s profile=%s close %d: XRP total: %v", s.report.Name, s.profile, s.report.Closes, err)
	}
	if total != s.expectedSupply {
		s.t.Fatalf("specialized trace %s profile=%s close %d: XRP total=%d expected=%d", s.report.Name, s.profile, s.report.Closes, total, s.expectedSupply)
	}
	if got := s.env.LastClosedLedger().TotalDrops(); got != s.ledgerDrops {
		s.t.Fatalf("specialized trace %s profile=%s close %d: ledger drops=%d expected=%d", s.report.Name, s.profile, s.report.Closes, got, s.ledgerDrops)
	}
	if _, err := s.env.LastClosedLedger().StateMapHash(); err != nil {
		s.t.Fatalf("specialized trace %s profile=%s close %d: state hash: %v", s.report.Name, s.profile, s.report.Closes, err)
	}
	s.report.Closes++
}

type specializedSubmitOptions struct {
	Want           ter.Result
	Applied        bool
	SequenceStep   int
	AllowRemoved   bool
	HeaderFeeClaim bool
}

func (s *specializedState) submit(payer *jtx.Account, txn tx.Transaction, opts specializedSubmitOptions) jtx.TxResult {
	s.t.Helper()
	beforeHash, err := s.env.Ledger().StateMapHash()
	if err != nil {
		s.t.Fatalf("specialized trace %s: state hash before step: %v", s.report.Name, err)
	}
	beforeTotal, err := totalLedgerXRP(s.env)
	if err != nil {
		s.t.Fatalf("specialized trace %s: XRP total before step: %v", s.report.Name, err)
	}
	beforeInfo := s.env.AccountInfo(payer)
	result := s.env.SubmitWithFlags(txn, tx.TapNONE)
	s.report.Steps++
	if err := classifySafetyOutcome(txn.TxType().String(), result, s.profile, s.report.Steps-1); err != nil {
		s.t.Fatal(err)
	}
	if result.Result != opts.Want {
		s.t.Fatalf("specialized trace %s: result=%s want=%s (%s)", s.report.Name, result.Code, opts.Want, result.Message)
	}
	if result.Applied != opts.Applied {
		s.t.Fatalf("specialized trace %s: result=%s applied=%t want=%t", s.report.Name, result.Code, result.Applied, opts.Applied)
	}
	afterHash, err := s.env.Ledger().StateMapHash()
	if err != nil {
		s.t.Fatalf("specialized trace %s: state hash after step: %v", s.report.Name, err)
	}
	afterTotal, err := totalLedgerXRP(s.env)
	if err != nil {
		s.t.Fatalf("specialized trace %s: XRP total after step: %v", s.report.Name, err)
	}
	afterInfo := s.env.AccountInfo(payer)
	if result.Applied {
		if beforeTotal < result.Fee || afterTotal != beforeTotal-result.Fee {
			s.t.Fatalf("specialized trace %s: XRP total=%d -> %d fee=%d", s.report.Name, beforeTotal, afterTotal, result.Fee)
		}
		if result.Metadata == nil || result.Metadata.TransactionResult != result.Result {
			s.t.Fatalf("specialized trace %s: applied result has inconsistent metadata", s.report.Name)
		}
		if !result.ApplyInvoked || !result.InvariantsChecked {
			s.t.Fatalf("specialized trace %s: applied result skipped apply/invariant phases: %+v", s.report.Name, result)
		}
		s.expectedSupply -= result.Fee
		if opts.HeaderFeeClaim {
			s.ledgerDrops -= result.Fee
		}
		s.report.Applied++
		if result.Result != ter.TesSUCCESS {
			s.report.FeeClaims++
		}
		if !opts.AllowRemoved && beforeInfo != nil && afterInfo != nil && opts.SequenceStep >= 0 && afterInfo.Sequence != beforeInfo.Sequence+uint32(opts.SequenceStep) {
			s.t.Fatalf("specialized trace %s: sequence=%d -> %d want +%d", s.report.Name, beforeInfo.Sequence, afterInfo.Sequence, opts.SequenceStep)
		}
		if opts.AllowRemoved && afterInfo != nil {
			s.t.Fatalf("specialized trace %s: deleted payer still exists", s.report.Name)
		}
	} else {
		if beforeHash != afterHash || beforeTotal != afterTotal {
			s.t.Fatalf("specialized trace %s: rejected result mutated state or XRP", s.report.Name)
		}
		if result.Fee != 0 || result.Metadata != nil {
			s.t.Fatalf("specialized trace %s: rejected result fee=%d metadata=%t", s.report.Name, result.Fee, result.Metadata != nil)
		}
		if beforeInfo != nil && afterInfo != nil && afterInfo.Sequence != beforeInfo.Sequence {
			s.t.Fatalf("specialized trace %s: rejected result changed sequence", s.report.Name)
		}
	}
	if result.TransactionCalls != 1 || result.InvariantChecks != 1 {
		s.t.Fatalf("specialized trace %s: observed engine phases calls=%d invariants=%d want=1/1", s.report.Name, result.TransactionCalls, result.InvariantChecks)
	}
	s.report.TransactionCalls += result.TransactionCalls
	s.report.InvariantChecks += result.InvariantChecks
	return result
}

func (s *specializedState) submitPseudo(txn tx.Transaction) jtx.TxResult {
	s.t.Helper()
	result := s.env.SubmitPseudo(txn)
	s.report.Steps++
	if err := classifySafetyOutcome(txn.TxType().String(), result, s.profile, s.report.Steps-1); err != nil {
		s.t.Fatal(err)
	}
	if !result.Applied || result.Result != ter.TesSUCCESS {
		s.t.Fatalf("specialized trace %s: pseudo result=%s applied=%t", s.report.Name, result.Code, result.Applied)
	}
	s.report.Applied++
	s.report.PseudoApplied++
	return result
}

func accountSetUp(t testing.TB, profile amendmentProfile, name string, names ...string) (*specializedState, []*jtx.Account) {
	t.Helper()
	s := newSpecializedState(t, profile, name)
	accounts := make([]*jtx.Account, len(names))
	for i, name := range names {
		accounts[i] = jtx.NewAccount(name)
		s.env.FundAmount(accounts[i], uint64(jtx.XRP(30_000)))
	}
	s.env.Close()
	return s, accounts
}

func specializedTraces() []specializedTrace {
	return []specializedTrace{
		{Name: "amm-create-deposit-pseudo-account", Profile: profileV341, Run: runSpecializedAMM},
		{Name: "mpt-issue-authorize-payment", Profile: profileV341, Run: runSpecializedMPT},
		{Name: "nft-mint-offer-accept", Profile: profileV341, Run: runSpecializedNFT},
		{Name: "permissioned-domain-lifecycle", Profile: profileV341, Run: runSpecializedPermissionedDomain},
		{Name: "permissioned-dex-domain-offer", Profile: profileV341, Run: runSpecializedPermissionedDEX},
		{Name: "account-delete-offer-cleanup", Profile: profileV341, Run: runSpecializedAccountDelete},
		{Name: "vault-deposit-withdraw-delete-pseudo-account", Profile: profileV341, Run: runSpecializedVault},
		{Name: "loan-broker-cover-lifecycle", Profile: profileV341, Run: runSpecializedLoanBroker},
		{Name: "escrow-create-cancel", Profile: profileV341, Run: runSpecializedEscrow},
		{Name: "batch-inner-payments", Profile: profileV341, Run: runSpecializedBatch},
		{Name: "pseudo-amendment-effect", Profile: profileV341AMMDisabled, Run: runSpecializedPseudo},
	}
}

type specializedInput struct {
	Family  uint8
	Profile amendmentProfile
	Amount  uint64
}

type specializedSeed struct {
	Name  string
	Input specializedInput
}

func specializedSeedCorpus() []specializedSeed {
	traces := specializedTraces()
	seeds := make([]specializedSeed, len(traces))
	for i, trace := range traces {
		seeds[i] = specializedSeed{
			Name: trace.Name,
			Input: specializedInput{
				Family:  uint8(i),
				Profile: trace.Profile,
				Amount:  100 + uint64(i*37),
			},
		}
	}
	return seeds
}

func decodeSpecializedInput(data []byte) specializedInput {
	input := specializedInput{Profile: profileV341, Amount: 100}
	if len(data) == 0 {
		return input
	}
	traceCount := len(specializedTraces())
	input.Family = data[0] % uint8(traceCount)
	if input.Family == uint8(traceCount-1) {
		input.Profile = profileV341AMMDisabled
	}
	if len(data) >= 4 {
		input.Amount = 1 + uint64(binary.BigEndian.Uint16(data[2:4]))
	} else if len(data) > 1 {
		input.Amount = 1 + uint64(data[1])
	}
	return input
}

func encodeSpecializedInput(input specializedInput) []byte {
	data := make([]byte, 4)
	data[0] = input.Family % uint8(len(specializedTraces()))
	if input.Profile == profileV341AMMDisabled || data[0] == uint8(len(specializedTraces())-1) {
		data[1] = 1
	}
	amount := input.Amount
	if amount == 0 {
		amount = 1
	}
	binary.BigEndian.PutUint16(data[2:4], uint16(amount-1))
	return data
}

func runSpecializedVariant(t testing.TB, input specializedInput) *specializedState {
	amounts := [...]uint64{100, 1000, 10_000}
	amount := amounts[input.Amount%uint64(len(amounts))]
	switch input.Family {
	case 0:
		return runSpecializedAMMWithAmount(t, amount)
	case 1:
		return runSpecializedMPTWithAmount(t, amount)
	case 2:
		return runSpecializedNFTWithAmount(t, amount)
	case 9:
		return runSpecializedBatchWithAmount(t, amount)
	case 10:
		return runSpecializedPseudoWithProfile(t, input.Profile)
	default:
		return specializedTraces()[input.Family].Run(t)
	}
}

func runSpecializedAMM(t testing.TB) *specializedState {
	return runSpecializedAMMWithAmount(t, 1000)
}

func runSpecializedAMMWithAmount(t testing.TB, amount uint64) *specializedState {
	s, accounts := accountSetUp(t, profileV341, "amm-create-deposit-pseudo-account", "amm-gw", "amm-alice", "amm-bob")
	gw, alice, bob := accounts[0], accounts[1], accounts[2]
	for _, holder := range []*jtx.Account{alice, bob} {
		jtx.RequireTxSuccess(t, s.env.Submit(trustset.TrustLine(holder, "USD", gw, "100000").Build()))
	}
	s.env.Close()
	for _, holder := range []*jtx.Account{alice, bob} {
		s.env.PayIOU(gw, holder, gw, "USD", 10_000)
	}
	s.env.Close()
	s.establishBaseline()
	asset := tx.Asset{Currency: "USD", Issuer: gw.Address}
	s.submit(alice, ammtest.AMMCreate(alice, ammtest.XRPAmount(int64(amount)), ammtest.IOUAmount(gw, "USD", float64(amount))).Build(), specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	key := amm.ComputeAMMKeylet(tx.Asset{Currency: "XRP"}, asset)
	if !s.env.LedgerEntryExists(key) {
		t.Fatalf("AMM trace did not create the AMM ledger object")
	}
	depositAmount := amount / 10
	if depositAmount == 0 {
		depositAmount = 1
	}
	s.submit(bob, ammtest.AMMDeposit(bob, ammtest.XRP(), asset).
		Amount(ammtest.XRPAmount(int64(depositAmount))).
		Amount2(ammtest.IOUAmount(gw, "USD", float64(depositAmount))).
		TwoAsset().Build(), specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	return s
}

func runSpecializedMPT(t testing.TB) *specializedState {
	return runSpecializedMPTWithAmount(t, 100)
}

func runSpecializedMPTWithAmount(t testing.TB, amount uint64) *specializedState {
	s, accounts := accountSetUp(t, profileV341, "mpt-issue-authorize-payment", "mpt-issuer", "mpt-holder")
	issuer, holder := accounts[0], accounts[1]
	s.establishBaseline()
	create := mpttx.NewMPTokenIssuanceCreate(issuer.Address)
	create.Fee = "10"
	create.Flags = ptrUint32(mpttest.TfMPTCanTransfer | mpttest.TfMPTRequireAuth)
	s.submit(issuer, create, specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	issuanceID := mpttest.MakeMPTIDHexFromAddr(create.GetCommon().SeqProxy(), issuer.Address)
	s.submit(holder, mpttx.NewMPTokenAuthorize(holder.Address, issuanceID), specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	authorize := mpttx.NewMPTokenAuthorize(issuer.Address, issuanceID)
	authorize.Fee = "10"
	authorize.Holder = holder.Address
	s.submit(issuer, authorize, specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	mptAmount := state.NewMPTAmountWithIssuanceID(int64(amount), issuer.Address, issuanceID)
	s.submit(issuer, payment.PayIssued(issuer, holder, mptAmount).Build(), specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	return s
}

func runSpecializedNFT(t testing.TB) *specializedState {
	return runSpecializedNFTWithAmount(t, 100)
}

func runSpecializedNFTWithAmount(t testing.TB, amount uint64) *specializedState {
	s, accounts := accountSetUp(t, profileV341, "nft-mint-offer-accept", "nft-issuer", "nft-buyer")
	issuer, buyer := accounts[0], accounts[1]
	s.establishBaseline()
	flags := nftoken.NFTokenFlagTransferable
	tokenID := nfttest.GetNextNFTokenID(s.env, issuer, 0, flags, 0)
	s.submit(issuer, nfttest.NFTokenMint(issuer, 0).Transferable().Build(), specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	offerID := nfttest.GetOfferIndex(s.env, issuer)
	s.submit(issuer, nfttest.NFTokenCreateSellOffer(issuer, tokenID, tx.NewXRPAmount(int64(amount))).Build(), specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	s.submit(buyer, nfttest.NFTokenAcceptSellOffer(buyer, offerID).Build(), specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	return s
}

func runSpecializedPermissionedDomain(t testing.TB) *specializedState {
	s, accounts := accountSetUp(t, profileV341, "permissioned-domain-lifecycle", "domain-owner", "domain-issuer")
	owner, issuer := accounts[0], accounts[1]
	s.establishBaseline()
	sequence := s.env.Seq(owner)
	credentialType := hex.EncodeToString([]byte("permdomain"))
	s.submit(owner, permissioneddomain.DomainSet(owner).Credential(issuer, credentialType).Build(), specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	domainKey := keylet.PermissionedDomain(owner.ID, sequence)
	domainID := hex.EncodeToString(domainKey.Key[:])
	s.submit(owner, permissioneddomain.DomainSet(owner).DomainID(domainID).Credential(issuer, credentialType+"01").Build(), specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	s.submit(owner, permissioneddomain.DomainDelete(owner, domainID).Build(), specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	return s
}

func runSpecializedPermissionedDEX(t testing.TB) *specializedState {
	s := newSpecializedState(t, profileV341, "permissioned-dex-domain-offer")
	dex := permissioneddex.SetupPermissionedDEX(t.(*testing.T), s.env)
	s.establishBaseline()
	s.submit(dex.Bob, offer.OfferCreate(dex.Bob, jtx.XRPTxAmount(10_000_000), dex.USD(10)).DomainID(dex.DomainID).Build(), specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	return s
}

func runSpecializedAccountDelete(t testing.TB) *specializedState {
	s, accounts := accountSetUp(t, profileV341, "account-delete-offer-cleanup", "delete-source", "delete-destination", "delete-gateway")
	source, destination, gateway := accounts[0], accounts[1], accounts[2]
	s.establishBaseline()
	offerSequence := s.env.Seq(source)
	s.submit(source, offer.OfferCreate(source, jtx.USD(gateway, 1), jtx.XRPTxAmount(1_000_000)).Build(), specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	offerKey := keylet.Offer(source.ID, offerSequence)
	if !s.env.LedgerEntryExists(offerKey) {
		t.Fatalf("AccountDelete trace did not create an offer")
	}
	s.env.IncLedgerSeqForAccDel(source)
	deleteTx := accounttx.NewAccountDelete(source.Address, destination.Address)
	deleteTx.Fee = fmt.Sprintf("%d", s.env.ReserveIncrement())
	s.submit(source, deleteTx, specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, AllowRemoved: true})
	s.close()
	if s.env.Exists(source) || s.env.LedgerEntryExists(offerKey) {
		t.Fatalf("AccountDelete trace retained source or offer")
	}
	return s
}

func runSpecializedVault(t testing.TB) *specializedState {
	s, accounts := accountSetUp(t, profileV341, "vault-deposit-withdraw-delete-pseudo-account", "vault-owner", "vault-depositor")
	owner, depositor := accounts[0], accounts[1]
	s.establishBaseline()
	sequence := s.env.Seq(owner)
	create := vaulttx.NewVaultCreate(owner.Address, tx.Asset{Currency: "XRP"})
	create.Fee = "50000000"
	s.submit(owner, create, specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	vaultKey := keylet.Vault(owner.AccountID(), sequence)
	vaultID := strings.ToUpper(hex.EncodeToString(vaultKey.Key[:]))
	if !s.env.VaultExists(vaultID) {
		t.Fatalf("Vault trace did not create a vault")
	}
	s.submit(depositor, vaulttx.NewVaultDeposit(depositor.Address, vaultID, tx.NewXRPAmount(100_000_000)), specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	s.submit(depositor, vaulttx.NewVaultWithdraw(depositor.Address, vaultID, tx.NewXRPAmount(100_000_000)), specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	s.submit(owner, vaulttx.NewVaultDelete(owner.Address, vaultID), specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	if s.env.VaultExists(vaultID) {
		t.Fatalf("Vault trace retained deleted vault")
	}
	return s
}

func runSpecializedLoanBroker(t testing.TB) *specializedState {
	s, accounts := accountSetUp(t, profileV341, "loan-broker-cover-lifecycle", "loan-owner", "loan-borrower")
	owner, borrower := accounts[0], accounts[1]
	s.establishBaseline()
	vaultSequence := s.env.Seq(owner)
	create := vaulttx.NewVaultCreate(owner.Address, tx.Asset{Currency: "XRP"})
	create.Fee = "50000000"
	kind := vaulttx.VaultKindClosedEnded
	subscription := s.env.NowRipple() + 60
	redemption := subscription + 100_000
	create.VaultKind = &kind
	create.SubscriptionDate = &subscription
	create.RedemptionDate = &redemption
	s.submit(owner, create, specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	vaultKey := keylet.Vault(owner.AccountID(), vaultSequence)
	vaultID := strings.ToUpper(hex.EncodeToString(vaultKey.Key[:]))
	s.submit(owner, vaulttx.NewVaultDeposit(owner.Address, vaultID, tx.NewXRPAmount(2_000_000_000)), specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	s.closeToParentCloseTime(subscription + 1)
	brokerSequence := s.env.Seq(owner)
	s.submit(owner, lending.NewLoanBrokerSet(owner.Address, vaultID), specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	brokerKey := keylet.LoanBroker(owner.AccountID(), brokerSequence)
	brokerID := strings.ToUpper(hex.EncodeToString(brokerKey.Key[:]))
	loanSet := lending.NewLoanSet(borrower.Address, brokerID, "1000")
	interval := uint32(60)
	payments := uint32(2)
	loanSet.PaymentInterval = &interval
	loanSet.PaymentTotal = &payments
	loanSet.Counterparty = owner.Address
	loanSet.GetCommon().Fee = fmt.Sprintf("%d", 2*s.env.BaseFee())
	loanSet.GetCommon().SigningPubKey = strings.ToUpper(borrower.PublicKeyHex())
	counterpartySignature, err := txsign.SignCounterpartyWithRules(
		loanSet,
		strings.ToUpper(owner.PublicKeyHex()),
		"00"+strings.ToUpper(owner.PrivateKeyHex()),
		s.env.Rules(),
	)
	if err != nil {
		t.Fatalf("loan counterparty signature: %v", err)
	}
	loanSet.GetCommon().CounterpartySignature = counterpartySignature
	s.submit(borrower, loanSet, specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	loanKey := keylet.Loan(brokerKey.Key, 1)
	loanID := strings.ToUpper(hex.EncodeToString(loanKey.Key[:]))
	s.submit(owner, lending.NewLoanManage(owner.Address, loanID), specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	s.submit(borrower, lending.NewLoanPay(borrower.Address, loanID, tx.NewXRPAmount(500)), specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	s.submit(borrower, lending.NewLoanPay(borrower.Address, loanID, tx.NewXRPAmount(500)), specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	s.submit(owner, lending.NewLoanDelete(owner.Address, loanID), specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	s.submit(owner, lending.NewLoanBrokerCoverDeposit(owner.Address, brokerID, tx.NewXRPAmount(500_000_000)), specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	s.submit(owner, lending.NewLoanBrokerCoverWithdraw(owner.Address, brokerID, tx.NewXRPAmount(200_000_000)), specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	s.submit(owner, lending.NewLoanBrokerDelete(owner.Address, brokerID), specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	return s
}

func runSpecializedEscrow(t testing.TB) *specializedState {
	s, accounts := accountSetUp(t, profileV341, "escrow-create-cancel", "escrow-owner", "escrow-destination")
	owner, destination := accounts[0], accounts[1]
	s.establishBaseline()
	offerSequence := s.env.Seq(owner)
	cancelAfter := s.env.NowRipple() + 100
	finishAfter := cancelAfter - 10
	s.submit(owner, escrowtest.EscrowCreate(owner, destination, jtx.XRP(100)).FinishAfter(finishAfter).CancelAfter(cancelAfter).Build(), specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	s.env.SetTime(protocol.FromRippleTime(cancelAfter - 10))
	s.close()
	s.env.SetTime(protocol.FromRippleTime(cancelAfter))
	s.close()
	s.submit(destination, escrowtest.EscrowCancel(destination, owner, offerSequence).Build(), specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: 1})
	s.close()
	return s
}

func runSpecializedBatch(t testing.TB) *specializedState {
	return runSpecializedBatchWithAmount(t, 100)
}

func runSpecializedBatchWithAmount(t testing.TB, amount uint64) *specializedState {
	s, accounts := accountSetUp(t, profileV341, "batch-inner-payments", "batch-sender", "batch-recipient")
	sender, recipient := accounts[0], accounts[1]
	s.establishBaseline()
	beforeRecipient := s.env.Balance(recipient)
	sequence := s.env.Seq(sender)
	fee := batchtest.CalcBatchFeeFromEnv(s.env, 0, 2)
	innerAmount1 := 1 + amount%100
	innerAmount2 := 2 + amount%100
	inner1 := batchtest.MakeInnerPaymentXRP(sender, recipient, int64(innerAmount1), sequence+1)
	inner2 := batchtest.MakeInnerPaymentXRP(sender, recipient, int64(innerAmount2), sequence+2)
	outer := batchtest.NewBatchBuilder(sender, sequence, fee, batch.BatchFlagAllOrNothing).
		AddInnerTx(inner1).
		AddInnerTx(inner2).
		MustBuild()
	result := s.submit(sender, outer, specializedSubmitOptions{Want: ter.TesSUCCESS, Applied: true, SequenceStep: -1, HeaderFeeClaim: true})
	if result.TransactionCalls != 1 || result.InvariantChecks != 1 {
		t.Fatalf("Batch trace outer engine phases calls=%d invariants=%d want=1/1", result.TransactionCalls, result.InvariantChecks)
	}
	var replayCalls, replayInvariants int
	s.env.SetApplyObserverForTest(func(phase txengine.ApplyPhase) {
		switch phase {
		case txengine.ApplyPhaseTransaction:
			replayCalls++
		case txengine.ApplyPhaseInvariants:
			replayInvariants++
		}
	})
	s.close()
	s.env.SetApplyObserverForTest(nil)
	if replayCalls != 3 || replayInvariants != 3 {
		t.Fatalf("Batch trace closed engine phases calls=%d invariants=%d want=3/3", replayCalls, replayInvariants)
	}
	s.report.TransactionCalls += replayCalls
	s.report.InvariantChecks += replayInvariants
	if got := s.env.Seq(sender); got != sequence+3 {
		t.Fatalf("Batch trace sender sequence=%d want=%d", got, sequence+3)
	}
	if got, want := s.env.Balance(recipient), beforeRecipient+uint64(jtx.XRP(int64(innerAmount1+innerAmount2))); got != want {
		t.Fatalf("Batch trace recipient balance=%d want=%d", got, want)
	}
	outerHash, err := tx.ComputeTransactionHash(outer)
	if err != nil {
		t.Fatalf("Batch trace outer hash: %v", err)
	}
	closed := s.env.LastClosedLedger()
	if _, found, err := closed.GetTransaction(outerHash); err != nil || !found {
		t.Fatalf("Batch trace closed outer transaction found=%t err=%v", found, err)
	}
	for i, inner := range []tx.Transaction{inner1, inner2} {
		innerHash, err := tx.ComputeTransactionHash(inner)
		if err != nil {
			t.Fatalf("Batch trace inner %d hash: %v", i, err)
		}
		stored, found, err := closed.GetTransaction(innerHash)
		if err != nil || !found {
			t.Fatalf("Batch trace inner %d found=%t err=%v", i, found, err)
		}
		_, metadata, err := tx.SplitTxWithMetaBlobStrict(stored)
		if err != nil {
			t.Fatalf("Batch trace inner %d metadata: %v", i, err)
		}
		decoded, err := binarycodec.DecodeBytes(metadata)
		if err != nil || decoded["TransactionResult"] != ter.TesSUCCESS.String() {
			t.Fatalf("Batch trace inner %d result=%v err=%v", i, decoded["TransactionResult"], err)
		}
	}
	return s
}

func runSpecializedPseudo(t testing.TB) *specializedState {
	return runSpecializedPseudoWithProfile(t, profileV341AMMDisabled)
}

func runSpecializedPseudoWithProfile(t testing.TB, profile amendmentProfile) *specializedState {
	s, accounts := accountSetUp(t, profile, "pseudo-amendment-effect", "pseudo-account")
	_ = accounts
	var amendmentHash [32]byte
	copy(amendmentHash[:], amendment.FeatureAMM[:])
	pseudoTx := &pseudo.EnableAmendment{BaseTx: *tx.NewBaseTx(tx.TypeAmendment, protocol.ZeroAccount)}
	pseudoTx.Amendment = strings.ToUpper(hex.EncodeToString(amendmentHash[:]))
	pseudoTx.Fee = "0"
	zero := uint32(0)
	pseudoTx.Sequence = &zero
	s.establishBaseline()
	s.submitPseudo(pseudoTx)
	s.close()
	data, err := s.env.LedgerEntry(keylet.Amendments())
	if err != nil {
		t.Fatalf("pseudo trace amendments entry: %v", err)
	}
	amendments, err := pseudo.ParseAmendmentsSLE(data)
	if err != nil {
		t.Fatalf("pseudo trace parse amendments entry: %v", err)
	}
	seen := false
	for _, enabled := range amendments.Amendments {
		if enabled == amendment.FeatureAMM {
			seen = true
			break
		}
	}
	if !seen {
		t.Fatalf("pseudo trace did not record AMM amendment")
	}
	return s
}

func ptrUint32(value uint32) *uint32 { return &value }
