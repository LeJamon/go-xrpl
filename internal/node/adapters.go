package node

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	consensusadaptor "github.com/LeJamon/go-xrpl/internal/consensus/adaptor"
	"github.com/LeJamon/go-xrpl/internal/ledger/cleaner"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/internal/rpc"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/txq"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

type ledgerCleanerSource struct {
	svc    *service.Service
	family shamap.Family

	mu        sync.RWMutex
	reacquire func(context.Context, uint32) error
}

func (s *ledgerCleanerSource) AvailableRange() (uint32, uint32, bool) {
	return s.svc.AvailableLedgerRange()
}

func (s *ledgerCleanerSource) Ledger(ctx context.Context, seq uint32) (cleaner.LedgerData, bool, error) {
	l, err := s.svc.CleanerLedger(ctx, seq)
	if err != nil || l == nil {
		return cleaner.LedgerData{}, false, err
	}
	sr, err := l.StateMapHash()
	if err != nil {
		return cleaner.LedgerData{}, false, err
	}
	tr, err := l.TxMapHash()
	if err != nil {
		return cleaner.LedgerData{}, false, err
	}
	return cleaner.LedgerData{
		Sequence:   l.Sequence(),
		Hash:       l.Hash(),
		ParentHash: l.ParentHash(),
		StateRoot:  sr,
		TxRoot:     tr,
	}, true, nil
}

func (s *ledgerCleanerSource) CanonicalHash(
	ctx context.Context,
	seq uint32,
) ([32]byte, bool, error) {
	return s.svc.CanonicalLedgerHash(ctx, seq)
}

func (s *ledgerCleanerSource) RepairLedgerIndex(
	ctx context.Context,
	info cleaner.LedgerData,
) (bool, error) {
	return s.svc.RepairCleanerLedgerIndex(
		ctx, info.Sequence, info.Hash, info.ParentHash,
	)
}

func (s *ledgerCleanerSource) Family() shamap.Family { return s.family }

func (s *ledgerCleanerSource) SetReacquire(fn func(context.Context, uint32) error) {
	s.mu.Lock()
	s.reacquire = fn
	s.mu.Unlock()
}

func (s *ledgerCleanerSource) Reacquire(ctx context.Context, seq uint32) error {
	s.mu.RLock()
	fn := s.reacquire
	s.mu.RUnlock()
	if fn == nil {
		return errors.New("ledger_cleaner: ledger acquisition unavailable")
	}
	return fn(ctx, seq)
}

func (s *ledgerCleanerSource) RepairTransactions(ctx context.Context, seq uint32) error {
	return s.svc.RepairLedgerTransactions(ctx, seq)
}

// toCleanerStatus translates the cleaner package's status into the RPC-types
// mirror struct (see ServiceContainer.LedgerCleanerConfigure for the layering
// boundary).
func toCleanerStatus(s cleaner.Status) types.LedgerCleanerStatus {
	return types.LedgerCleanerStatus{
		State:          s.State,
		MinLedger:      s.MinLedger,
		MaxLedger:      s.MaxLedger,
		CheckNodes:     s.CheckNodes,
		FixTxns:        s.FixTxns,
		Failures:       s.Failures,
		LedgersChecked: s.LedgersChecked,
		NodesChecked:   s.NodesChecked,
		MissingNodes:   s.MissingNodes,
		LastError:      s.LastError,
	}
}

// ledgerInfoAdapter adapts the ledger service to the LedgerInfoProvider interface
type ledgerInfoAdapter struct {
	ledgerService *service.Service
}

