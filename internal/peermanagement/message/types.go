// Package message implements XRPL peer protocol message types and serialization.
// This package provides message encoding/decoding compatible with rippled's
// protocol buffer format and wire protocol.
package message

import "github.com/LeJamon/go-xrpl/internal/peermanagement/proto"

// MessageType represents the type of a peer protocol message.
// Reference: rippled ripple.proto MessageType enum
type MessageType uint16

const (
	TypeUnknown                 MessageType = 0
	TypeManifests               MessageType = MessageType(proto.MessageType_mtMANIFESTS)
	TypePing                    MessageType = MessageType(proto.MessageType_mtPING)
	TypeCluster                 MessageType = MessageType(proto.MessageType_mtCLUSTER)
	TypeEndpoints               MessageType = MessageType(proto.MessageType_mtENDPOINTS)
	TypeTransaction             MessageType = MessageType(proto.MessageType_mtTRANSACTION)
	TypeGetLedger               MessageType = MessageType(proto.MessageType_mtGET_LEDGER)
	TypeLedgerData              MessageType = MessageType(proto.MessageType_mtLEDGER_DATA)
	TypeProposeLedger           MessageType = MessageType(proto.MessageType_mtPROPOSE_LEDGER)
	TypeStatusChange            MessageType = MessageType(proto.MessageType_mtSTATUS_CHANGE)
	TypeHaveSet                 MessageType = MessageType(proto.MessageType_mtHAVE_SET)
	TypeValidation              MessageType = MessageType(proto.MessageType_mtVALIDATION)
	TypeGetObjects              MessageType = MessageType(proto.MessageType_mtGET_OBJECTS)
	TypeValidatorList           MessageType = MessageType(proto.MessageType_mtVALIDATOR_LIST)
	TypeSquelch                 MessageType = MessageType(proto.MessageType_mtSQUELCH)
	TypeValidatorListCollection MessageType = MessageType(proto.MessageType_mtVALIDATOR_LIST_COLLECTION)
	TypeProofPathReq            MessageType = MessageType(proto.MessageType_mtPROOF_PATH_REQ)
	TypeProofPathResponse       MessageType = MessageType(proto.MessageType_mtPROOF_PATH_RESPONSE)
	TypeReplayDeltaReq          MessageType = MessageType(proto.MessageType_mtREPLAY_DELTA_REQ)
	TypeReplayDeltaResponse     MessageType = MessageType(proto.MessageType_mtREPLAY_DELTA_RESPONSE)
	TypeHaveTransactions        MessageType = MessageType(proto.MessageType_mtHAVE_TRANSACTIONS)
	TypeTransactions            MessageType = MessageType(proto.MessageType_mtTRANSACTIONS)
)

// String returns the string representation of a MessageType.
func (t MessageType) String() string {
	if name, ok := proto.MessageType_name[int32(t)]; ok {
		return name
	}
	return "mtUNKNOWN"
}

// TransactionStatus represents the status of a transaction.
type TransactionStatus int32

const (
	TxStatusNew            TransactionStatus = TransactionStatus(proto.TransactionStatus_tsNEW)
	TxStatusCurrent        TransactionStatus = TransactionStatus(proto.TransactionStatus_tsCURRENT)
	TxStatusCommitted      TransactionStatus = TransactionStatus(proto.TransactionStatus_tsCOMMITTED)
	TxStatusRejectConflict TransactionStatus = TransactionStatus(proto.TransactionStatus_tsREJECT_CONFLICT)
	TxStatusRejectInvalid  TransactionStatus = TransactionStatus(proto.TransactionStatus_tsREJECT_INVALID)
	TxStatusRejectFunds    TransactionStatus = TransactionStatus(proto.TransactionStatus_tsREJECT_FUNDS)
	TxStatusHeldSeq        TransactionStatus = TransactionStatus(proto.TransactionStatus_tsHELD_SEQ)
	TxStatusHeldLedger     TransactionStatus = TransactionStatus(proto.TransactionStatus_tsHELD_LEDGER)
)

// NodeStatus represents the status of a node.
type NodeStatus int32

const (
	NodeStatusConnecting NodeStatus = NodeStatus(proto.NodeStatus_nsCONNECTING)
	NodeStatusConnected  NodeStatus = NodeStatus(proto.NodeStatus_nsCONNECTED)
	NodeStatusMonitoring NodeStatus = NodeStatus(proto.NodeStatus_nsMONITORING)
	NodeStatusValidating NodeStatus = NodeStatus(proto.NodeStatus_nsVALIDATING)
	NodeStatusShutting   NodeStatus = NodeStatus(proto.NodeStatus_nsSHUTTING)
)

