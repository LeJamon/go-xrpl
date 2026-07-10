package node

import (
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/cleaner"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/internal/rpc"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/txq"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

type ledgerCleanerSource struct {
	svc    *service.Service
	family shamap.Family
}

func (s *ledgerCleanerSource) AvailableRange() (uint32, uint32, bool) {
	return s.svc.AvailableLedgerRange()
}

func (s *ledgerCleanerSource) LedgerRoots(seq uint32) (stateRoot, txRoot [32]byte, ok bool) {
	l, err := s.svc.GetLedgerBySequence(seq)
	if err != nil || l == nil {
		return [32]byte{}, [32]byte{}, false
	}
	sr, err := l.StateMapHash()
	if err != nil {
		return [32]byte{}, [32]byte{}, false
	}
	tr, err := l.TxMapHash()
	if err != nil {
		return [32]byte{}, [32]byte{}, false
	}
	return sr, tr, true
}

func (s *ledgerCleanerSource) Family() shamap.Family { return s.family }

// toCleanerStatus translates the cleaner package's status into the RPC-types
// mirror struct (see ServiceContainer.LedgerCleanerConfigure for the layering
// boundary).
func toCleanerStatus(s cleaner.Status) types.LedgerCleanerStatus {
	return types.LedgerCleanerStatus{
		State:          s.State,
		MinLedger:      s.MinLedger,
		MaxLedger:      s.MaxLedger,
		CheckNodes:     s.CheckNodes,
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

	baseFee, reserveBase, reserveInc := a.ledgerService.GetCurrentFees()

	ledgerTime := uint32(validatedLedger.CloseTime().Unix() - protocol.RippleEpochUnix)

	hash := validatedLedger.Hash()
	serverInfo := a.ledgerService.GetServerInfo()

	return &types.LedgerSubscribeInfo{
		LedgerIndex:      validatedLedger.Sequence(),
		LedgerHash:       upperHex(hash[:]),
		LedgerTime:       ledgerTime,
		FeeBase:          baseFee,
		FeeRef:           baseFee,
		ReserveBase:      reserveBase,
		ReserveInc:       reserveInc,
		ValidatedLedgers: serverInfo.CompleteLedgers,
		NetworkID:        serverInfo.NetworkID,
		XRPFeesEnabled:   a.ledgerService.XRPFeesEnabled(),
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

// decodeTxWithMetaToJSON splits a VL-encoded tx+meta binary blob and decodes
// each part to JSON. The blob format is: [VL-length][tx_blob][VL-length][meta_blob].
// Returns (txJSON, metaJSON) as json.RawMessage, or empty JSON objects on error.
func decodeTxWithMetaToJSON(data []byte) (json.RawMessage, json.RawMessage) {
	emptyObj := json.RawMessage("{}")

	if len(data) == 0 {
		return emptyObj, emptyObj
	}

	// Parse first VL field (transaction)
	txLen, txPrefixLen := parseVLLength(data)
	if txPrefixLen == 0 || txPrefixLen+txLen > len(data) {
		return emptyObj, emptyObj
	}
	txBlob := data[txPrefixLen : txPrefixLen+txLen]

	// Parse second VL field (metadata)
	metaStart := txPrefixLen + txLen
	var metaBlob []byte
	if metaStart < len(data) {
		metaLen, metaPrefixLen := parseVLLength(data[metaStart:])
		if metaPrefixLen > 0 && metaStart+metaPrefixLen+metaLen <= len(data) {
			metaBlob = data[metaStart+metaPrefixLen : metaStart+metaPrefixLen+metaLen]
		}
	}

	// Decode transaction binary to JSON
	txHex := hex.EncodeToString(txBlob)
	txMap, err := binarycodec.Decode(txHex)
	if err != nil {
		return emptyObj, emptyObj
	}
	txJSON, err := json.Marshal(txMap)
	if err != nil {
		return emptyObj, emptyObj
	}

	// Decode metadata binary to JSON
	metaJSON := emptyObj
	if len(metaBlob) > 0 {
		metaHex := hex.EncodeToString(metaBlob)
		metaMap, err := binarycodec.Decode(metaHex)
		if err == nil {
			if m, err := json.Marshal(metaMap); err == nil {
				metaJSON = m
			}
		}
	}

	return json.RawMessage(txJSON), metaJSON
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
		b.publisher.PublishValidation(buildValidationEvent(e, b.manifests, b.networkID))
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
// (ephemeral) key in every case. The raw STValidation wire bytes are
// surfaced via the `data` field (NetworkOPs.cpp:2422) and network_id
// from the local config (NetworkOPs.cpp:2423).
func buildValidationEvent(e *consensus.ValidationReceivedEvent, manifests *manifest.Cache, networkID uint32) *rpc.ValidationEvent {
	v := e.Validation
	signingEnc, _ := addresscodec.EncodeNodePublicKey(v.SigningPubKey[:])
	ev := rpc.NewValidationEvent(
		upperHex(v.LedgerID[:]),
		strconv.FormatUint(uint64(v.LedgerSeq), 10),
		signingEnc,
		upperHex(v.Signature),
		uint32(v.SignTime.Unix()-protocol.RippleEpochUnix),
		v.Flags,
		v.Full,
	)
	if len(v.Raw) > 0 {
		ev.Data = upperHex(v.Raw)
	}
	if networkID > 0 {
		ev.NetworkID = networkID
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
	if v.LoadFee != 0 {
		ev.LoadFee = v.LoadFee
	}
	if v.ServerVersion != 0 {
		ev.ServerVersion = strconv.FormatUint(v.ServerVersion, 10)
	}
	if v.BaseFee != 0 {
		ev.BaseFee = v.BaseFee
	} else if v.BaseFeeDrops != 0 {
		ev.BaseFee = v.BaseFeeDrops
	}
	if v.ReserveBase != 0 {
		ev.ReserveBase = uint64(v.ReserveBase)
	} else if v.ReserveBaseDrops != 0 {
		ev.ReserveBase = v.ReserveBaseDrops
	}
	if v.ReserveIncrement != 0 {
		ev.ReserveInc = uint64(v.ReserveIncrement)
	} else if v.ReserveIncrementDrops != 0 {
		ev.ReserveInc = v.ReserveIncrementDrops
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
	return ev
}

// bookPair holds a single (takerGets, takerPays) currency pair touched
// by a transaction. Used to fan one tx out to N per-book subscribers.
type bookPair struct {
	takerGets types.CurrencySpec
	takerPays types.CurrencySpec
}

// extractBookPairsFromTxData walks a VL-encoded tx+meta blob and
// returns every distinct (takerGets, takerPays) pair from affected
// Offer nodes. Mirrors rippled's per-tx fan-out in NetworkOPs::pubProposedTx
// which feeds each Offer change into the matching subBook subscribers.
func extractBookPairsFromTxData(data []byte) []bookPair {
	_, metaJSON := decodeTxWithMetaToJSON(data)
	if len(metaJSON) == 0 {
		return nil
	}
	var meta struct {
		AffectedNodes []map[string]json.RawMessage `json:"AffectedNodes"`
	}
	if err := json.Unmarshal(metaJSON, &meta); err != nil {
		return nil
	}
	seen := make(map[string]struct{})
	var out []bookPair
	for _, node := range meta.AffectedNodes {
		for _, raw := range node {
			var nd struct {
				LedgerEntryType string         `json:"LedgerEntryType"`
				FinalFields     map[string]any `json:"FinalFields"`
			}
			if err := json.Unmarshal(raw, &nd); err != nil {
				continue
			}
			if nd.LedgerEntryType != "Offer" || nd.FinalFields == nil {
				continue
			}
			gets := currencySpecFromAmount(nd.FinalFields["TakerGets"])
			pays := currencySpecFromAmount(nd.FinalFields["TakerPays"])
			key := gets.Currency + "/" + gets.Issuer + "|" + pays.Currency + "/" + pays.Issuer
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, bookPair{takerGets: gets, takerPays: pays})
		}
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
		return types.CurrencySpec{Currency: currency, Issuer: issuer}
	default:
		return types.CurrencySpec{}
	}
}

func buildProposedTxEvent(ev service.SubmittedTxEvent) *rpc.ProposedTransactionEvent {
	txJSON := json.RawMessage("{}")
	var sourceAccount string
	if len(ev.RawBlob) > 0 {
		if decoded, err := binarycodec.Decode(hex.EncodeToString(ev.RawBlob)); err == nil {
			if acc, ok := decoded["Account"].(string); ok {
				sourceAccount = acc
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
		sourceAccount,
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
	masterEnc, _ := addresscodec.EncodeNodePublicKey(m.MasterKey[:])
	var signingEnc string
	if !m.Revoked() {
		signingEnc, _ = addresscodec.EncodeNodePublicKey(m.SigningKey[:])
	}
	masterSig, sig := m.Signatures()
	return rpc.NewManifestEvent(
		masterEnc,
		signingEnc,
		masterSig,
		sig,
		m.Domain,
		upperHex(m.Serialized),
		m.Sequence,
	)
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
	event *service.LedgerAcceptedEvent
}

func newAcceptedLedgerView(event *service.LedgerAcceptedEvent) *acceptedLedgerView {
	return &acceptedLedgerView{event: event}
}

func (a *acceptedLedgerView) ForEachTransaction(fn func(txHash [32]byte, txData []byte) bool) error {
	if a == nil || a.event == nil {
		return nil
	}
	for _, tr := range a.event.TransactionResults {
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
	return a.event.LedgerInfo.CloseTime.Unix() - protocol.RippleEpochUnix
}

func (a *acceptedLedgerView) IsValidated() bool {
	if a == nil || a.event == nil || a.event.LedgerInfo == nil {
		return false
	}
	return a.event.LedgerInfo.Validated
}

// metaTransactionResult returns the TransactionResult string (e.g.
// "tesSUCCESS") from a decoded transaction metadata blob. Returns
// "tesSUCCESS" when the field is missing so callers stay on the
// historic happy-path default; book-stream consumers gate on the
// returned value matching "tesSUCCESS" exactly, mirroring rippled's
// pubValidatedTransaction tesSUCCESS gate at NetworkOPs.cpp:3409-3410.
func metaTransactionResult(metaJSON json.RawMessage) string {
	if len(metaJSON) == 0 {
		return "tesSUCCESS"
	}
	var meta struct {
		TransactionResult string `json:"TransactionResult"`
	}
	if err := json.Unmarshal(metaJSON, &meta); err != nil || meta.TransactionResult == "" {
		return "tesSUCCESS"
	}
	return meta.TransactionResult
}

// parseVLLength parses a variable-length field prefix.
// Returns (length, bytesConsumed).
func parseVLLength(data []byte) (int, int) {
	if len(data) == 0 {
		return 0, 0
	}
	b1 := int(data[0])
	if b1 <= 192 {
		return b1, 1
	}
	if b1 <= 240 {
		if len(data) < 2 {
			return 0, 0
		}
		return 193 + ((b1 - 193) * 256) + int(data[1]), 2
	}
	if len(data) < 3 {
		return 0, 0
	}
	return 12481 + ((b1 - 241) * 65536) + (int(data[1]) * 256) + int(data[2]), 3
}

// buildTable constructs the live amendment table from the operator's
// [amendments] config and any persisted runtime votes. Config preferences are
// applied first, then persisted votes (from the `feature` RPC) override them so
// runtime changes win across restarts — mirroring rippled, where the FeatureVotes
// DB takes precedence over the config stanzas. Unknown names are logged and
// ignored. The returned table owns operator veto/upvote and the enabled/blocked
// state, and is shared between the ledger service and the consensus adaptor.