func (a *ledgerInfoAdapter) GetCurrentLedgerInfo() *types.LedgerSubscribeInfo {
	if a.ledgerService == nil {
		return nil
	}

	validatedLedger := a.ledgerService.GetValidatedLedger()
	if validatedLedger == nil {
		return nil
	}

	baseFee, reserveBase, reserveInc := service.FeesFromLedger(validatedLedger)

	ledgerTime := protocol.ToRippleTime(validatedLedger.CloseTime())

	hash := validatedLedger.Hash()
	serverInfo := a.ledgerService.GetServerInfo()
	validatedLedgers := ""
	if serverPublishesValidatedRange(serverInfo.ServerState) && !serverInfo.NeedsNetworkLedger {
		validatedLedgers = serverInfo.CompleteLedgers
	}
	xrpFeesEnabled := validatedLedger.Rules() != nil && validatedLedger.Rules().XRPFeesEnabled()

	return &types.LedgerSubscribeInfo{
		LedgerIndex:      validatedLedger.Sequence(),
		LedgerHash:       upperHex(hash[:]),
		LedgerTime:       ledgerTime,
		FeeBase:          baseFee,
		FeeRef:           deprecatedFeeReferenceUnits,
		ReserveBase:      reserveBase,
		ReserveInc:       reserveInc,
		ValidatedLedgers: validatedLedgers,
		NetworkID:        serverInfo.NetworkID,
		XRPFeesEnabled:   xrpFeesEnabled,
	}
}

const deprecatedFeeReferenceUnits uint64 = 10

func serverPublishesValidatedRange(state string) bool {
	switch state {
	case "syncing", "tracking", "full", "proposing", "validating":
		return true
	default:
		return false
	}
}

func buildLedgerCloseEvent(event *service.LedgerAcceptedEvent, serverInfo service.ServerInfo) *rpc.LedgerCloseEvent {
	if event == nil || event.LedgerInfo == nil || event.Ledger == nil {
		return nil
	}
	baseFee, reserveBase, reserveInc := service.FeesFromLedger(event.Ledger)
	var feeRef *uint64
	if rules := event.Ledger.Rules(); rules == nil || !rules.XRPFeesEnabled() {
		value := deprecatedFeeReferenceUnits
		feeRef = &value
	}
	validatedLedgers := ""
	if serverPublishesValidatedRange(serverInfo.ServerState) {
		validatedLedgers = serverInfo.CompleteLedgers
	}
	return &rpc.LedgerCloseEvent{
		Type:             "ledgerClosed",
		LedgerIndex:      event.LedgerInfo.Sequence,
		LedgerHash:       upperHex(event.LedgerInfo.Hash[:]),
		LedgerTime:       protocol.ToRippleTime(event.LedgerInfo.CloseTime),
		FeeBase:          baseFee,
		FeeRef:           feeRef,
		NetworkID:        serverInfo.NetworkID,
		ReserveBase:      reserveBase,
		ReserveInc:       reserveInc,
		TxnCount:         len(event.TransactionResults),
		ValidatedLedgers: validatedLedgers,
	}
}

// upperHex renders bytes as uppercase hex
func upperHex(b []byte) string {
	return strings.ToUpper(hex.EncodeToString(b))
}

// queuedTxInfos projects the ledger service's TxQ candidate details into the
// RPC-layer view consumed by account_info and the ledger method's queue_data.
// The transaction body is flattened only for the ledger dump (which echoes it);
// account_info ignores TxJSON.
func queuedTxInfos(details []*txq.CandidateDetails) []types.QueuedTxInfo {
	if len(details) == 0 {
		return nil
	}
	out := make([]types.QueuedTxInfo, 0, len(details))
	for _, d := range details {
		info := types.QueuedTxInfo{
			Account:          d.Account,
			TxID:             d.TxID,
			SeqValue:         d.SeqProxy.Value,
			IsTicket:         d.SeqProxy.IsTicket,
			FeeLevel:         uint64(d.FeeLevel),
			LastValid:        d.LastValid,
			Fee:              d.Fee,
			MaxSpendDrops:    d.PotentialSpend + d.Fee,
			AuthChange:       d.AuthChange,
			RetriesRemaining: d.RetriesRemaining,
			PreflightResult:  d.PreflightResult.String(),
			LastResult:       d.LastResult.String(),
			HasLastResult:    d.HasLastResult,
		}
		if d.Txn != nil {
			if flat, err := d.Txn.Flatten(); err == nil {
				info.TxJSON = flat
			}
		}
		out = append(out, info)
	}
	return out
}