// NodeEvent represents an event on a node.
type NodeEvent int32

const (
	NodeEventClosingLedger  NodeEvent = NodeEvent(proto.NodeEvent_neCLOSING_LEDGER)
	NodeEventAcceptedLedger NodeEvent = NodeEvent(proto.NodeEvent_neACCEPTED_LEDGER)
	NodeEventSwitchedLedger NodeEvent = NodeEvent(proto.NodeEvent_neSWITCHED_LEDGER)
	NodeEventLostSync       NodeEvent = NodeEvent(proto.NodeEvent_neLOST_SYNC)
)

// TxSetStatus represents the status of a transaction set.
type TxSetStatus int32

const (
	TxSetStatusHave   TxSetStatus = TxSetStatus(proto.TxSetStatus_tsHAVE)
	TxSetStatusCanGet TxSetStatus = TxSetStatus(proto.TxSetStatus_tsCAN_GET)
	TxSetStatusNeed   TxSetStatus = TxSetStatus(proto.TxSetStatus_tsNEED)
)

// LedgerInfoType represents types of ledger information.
type LedgerInfoType int32

const (
	LedgerInfoBase        LedgerInfoType = LedgerInfoType(proto.TMLedgerInfoType_liBASE)
	LedgerInfoTxNode      LedgerInfoType = LedgerInfoType(proto.TMLedgerInfoType_liTX_NODE)
	LedgerInfoAsNode      LedgerInfoType = LedgerInfoType(proto.TMLedgerInfoType_liAS_NODE)
	LedgerInfoTsCandidate LedgerInfoType = LedgerInfoType(proto.TMLedgerInfoType_liTS_CANDIDATE)
)

// LedgerQueryType represents the GetLedger query routing mode. qtINDIRECT
// is the only defined value; a present query_type that is anything else is
// rejected as invalid data. The field is modelled as a pointer on GetLedger
// so absence (the common case) is distinguishable from an explicit
// qtINDIRECT, matching rippled's has_querytype() presence check.
type LedgerQueryType int32

const (
	QueryTypeIndirect LedgerQueryType = LedgerQueryType(proto.TMQueryType_qtINDIRECT)
)

// LedgerType represents types of ledgers.
type LedgerType int32

const (
	LedgerTypeAccepted LedgerType = LedgerType(proto.TMLedgerType_ltACCEPTED)
	LedgerTypeCurrent  LedgerType = LedgerType(proto.TMLedgerType_ltCURRENT)
	LedgerTypeClosed   LedgerType = LedgerType(proto.TMLedgerType_ltCLOSED)
)

// ReplyError represents error codes in replies.
type ReplyError int32

const (
	ReplyErrorNone       ReplyError = 0
	ReplyErrorNoLedger   ReplyError = ReplyError(proto.TMReplyError_reNO_LEDGER)
	ReplyErrorNoNode     ReplyError = ReplyError(proto.TMReplyError_reNO_NODE)
	ReplyErrorBadRequest ReplyError = ReplyError(proto.TMReplyError_reBAD_REQUEST)
)

// PingType represents the type of a ping message.
type PingType int32

const (
	PingTypePing PingType = PingType(proto.TMPing_ptPING)
	PingTypePong PingType = PingType(proto.TMPing_ptPONG)
)

// ObjectType represents types of objects that can be requested.
type ObjectType int32

const (
	ObjectTypeUnknown         ObjectType = ObjectType(proto.TMGetObjectByHash_otUNKNOWN)
	ObjectTypeLedger          ObjectType = ObjectType(proto.TMGetObjectByHash_otLEDGER)
	ObjectTypeTransaction     ObjectType = ObjectType(proto.TMGetObjectByHash_otTRANSACTION)
	ObjectTypeTransactionNode ObjectType = ObjectType(proto.TMGetObjectByHash_otTRANSACTION_NODE)
	ObjectTypeStateNode       ObjectType = ObjectType(proto.TMGetObjectByHash_otSTATE_NODE)
	ObjectTypeCasObject       ObjectType = ObjectType(proto.TMGetObjectByHash_otCAS_OBJECT)
	ObjectTypeFetchPack       ObjectType = ObjectType(proto.TMGetObjectByHash_otFETCH_PACK)
	ObjectTypeTransactions    ObjectType = ObjectType(proto.TMGetObjectByHash_otTRANSACTIONS)
)

// LedgerMapType represents types of ledger maps.
type LedgerMapType int32

const (
	LedgerMapTransaction  LedgerMapType = LedgerMapType(proto.TMLedgerMapType_lmTRANSACTION)
	LedgerMapAccountState LedgerMapType = LedgerMapType(proto.TMLedgerMapType_lmACCOUNT_STATE)
)
