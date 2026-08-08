package types

import "errors"

// Peer-connect admission errors are returned synchronously by the runtime
// scheduler. They are kept in the RPC types package so handlers can classify
// them without importing the node package.
var (
	ErrPeerConnectQueueFull   = errors.New("peer connect queue is full")
	ErrPeerConnectClosed      = errors.New("peer connect scheduler is closed")
	ErrPeerConnectUnavailable = errors.New("peer connect scheduler is unavailable")
)