func decodeTxWithMetaToJSON(data []byte) (json.RawMessage, json.RawMessage, error) {
	return decodeTxWithMetaToJSONAt(data, handlers.SyntheticMetadataContext{})
}

func decodeTxWithMetaToJSONAt(
	data []byte,
	ctx handlers.SyntheticMetadataContext,
) (json.RawMessage, json.RawMessage, error) {
	txBlob, metaBlob, err := txcore.SplitTxWithMetaBlobStrict(data)
	if err != nil {
		return nil, nil, fmt.Errorf("split accepted transaction: %w", err)
	}

	txMap, err := binarycodec.DecodeBytes(txBlob)
	if err != nil {
		return nil, nil, fmt.Errorf("decode accepted transaction: %w", err)
	}
	txMap, err = txcore.CanonicalizeSerializedTransaction(txMap)
	if err != nil {
		return nil, nil, fmt.Errorf("validate accepted transaction: %w", err)
	}

	metaMap, err := binarycodec.DecodeBytes(metaBlob)
	if err != nil {
		return nil, nil, fmt.Errorf("decode accepted metadata: %w", err)
	}
	metaMap, err = txcore.CanonicalizeSerializedMetadata(metaMap)
	if err != nil {
		return nil, nil, fmt.Errorf("validate accepted metadata: %w", err)
	}
	handlers.InjectSyntheticFields(txMap, metaMap, ctx)

	txJSON, err := json.Marshal(txMap)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal accepted transaction: %w", err)
	}
	metaJSON, err := json.Marshal(metaMap)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal accepted metadata: %w", err)
	}
	return txJSON, metaJSON, nil
}

// rpcEventBridge fans the consensus engine's event bus out to the
// WebSocket subscription publisher. Mirrors NetworkOPs::pubValidation
// and NetworkOPs::pubConsensus (NetworkOPs.cpp:2380-2510): both feeds
// originate from the same engine and share a single bridge subscriber
// so the engine's broadcast goroutine never blocks on a publish. The
// manifest cache is threaded through so pubValidation can resolve the
// signing key back to its master key when the two differ.
type rpcEventBridge struct {
	publisher rpc.EventPublisher
	manifests *manifest.Cache
	networkID uint32
}

func (b *rpcEventBridge) OnEvent(event consensus.Event) {
	if b == nil || b.publisher == nil {
		return
	}
	switch e := event.(type) {
	case *consensus.ValidationReceivedEvent:
		if e == nil || e.Validation == nil {
			return
		}
		validationEvent, err := buildValidationEvent(e, b.manifests, b.networkID)
		if err != nil {
			xrpllog.Named(xrpllog.PartitionRPC).Error("Skipping invalid validation event", "err", err)
			return
		}
		b.publisher.PublishValidation(validationEvent)
	case *consensus.PhaseChangedEvent:
		if e == nil {
			return
		}
		b.publisher.PublishConsensusPhase(consensusPhaseName(e.NewPhase))
	}
}

func consensusPhaseName(p consensus.Phase) string {
	switch p {
	case consensus.PhaseOpen:
		return rpc.ConsensusPhaseOpen
	case consensus.PhaseEstablish:
		return rpc.ConsensusPhaseEstablish
	case consensus.PhaseAccepted:
		return rpc.ConsensusPhaseAccepted
	default:
		return p.String()
	}
}

