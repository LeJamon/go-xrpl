package message

// Message is the interface implemented by all protocol messages.
type Message interface {
	// Type returns the message type.
	Type() MessageType
}

// Manifest represents a validator manifest.
type Manifest struct {
	STObject []byte `json:"stobject"`
}

// Manifests is a collection of manifests.
type Manifests struct {
	List    []Manifest `json:"list"`
	History bool       `json:"history,omitempty"`
}

func (m *Manifests) Type() MessageType { return TypeManifests }

// ClusterNode represents a node in the cluster.
type ClusterNode struct {
	PublicKey  string `json:"public_key"`
	ReportTime uint32 `json:"report_time"`
	NodeLoad   uint32 `json:"node_load"`
	NodeName   string `json:"node_name,omitempty"`
	Address    string `json:"address,omitempty"`
}

// LoadSource represents a source of load.
type LoadSource struct {
	Name  string `json:"name"`
	Cost  uint32 `json:"cost"`
	Count uint32 `json:"count,omitempty"`
}

// Cluster represents cluster status.
type Cluster struct {
	ClusterNodes []ClusterNode `json:"cluster_nodes"`
	LoadSources  []LoadSource  `json:"load_sources"`
}

func (c *Cluster) Type() MessageType { return TypeCluster }

// Transaction represents a transaction message.
type Transaction struct {
	RawTransaction   []byte            `json:"raw_transaction"`
	Status           TransactionStatus `json:"status"`
	ReceiveTimestamp uint64            `json:"receive_timestamp,omitempty"`
	Deferred         bool              `json:"deferred,omitempty"`
}

func (t *Transaction) Type() MessageType { return TypeTransaction }

// Transactions is a collection of transactions.
type Transactions struct {
	Transactions []Transaction `json:"transactions"`
}

func (t *Transactions) Type() MessageType { return TypeTransactions }

// StatusChange represents a node status change.
type StatusChange struct {
	NewStatus          NodeStatus `json:"new_status,omitempty"`
	NewStatusSet       bool       `json:"-"`
	NewEvent           NodeEvent  `json:"new_event,omitempty"`
	NewEventSet        bool       `json:"-"`
	LedgerSeq          uint32     `json:"ledger_seq,omitempty"`
	LedgerSeqSet       bool       `json:"-"`
	LedgerHash         []byte     `json:"ledger_hash,omitempty"`
	LedgerHashPrevious []byte     `json:"ledger_hash_previous,omitempty"`
	NetworkTime        uint64     `json:"network_time,omitempty"`
	NetworkTimeSet     bool       `json:"-"`
	FirstSeq           *uint32    `json:"first_seq,omitempty"`
	LastSeq            *uint32    `json:"last_seq,omitempty"`
}

func (s *StatusChange) Type() MessageType { return TypeStatusChange }

func (s *StatusChange) HasNewStatus() bool {
	return s != nil && (s.NewStatusSet || s.NewStatus != 0)
}

func (s *StatusChange) HasNewEvent() bool {
	return s != nil && (s.NewEventSet || s.NewEvent != 0)
}

func (s *StatusChange) HasLedgerSeq() bool {
	return s != nil && (s.LedgerSeqSet || s.LedgerSeq != 0)
}

func (s *StatusChange) HasNetworkTime() bool {
	return s != nil && (s.NetworkTimeSet || s.NetworkTime != 0)
}

func (s *StatusChange) HasLedgerHash() bool {
	return s != nil && s.LedgerHash != nil
}

// ProposeSet represents a ledger proposal.
type ProposeSet struct {
	ProposeSeq          uint32   `json:"propose_seq"`
	CurrentTxHash       []byte   `json:"current_tx_hash"`
	NodePubKey          []byte   `json:"node_pub_key"`
	CloseTime           uint32   `json:"close_time"`
	Signature           []byte   `json:"signature"`
	PreviousLedger      []byte   `json:"previous_ledger"`
	AddedTransactions   [][]byte `json:"added_transactions,omitempty"`
	RemovedTransactions [][]byte `json:"removed_transactions,omitempty"`
}

func (p *ProposeSet) Type() MessageType { return TypeProposeLedger }

// HaveTransactionSet indicates availability of a transaction set.
type HaveTransactionSet struct {
	Status TxSetStatus `json:"status"`
	Hash   []byte      `json:"hash"`
}

