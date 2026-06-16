package adaptor

import (
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

// maybeRelayGetLedger forwards an unsatisfiable GetLedger to a peer that
// advertises the requested ledger (liBASE / liAS_NODE / liTX_NODE) or
// tx-set (liTS_CANDIDATE), mirroring rippled's getLedger / getTxSet relay.
// It returns true when the request was relayed, in which case the caller
// must stop processing it locally.
//
// Only an ORIGINAL, indirect request is relayed: query_type present AND
// request_cookie absent — rippled's has_querytype() && !has_requestcookie()
// predicate. A request that already carries a cookie has been relayed once
// and is never relayed again (loop prevention); fan-out is capped at the
// single best peer. The relayed frame is stamped with
// request_cookie = our local id for the original requester so the eventual
// TMLedgerData reply routes back through routeRelayedLedgerData.
func (r *Router) maybeRelayGetLedger(from peermanagement.PeerID, req *message.GetLedger) bool {
	if req.QueryType == nil || req.RequestCookie != 0 {
		return false
	}

	var target [32]byte
	hasHash := len(req.LedgerHash) == 32
	if hasHash {
		copy(target[:], req.LedgerHash)
	}

	var peer uint64
	var ok bool
	if req.InfoType == message.LedgerInfoTsCandidate {
		// A tx-set is located only by its root hash.
		if !hasHash {
			return false
		}
		peer, ok = r.adaptor.PeerWithTxSet(target, uint64(from))
	} else {
		// rippled relays a ledger request only from its has_ledgerhash()
		// branch (getLedger, PeerImp.cpp:3165/3175); a seq-only miss is
		// never relayed. Require a hash, then pass the seq as the secondary
		// range filter exactly as getPeerWithLedger(hash, seq) does.
		if !hasHash {
			return false
		}
		peer, ok = r.adaptor.PeerWithLedger(target, req.LedgerSeq, uint64(from))
	}
	if !ok {
		return false
	}

	relayed := *req
	relayed.RequestCookie = uint64(from)
	frame, err := encodeFrame(message.TypeGetLedger, &relayed)
	if err != nil {
		r.logger.Warn("failed to encode relayed get_ledger", "error", err)
		return false
	}
	if err := r.adaptor.SendToPeer(peer, frame); err != nil {
		r.logger.Debug("failed to relay get_ledger",
			"error", err, "to", peer, "from", from)
		return false
	}
	r.logger.Debug("relayed get_ledger to peer with data",
		"from", from, "to", peer, "itype", req.InfoType)
	return true
}

// routeRelayedLedgerData forwards a TMLedgerData carrying a request_cookie
// back to the original requester named by the cookie, clearing the cookie
// so the requester consumes the reply locally rather than re-relaying it.
// Mirrors rippled onMessage(TMLedgerData)'s findPeerByShortID branch: an
// unroutable cookie (the requester has since disconnected) is dropped.
func (r *Router) routeRelayedLedgerData(ld *message.LedgerData, from peermanagement.PeerID) {
	target := uint64(ld.RequestCookie)
	out := *ld
	out.RequestCookie = 0
	frame, err := encodeFrame(message.TypeLedgerData, &out)
	if err != nil {
		r.logger.Warn("failed to encode relayed ledger_data", "error", err)
		return
	}
	if err := r.adaptor.SendToPeer(target, frame); err != nil {
		r.logger.Info("unable to route ledger_data reply to original requester",
			"error", err, "cookie", target, "from", from)
	}
}