// buildValidationEvent renders a rippled-shape validationReceived event
// from a ValidationReceivedEvent. master_key is emitted only when the
// manifest cache resolves a master distinct from the signing key
// (NetworkOPs.cpp:2434-2438); validation_public_key carries the signing
// (ephemeral) key in every case. Canonical STValidation bytes are
// surfaced via the `data` field and network_id
// from the local config (NetworkOPs.cpp:2423).
func buildValidationEvent(e *consensus.ValidationReceivedEvent, manifests *manifest.Cache, networkID uint32) (*rpc.ValidationEvent, error) {
	v := e.Validation
	canonical, err := consensusadaptor.CanonicalSTValidation(v)
	if err != nil {
		return nil, err
	}
	signingEnc, _ := addresscodec.EncodeNodePublicKey(v.SigningPubKey[:])
	ev := rpc.NewValidationEvent(
		upperHex(v.LedgerID[:]),
		v.LedgerSeq,
		signingEnc,
		upperHex(v.Signature),
		protocol.ToRippleTime(v.SignTime),
		v.Flags,
		v.Full,
	)
	ev.Data = upperHex(canonical)
	ev.NetworkID = networkID
	if !v.CloseTime.IsZero() {
		closeTime := protocol.ToRippleTime(v.CloseTime)
		ev.CloseTime = &closeTime
	}
	if manifests != nil {
		master := manifests.GetMasterKey(v.SigningPubKey)
		if master != v.SigningPubKey {
			if enc, err := addresscodec.EncodeNodePublicKey(master[:]); err == nil {
				ev.MasterKey = enc
			}
		}
	}
	if v.Cookie != 0 {
		ev.Cookie = strconv.FormatUint(v.Cookie, 10)
	}
	if v.HasLoadFee() {
		loadFee := v.LoadFee
		ev.LoadFee = &loadFee
	}
	if v.ServerVersion != 0 {
		ev.ServerVersion = strconv.FormatUint(v.ServerVersion, 10)
	}
	if v.HasBaseFee() {
		ev.BaseFee = float64(v.BaseFee)
	}
	if amount, ok := v.BaseFeeDropsVote(); ok {
		ev.BaseFee = jsonClippedXRPAmount(amount.Drops())
	}
	if v.HasReserveBase() {
		ev.ReserveBase = v.ReserveBase
	}
	if amount, ok := v.ReserveBaseDropsVote(); ok {
		ev.ReserveBase = jsonClippedXRPAmount(amount.Drops())
	}
	if v.HasReserveIncrement() {
		ev.ReserveInc = v.ReserveIncrement
	}
	if amount, ok := v.ReserveIncrementDropsVote(); ok {
		ev.ReserveInc = jsonClippedXRPAmount(amount.Drops())
	}
	if len(v.Amendments) > 0 {
		ev.Amendments = make([]string, len(v.Amendments))
		for i, a := range v.Amendments {
			ev.Amendments[i] = upperHex(a[:])
		}
	}
	if v.ValidatedHash != [32]byte{} {
		ev.ValidatedHash = upperHex(v.ValidatedHash[:])
	}
	return ev, nil
}

func jsonClippedXRPAmount(value int64) int32 {
	if value < math.MinInt32 {
		return math.MinInt32
	}
	if value > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(value)
}

func extractBookPairsFromMetadata(metaJSON []byte) []types.OrderBookSpec {
	var meta struct {
		AffectedNodes []map[string]json.RawMessage `json:"AffectedNodes"`
	}
	if err := json.Unmarshal(metaJSON, &meta); err != nil {
		return nil
	}
	seen := make(map[string]struct{})
	var out []types.OrderBookSpec
	for _, node := range meta.AffectedNodes {
		var raw json.RawMessage
		var fieldName string
		switch {
		case node["ModifiedNode"] != nil:
			raw = node["ModifiedNode"]
			fieldName = "PreviousFields"
		case node["CreatedNode"] != nil:
			raw = node["CreatedNode"]
			fieldName = "NewFields"
		case node["DeletedNode"] != nil:
			raw = node["DeletedNode"]
			fieldName = "FinalFields"
		default:
			continue
		}

		var fieldsByName map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fieldsByName); err != nil {
			continue
		}
		var ledgerEntryType string
		if err := json.Unmarshal(fieldsByName["LedgerEntryType"], &ledgerEntryType); err != nil || ledgerEntryType != "Offer" {
			continue
		}
		var fields map[string]any
		if err := json.Unmarshal(fieldsByName[fieldName], &fields); err != nil {
			continue
		}
		gets := currencySpecFromAmount(fields["TakerGets"])
		pays := currencySpecFromAmount(fields["TakerPays"])
		if !validBookCurrencySpec(gets) || !validBookCurrencySpec(pays) {
			continue
		}
		domain, _ := fields["DomainID"].(string)
		book := types.OrderBookSpec{TakerGets: gets, TakerPays: pays, Domain: domain}
		key := gets.Currency + "\x00" + gets.Issuer + "\x00" + strings.ToUpper(gets.MPTIssuanceID) + "\x00" +
			pays.Currency + "\x00" + pays.Issuer + "\x00" + strings.ToUpper(pays.MPTIssuanceID) + "\x00" + strings.ToUpper(domain)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, book)
	}
	return out
}