func (h *HaveTransactionSet) Type() MessageType { return TypeHaveSet }

// ValidatorList represents a validator list (UNL).
type ValidatorList struct {
	Manifest  []byte `json:"manifest"`
	Blob      []byte `json:"blob"`
	Signature []byte `json:"signature"`
	Version   uint32 `json:"version"`
}

func (v *ValidatorList) Type() MessageType { return TypeValidatorList }

// ValidatorBlobInfo represents v2 validator blob info.
type ValidatorBlobInfo struct {
	Manifest  []byte `json:"manifest,omitempty"`
	Blob      []byte `json:"blob"`
	Signature []byte `json:"signature"`
}

func (v *ValidatorBlobInfo) HasManifest() bool {
	return v != nil && v.Manifest != nil
}

// ValidatorListCollection represents a collection of v2 validator lists.
type ValidatorListCollection struct {
	Version  uint32              `json:"version"`
	Manifest []byte              `json:"manifest"`
	Blobs    []ValidatorBlobInfo `json:"blobs"`
}

func (v *ValidatorListCollection) Type() MessageType { return TypeValidatorListCollection }

// Validation represents a ledger validation message.
type Validation struct {
	Validation []byte `json:"validation"`
}

func (v *Validation) Type() MessageType { return TypeValidation }

// Endpointv2 represents a peer endpoint.
type Endpointv2 struct {
	Endpoint string `json:"endpoint"`
	Hops     uint32 `json:"hops"`
}

// Endpoints represents peer endpoints for discovery.
type Endpoints struct {
	Version     uint32       `json:"version"`
	EndpointsV2 []Endpointv2 `json:"endpoints_v2"`
}

func (e *Endpoints) Type() MessageType { return TypeEndpoints }

// IndexedObject represents an indexed object.
type IndexedObject struct {
	Hash      []byte `json:"hash,omitempty"`
	NodeID    []byte `json:"node_id,omitempty"`
	Index     []byte `json:"index,omitempty"`
	Data      []byte `json:"data,omitempty"`
	LedgerSeq uint32 `json:"ledger_seq,omitempty"`
}

// GetObjectByHash requests objects by hash.
//
// The legacy top-level seq field (proto field 3, "used to match replies to
// queries") was removed and reserved in the 3.2.0 peer protocol; a 3.2.0 peer
// neither sends nor interprets it. Decoding an old peer's seq is tolerated
// (protobuf skips the reserved field).
type GetObjectByHash struct {
	ObjType    ObjectType      `json:"type"`
	Query      bool            `json:"query"`
	LedgerHash []byte          `json:"ledger_hash,omitempty"`
	Fat        bool            `json:"fat,omitempty"`
	Objects    []IndexedObject `json:"objects,omitempty"`
}

func (g *GetObjectByHash) Type() MessageType { return TypeGetObjects }

func (g *GetObjectByHash) HasLedgerHash() bool {
	return g != nil && g.LedgerHash != nil
}

// LedgerNode represents a node in the ledger.
type LedgerNode struct {
	NodeData []byte `json:"nodedata"`
	NodeID   []byte `json:"nodeid,omitempty"`
}

// GetLedger requests ledger data.
type GetLedger struct {
	InfoType         LedgerInfoType   `json:"itype"`
	LType            LedgerType       `json:"ltype,omitempty"`
	LTypeSet         bool             `json:"-"`
	LedgerHash       []byte           `json:"ledger_hash,omitempty"`
	LedgerSeq        uint32           `json:"ledger_seq,omitempty"`
	LedgerSeqSet     bool             `json:"-"`
	NodeIDs          [][]byte         `json:"node_ids,omitempty"`
	RequestCookie    uint64           `json:"request_cookie,omitempty"`
	RequestCookieSet bool             `json:"-"`
	QueryType        *LedgerQueryType `json:"query_type,omitempty"`
	QueryDepth       uint32           `json:"query_depth,omitempty"`
	QueryDepthSet    bool             `json:"-"`
}

func (g *GetLedger) Type() MessageType { return TypeGetLedger }

func (g *GetLedger) HasLType() bool {
	return g != nil && (g.LTypeSet || g.LType != 0)
}

func (g *GetLedger) HasLedgerHash() bool {
	return g != nil && g.LedgerHash != nil
}

// HasLedgerSeq reports protobuf field presence, including an explicit zero.
func (g *GetLedger) HasLedgerSeq() bool {
	return g != nil && (g.LedgerSeqSet || g.LedgerSeq != 0)
}

