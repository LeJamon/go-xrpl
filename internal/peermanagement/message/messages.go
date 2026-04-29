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
	NewEvent           NodeEvent  `json:"new_event,omitempty"`
	LedgerSeq          uint32     `json:"ledger_seq,omitempty"`
	LedgerHash         []byte     `json:"ledger_hash,omitempty"`
	LedgerHashPrevious []byte     `json:"ledger_hash_previous,omitempty"`
	NetworkTime        uint64     `json:"network_time,omitempty"`
	FirstSeq           *uint32    `json:"first_seq,omitempty"`
	LastSeq            *uint32    `json:"last_seq,omitempty"`
}

func (s *StatusChange) Type() MessageType { return TypeStatusChange }

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
type GetObjectByHash struct {
	ObjType    ObjectType      `json:"type"`
	Query      bool            `json:"query"`
	Seq        uint32          `json:"seq,omitempty"`
	LedgerHash []byte          `json:"ledger_hash,omitempty"`
	Fat        bool            `json:"fat,omitempty"`
	Objects    []IndexedObject `json:"objects,omitempty"`
}

func (g *GetObjectByHash) Type() MessageType { return TypeGetObjects }

// LedgerNode represents a node in the ledger.
type LedgerNode struct {
	NodeData []byte `json:"nodedata"`
	NodeID   []byte `json:"nodeid,omitempty"`
}

// GetLedger requests ledger data.
type GetLedger struct {
	InfoType      LedgerInfoType `json:"itype"`
	LType         LedgerType     `json:"ltype,omitempty"`
	LedgerHash    []byte         `json:"ledger_hash,omitempty"`
	LedgerSeq     uint32         `json:"ledger_seq,omitempty"`
	NodeIDs       [][]byte       `json:"node_ids,omitempty"`
	RequestCookie uint64         `json:"request_cookie,omitempty"`
	QueryDepth    uint32         `json:"query_depth,omitempty"`
	QueryType     QueryType      `json:"query_type,omitempty"`
}

func (g *GetLedger) Type() MessageType { return TypeGetLedger }

// LedgerData contains ledger data response.
type LedgerData struct {
	LedgerHash    []byte         `json:"ledger_hash"`
	LedgerSeq     uint32         `json:"ledger_seq"`
	InfoType      LedgerInfoType `json:"type"`
	Nodes         []LedgerNode   `json:"nodes,omitempty"`
	RequestCookie uint32         `json:"request_cookie,omitempty"`
	Error         ReplyError     `json:"error,omitempty"`
}

func (l *LedgerData) Type() MessageType { return TypeLedgerData }

// Ping represents a ping/pong message for keepalive and latency measurement.
type Ping struct {
	PType    PingType `json:"type"`
	Seq      uint32   `json:"seq,omitempty"`
	PingTime uint64   `json:"ping_time,omitempty"`
	NetTime  uint64   `json:"net_time,omitempty"`
}

func (p *Ping) Type() MessageType { return TypePing }

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
}

func (p *ProofPathResponse) Type() MessageType { return TypeProofPathResponse }

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
}

func (r *ReplayDeltaResponse) Type() MessageType { return TypeReplayDeltaResponse }

// HaveTransactions indicates available transaction hashes.
type HaveTransactions struct {
	Hashes [][]byte `json:"hashes"`
}

func (h *HaveTransactions) Type() MessageType { return TypeHaveTransactions }

// QueryType for GetLedger requests
type QueryType int32

const (
	// QueryTypeLedgerHeader requests the ledger header.
	QueryTypeLedgerHeader QueryType = 0
	// QueryTypeAccountState requests account state nodes.
	QueryTypeAccountState QueryType = 1
	// QueryTypeTransactionData requests transaction nodes.
	QueryTypeTransactionData QueryType = 2
)