func currencySpecFromAmount(raw any) types.CurrencySpec {
	switch v := raw.(type) {
	case string:
		return types.CurrencySpec{Currency: "XRP"}
	case map[string]any:
		currency, _ := v["currency"].(string)
		issuer, _ := v["issuer"].(string)
		mptIssuanceID, _ := v["mpt_issuance_id"].(string)
		return types.CurrencySpec{Currency: currency, Issuer: issuer, MPTIssuanceID: mptIssuanceID}
	default:
		return types.CurrencySpec{}
	}
}

func validBookCurrencySpec(spec types.CurrencySpec) bool {
	return spec.Currency != "" || spec.MPTIssuanceID != ""
}

func buildProposedTxEvent(ev service.SubmittedTxEvent) *rpc.ProposedTransactionEvent {
	txJSON := json.RawMessage("{}")
	if len(ev.RawBlob) > 0 {
		if decoded, err := binarycodec.Decode(hex.EncodeToString(ev.RawBlob)); err == nil {
			if ev.OwnerFunds != "" {
				decoded["owner_funds"] = ev.OwnerFunds
			}
			if encoded, err := json.Marshal(decoded); err == nil {
				txJSON = encoded
			}
		}
	}
	return rpc.NewProposedTransactionEvent(
		txJSON,
		ev.Result.Name,
		ev.Result.Code,
		ev.Result.Message,
		ev.CurrentLedger,
		upperHex(ev.TxHash[:]),
	)
}

// buildManifestEvent renders a rippled-shape manifestReceived event.
// Mirrors NetworkOPs::pubManifest (NetworkOPs.cpp:2229-2265): the
// canonical serialized blob is emitted as `manifest`, with the master
// signature always present and signing_key/signature/domain conditional
// on manifest presence.
func buildManifestEvent(m *manifest.Manifest) *rpc.ManifestEvent {
	if m == nil {
		return nil
	}
	master := m.MasterKey()
	masterEnc, _ := addresscodec.EncodeNodePublicKey(master[:])
	var signingEnc string
	if !m.Revoked() {
		signing := m.SigningKey()
		signingEnc, _ = addresscodec.EncodeNodePublicKey(signing[:])
	}
	masterSig, sig := m.Signatures()
	return rpc.NewManifestEvent(
		masterEnc,
		signingEnc,
		masterSig,
		sig,
		m.Domain(),
		upperHex(m.Serialized()),
		m.Sequence(),
	)
}

type manifestEventPublisher interface {
	PublishManifest(*rpc.ManifestEvent)
	GetSubscriberCount(types.SubscriptionType) int
}

func publishManifestIfSubscribed(publisher manifestEventPublisher, m *manifest.Manifest) {
	if publisher == nil || publisher.GetSubscriberCount(types.SubManifests) == 0 {
		return
	}
	publisher.PublishManifest(buildManifestEvent(m))
}