// HasRequestCookie reports protobuf field presence, including an explicit zero.
func (g *GetLedger) HasRequestCookie() bool {
	return g != nil && (g.RequestCookieSet || g.RequestCookie != 0)
}

// HasQueryDepth reports protobuf field presence, including an explicit zero.
func (g *GetLedger) HasQueryDepth() bool {
	return g != nil && (g.QueryDepthSet || g.QueryDepth != 0)
}

// LedgerData contains ledger data response.
type LedgerData struct {
	LedgerHash       []byte         `json:"ledger_hash"`
	LedgerSeq        uint32         `json:"ledger_seq"`
	InfoType         LedgerInfoType `json:"type"`
	Nodes            []LedgerNode   `json:"nodes,omitempty"`
	RequestCookie    uint32         `json:"request_cookie,omitempty"`
	RequestCookieSet bool           `json:"-"`
	Error            ReplyError     `json:"error,omitempty"`
	ErrorSet         bool           `json:"-"`
}

func (l *LedgerData) Type() MessageType { return TypeLedgerData }

// HasRequestCookie reports protobuf field presence, including an explicit zero.
func (l *LedgerData) HasRequestCookie() bool {
	return l != nil && (l.RequestCookieSet || l.RequestCookie != 0)
}

func (l *LedgerData) HasError() bool {
	return l != nil && (l.ErrorSet || l.Error != ReplyErrorNone)
}

// Ping represents a ping/pong message for keepalive and latency measurement.
type Ping struct {
	PType  PingType `json:"type"`
	Seq    uint32   `json:"seq,omitempty"`
	SeqSet bool     `json:"-"`
}

func (p *Ping) Type() MessageType { return TypePing }

func (p *Ping) HasSeq() bool {
	return p != nil && (p.SeqSet || p.Seq != 0)
}

// Squelch represents a squelch message for reduce-relay.
type Squelch struct {
	Squelch         bool   `json:"squelch"`
	ValidatorPubKey []byte `json:"validator_pub_key"`
	SquelchDuration uint32 `json:"squelch_duration,omitempty"`
}

func (s *Squelch) Type() MessageType { return TypeSquelch }

// ProofPathRequest requests a proof path.
type ProofPathRequest struct {
	Key        []byte        `json:"key"`
	LedgerHash []byte        `json:"ledger_hash"`
	MapType    LedgerMapType `json:"type"`
}

func (p *ProofPathRequest) Type() MessageType { return TypeProofPathReq }

// ProofPathResponse contains a proof path response.
type ProofPathResponse struct {
	Key          []byte        `json:"key"`
	LedgerHash   []byte        `json:"ledger_hash"`
	MapType      LedgerMapType `json:"type"`
	LedgerHeader []byte        `json:"ledger_header,omitempty"`
	Path         [][]byte      `json:"path,omitempty"`
	Error        ReplyError    `json:"error,omitempty"`
	ErrorSet     bool          `json:"-"`
}

func (p *ProofPathResponse) Type() MessageType { return TypeProofPathResponse }

func (p *ProofPathResponse) HasError() bool {
	return p != nil && (p.ErrorSet || p.Error != ReplyErrorNone)
}

// ReplayDeltaRequest requests replay delta.
type ReplayDeltaRequest struct {
	LedgerHash []byte `json:"ledger_hash"`
}

func (r *ReplayDeltaRequest) Type() MessageType { return TypeReplayDeltaReq }

// ReplayDeltaResponse contains replay delta response.
type ReplayDeltaResponse struct {
	LedgerHash   []byte     `json:"ledger_hash"`
	LedgerHeader []byte     `json:"ledger_header,omitempty"`
	Transactions [][]byte   `json:"transaction,omitempty"`
	Error        ReplyError `json:"error,omitempty"`
	ErrorSet     bool       `json:"-"`
}

func (r *ReplayDeltaResponse) Type() MessageType { return TypeReplayDeltaResponse }

func (r *ReplayDeltaResponse) HasError() bool {
	return r != nil && (r.ErrorSet || r.Error != ReplyErrorNone)
}

// HaveTransactions indicates available transaction hashes.
type HaveTransactions struct {
	Hashes [][]byte `json:"hashes"`
}

func (h *HaveTransactions) Type() MessageType { return TypeHaveTransactions }
