// Package message implements XRPL peer protocol message types and serialization.
// This package provides message encoding/decoding compatible with rippled's
// protocol buffer format and wire protocol.
package message

// MessageType represents the type of a peer protocol message.
// Reference: rippled ripple.proto MessageType enum
type MessageType uint16

const (
	TypeUnknown                 MessageType = 0
	TypeManifests               MessageType = 2
	TypePing                    MessageType = 3
	TypeCluster                 MessageType = 5
	TypeEndpoints               MessageType = 15
	TypeTransaction             MessageType = 30
	TypeGetLedger               MessageType = 31
	TypeLedgerData              MessageType = 32
	TypeProposeLedger           MessageType = 33
	TypeStatusChange            MessageType = 34
	TypeHaveSet                 MessageType = 35
	TypeValidation              MessageType = 41
	TypeGetObjects              MessageType = 42
	TypeValidatorList           MessageType = 54
	TypeSquelch                 MessageType = 55
	TypeValidatorListCollection MessageType = 56
	TypeProofPathReq            MessageType = 57
	TypeProofPathResponse       MessageType = 58
	TypeReplayDeltaReq          MessageType = 59
	TypeReplayDeltaResponse     MessageType = 60
	TypeHaveTransactions        MessageType = 63
	TypeTransactions            MessageType = 64
)

// String returns the string representation of a MessageType.
func (t MessageType) String() string {
	switch t {
	case TypeManifests:
		return "mtMANIFESTS"
	case TypePing:
		return "mtPING"
	case TypeCluster:
		return "mtCLUSTER"
	case TypeEndpoints:
		return "mtENDPOINTS"
	case TypeTransaction:
		return "mtTRANSACTION"
	case TypeGetLedger:
		return "mtGET_LEDGER"
	case TypeLedgerData:
		return "mtLEDGER_DATA"
	case TypeProposeLedger:
		return "mtPROPOSE_LEDGER"
	case TypeStatusChange:
		return "mtSTATUS_CHANGE"
	case TypeHaveSet:
		return "mtHAVE_SET"
	case TypeValidation:
		return "mtVALIDATION"
	case TypeGetObjects:
		return "mtGET_OBJECTS"
	case TypeValidatorList:
		return "mtVALIDATORLIST"
	case TypeSquelch:
		return "mtSQUELCH"
	case TypeValidatorListCollection:
		return "mtVALIDATORLISTCOLLECTION"
	case TypeProofPathReq:
		return "mtPROOF_PATH_REQ"
	case TypeProofPathResponse:
		return "mtPROOF_PATH_RESPONSE"
	case TypeReplayDeltaReq:
		return "mtREPLAY_DELTA_REQ"
	case TypeReplayDeltaResponse:
		return "mtREPLAY_DELTA_RESPONSE"
	case TypeHaveTransactions:
		return "mtHAVE_TRANSACTIONS"
	case TypeTransactions:
		return "mtTRANSACTIONS"
	default:
		return "mtUNKNOWN"
	}
}

// TransactionStatus represents the status of a transaction.
type TransactionStatus int32

const (
	TxStatusNew            TransactionStatus = 1
	TxStatusCurrent        TransactionStatus = 2
	TxStatusCommitted      TransactionStatus = 3
	TxStatusRejectConflict TransactionStatus = 4
	TxStatusRejectInvalid  TransactionStatus = 5
	TxStatusRejectFunds    TransactionStatus = 6
	TxStatusHeldSeq        TransactionStatus = 7
	TxStatusHeldLedger     TransactionStatus = 8
)

// NodeStatus represents the status of a node.
type NodeStatus int32

const (
	NodeStatusConnecting NodeStatus = 1
	NodeStatusConnected  NodeStatus = 2
	NodeStatusMonitoring NodeStatus = 3
	NodeStatusValidating NodeStatus = 4
	NodeStatusShutting   NodeStatus = 5
)

// NodeEvent represents an event on a node.
type NodeEvent int32

const (
	NodeEventClosingLedger  NodeEvent = 1
	NodeEventAcceptedLedger NodeEvent = 2
	NodeEventSwitchedLedger NodeEvent = 3
	NodeEventLostSync       NodeEvent = 4
)

// TxSetStatus represents the status of a transaction set.
type TxSetStatus int32

const (
	TxSetStatusHave   TxSetStatus = 1
	TxSetStatusCanGet TxSetStatus = 2
	TxSetStatusNeed   TxSetStatus = 3
)

// LedgerInfoType represents types of ledger information.
type LedgerInfoType int32

const (
	LedgerInfoBase        LedgerInfoType = 0
	LedgerInfoTxNode      LedgerInfoType = 1
	LedgerInfoAsNode      LedgerInfoType = 2
	LedgerInfoTsCandidate LedgerInfoType = 3
)

// LedgerQueryType represents the GetLedger query routing mode. qtINDIRECT
// is the only defined value; a present query_type that is anything else is
// rejected as invalid data. The field is modelled as a pointer on GetLedger
// so absence (the common case) is distinguishable from an explicit
// qtINDIRECT, matching rippled's has_querytype() presence check.
type LedgerQueryType int32

const (
	QueryTypeIndirect LedgerQueryType = 0
)

// LedgerType represents types of ledgers.
type LedgerType int32

const (
	LedgerTypeAccepted LedgerType = 0
	LedgerTypeCurrent  LedgerType = 1
	LedgerTypeClosed   LedgerType = 2
)

// ReplyError represents error codes in replies.
type ReplyError int32

const (
	ReplyErrorNone       ReplyError = 0
	ReplyErrorNoLedger   ReplyError = 1
	ReplyErrorNoNode     ReplyError = 2
	ReplyErrorBadRequest ReplyError = 3
)

// PingType represents the type of a ping message.
type PingType int32

const (
	PingTypePing PingType = 0
	PingTypePong PingType = 1
)

// ObjectType represents types of objects that can be requested.
type ObjectType int32

const (
	ObjectTypeUnknown         ObjectType = 0
	ObjectTypeLedger          ObjectType = 1
	ObjectTypeTransaction     ObjectType = 2
	ObjectTypeTransactionNode ObjectType = 3
	ObjectTypeStateNode       ObjectType = 4
	ObjectTypeCasObject       ObjectType = 5
	ObjectTypeFetchPack       ObjectType = 6
	ObjectTypeTransactions    ObjectType = 7
)

// LedgerMapType represents types of ledger maps.
type LedgerMapType int32

const (
	LedgerMapTransaction  LedgerMapType = 1
	LedgerMapAccountState LedgerMapType = 2
)