// serverStatusSnapshot is the diff key for the pubServer emit gate.
// Two snapshots being equal means none of the fields rippled keys on
// (NetworkOPs.cpp:2278-2295 ServerFeeSummary::operator==) have moved,
// so the corresponding serverStatus event is suppressed.
type serverStatusSnapshot struct {
	baseFee                 uint64
	loadBase                uint64
	loadFactor              uint64
	loadFactorLocal         uint64
	loadFactorNet           uint64
	loadFactorCluster       uint64
	loadFactorFeeEscalation uint64
	loadFactorFeeQueue      uint64
	loadFactorFeeReference  uint64
	loadFactorServer        uint64
	serverStatus            string
}

// acceptedLedgerView adapts a LedgerAcceptedEvent to the
// LedgerWithTransactions surface ComputeBookChanges expects, feeding the
// transaction set directly off the event rather than re-fetching the
// ledger from the adapter (which can race close-time visibility).
type acceptedLedgerView struct {
	event              *service.LedgerAcceptedEvent
	transactionResults []service.TransactionResultEvent
}

func newAcceptedLedgerView(
	event *service.LedgerAcceptedEvent,
	transactionResults []service.TransactionResultEvent,
) *acceptedLedgerView {
	return &acceptedLedgerView{event: event, transactionResults: transactionResults}
}

func (a *acceptedLedgerView) ForEachTransaction(fn func(txHash [32]byte, txData []byte) bool) error {
	if a == nil || a.event == nil {
		return nil
	}
	for _, tr := range a.transactionResults {
		if !fn(tr.TxHash, tr.TxData) {
			return nil
		}
	}
	return nil
}

func (a *acceptedLedgerView) Sequence() uint32 {
	if a == nil || a.event == nil || a.event.LedgerInfo == nil {
		return 0
	}
	return a.event.LedgerInfo.Sequence
}

func (a *acceptedLedgerView) Hash() [32]byte {
	if a == nil || a.event == nil || a.event.LedgerInfo == nil {
		return [32]byte{}
	}
	return a.event.LedgerInfo.Hash
}

func (a *acceptedLedgerView) CloseTime() int64 {
	if a == nil || a.event == nil || a.event.LedgerInfo == nil {
		return 0
	}
	return protocol.RippleSeconds(a.event.LedgerInfo.CloseTime)
}

func (a *acceptedLedgerView) IsValidated() bool {
	if a == nil || a.event == nil || a.event.LedgerInfo == nil {
		return false
	}
	return a.event.LedgerInfo.Validated
}

func metaTransactionResult(metaJSON json.RawMessage) (ter.Result, error) {
	var meta struct {
		TransactionResult *string `json:"TransactionResult"`
	}
	if err := json.Unmarshal(metaJSON, &meta); err != nil {
		return 0, fmt.Errorf("decode transaction result: %w", err)
	}
	if meta.TransactionResult == nil || *meta.TransactionResult == "" {
		return 0, errors.New("metadata is missing TransactionResult")
	}
	name := *meta.TransactionResult
	code, err := definitions.Get().TransactionResultCode(name)
	if err != nil {
		return 0, fmt.Errorf("unknown transaction result %q: %w", name, err)
	}
	return ter.Result(code), nil
}

func buildValidatedTransactionEvent(
	txResult service.TransactionResultEvent,
	event *service.LedgerAcceptedEvent,
	defaultNetworkID uint32,
) (*rpc.TransactionEvent, ter.Result, error) {
	info := event.LedgerInfo
	closeTime := rippleEpochSeconds(info.CloseTime)
	txJSON, metaJSON, err := decodeTxWithMetaToJSONAt(txResult.TxData, handlers.SyntheticMetadataContext{
		LedgerSequence: txResult.LedgerIndex,
		CloseTime:      closeTime,
	})
	if err != nil {
		return nil, 0, err
	}
	engineResult, err := metaTransactionResult(metaJSON)
	if err != nil {
		return nil, 0, err
	}

	streamEvent := &rpc.TransactionEvent{
		Type:                "transaction",
		EngineResult:        engineResult.String(),
		EngineResultCode:    int(engineResult),
		EngineResultMessage: engineResult.Message(),
		LedgerIndex:         txResult.LedgerIndex,
		LedgerHash:          upperHex(info.Hash[:]),
		Transaction:         txJSON,
		Meta:                metaJSON,
		Hash:                upperHex(txResult.TxHash[:]),
		Validated:           txResult.Validated,
		Status:              "closed",
	}
	if !txResult.Validated {
		return streamEvent, engineResult, nil
	}

	if !info.CloseTime.IsZero() {
		streamEvent.CloseTimeISO = info.CloseTime.UTC().Format(time.RFC3339)
	}

	var txFields map[string]any
	if err := json.Unmarshal(txJSON, &txFields); err != nil {
		return nil, 0, fmt.Errorf("decode projected transaction: %w", err)
	}
	if txFields == nil {
		return nil, 0, errors.New("decoded projected transaction is null")
	}
	if closeTime >= 0 && closeTime <= int64(^uint32(0)) {
		txFields["date"] = uint32(closeTime)
	}
	if event.Ledger != nil {
		_, reserveBase, reserveInc := service.FeesFromLedger(event.Ledger)
		if funds, ok := handlers.TransactionOwnerFunds(txFields, event.Ledger, reserveBase, reserveInc); ok {
			txFields["owner_funds"] = funds
		}
	}
	encoded, err := json.Marshal(txFields)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal projected transaction: %w", err)
	}
	streamEvent.Transaction = encoded

	transactionIndex, ok := metadataTransactionIndex(metaJSON)
	if !ok {
		return nil, 0, errors.New("metadata is missing TransactionIndex")
	}
	networkID := defaultNetworkID
	if raw, exists := txFields["NetworkID"]; exists {
		if override, valid := uint32JSONValue(raw); valid {
			networkID = override
		}
	}
	if ctid, ok := handlers.EncodeCTID(txResult.LedgerIndex, transactionIndex, networkID); ok {
		streamEvent.CTID = ctid
	}
	return streamEvent, engineResult, nil
}

type validatedTransactionPublication struct {
	result       service.TransactionResultEvent
	event        *rpc.TransactionEvent
	engineResult ter.Result
}

func buildValidatedTransactionPublications(
	results []service.TransactionResultEvent,
	event *service.LedgerAcceptedEvent,
	defaultNetworkID uint32,
) ([]validatedTransactionPublication, error) {
	publications := make([]validatedTransactionPublication, 0, len(results))
	for _, result := range results {
		txEvent, engineResult, err := buildValidatedTransactionEvent(result, event, defaultNetworkID)
		if err != nil {
			return nil, fmt.Errorf("decode accepted transaction %s: %w", upperHex(result.TxHash[:]), err)
		}
		publications = append(publications, validatedTransactionPublication{
			result:       result,
			event:        txEvent,
			engineResult: engineResult,
		})
	}
	return publications, nil
}

func rippleEpochSeconds(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	seconds := t.Unix() - protocol.RippleEpochUnix
	if seconds < 0 {
		return 0
	}
	return seconds
}

func metadataTransactionIndex(metaJSON json.RawMessage) (uint32, bool) {
	var metadata struct {
		TransactionIndex *uint32 `json:"TransactionIndex"`
	}
	if err := json.Unmarshal(metaJSON, &metadata); err != nil || metadata.TransactionIndex == nil {
		return 0, false
	}
	return *metadata.TransactionIndex, true
}

func uint32JSONValue(value any) (uint32, bool) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0, false
	}
	var parsed uint32
	if err := json.Unmarshal(encoded, &parsed); err != nil {
		return 0, false
	}
	return parsed, true
}

// buildTable constructs the live amendment table from the operator's
// [amendments] config and any persisted runtime votes. Config preferences are
// applied first, then persisted votes (from the `feature` RPC) override them so
// runtime changes win across restarts — mirroring rippled, where the FeatureVotes
// DB takes precedence over the config stanzas. Unknown names are logged and
// ignored. The returned table owns operator veto/upvote and the enabled/blocked
// state, and is shared between the ledger service and the consensus adaptor.
