package peermanagement

import (
	"bytes"
	"log/slog"
	"strconv"
	"time"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/cluster"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

// clusterFeeRef is the load-fee reference baseline. Replace with a
// live reference once go-xrpl grows a load-fee tracker.
const clusterFeeRef uint32 = 256

// PeersJSON implements types.PeerSource for the `peers` RPC method,
// emitting the subset of rippled's per-peer RPC fields for which
// go-xrpl has data.
func (o *Overlay) PeersJSON() []map[string]any {
	list := o.Peers()
	out := make([]map[string]any, 0, len(list))
	for _, p := range list {
		entry := map[string]any{
			"address":    p.Endpoint.String(),
			"public_key": p.PublicKey,
			"uptime":     int64(time.Since(p.ConnectedAt).Seconds()),
			"load":       p.Load,
		}
		if p.Inbound {
			entry["inbound"] = true
		}
		if p.ServerDomain != "" {
			entry["server_domain"] = p.ServerDomain
		}
		// Emit only when the peer set a Network-ID.
		if p.NetworkID != "" {
			entry["network_id"] = p.NetworkID
		}
		if p.ClosedLedger != "" {
			entry["ledger"] = p.ClosedLedger
		}
		if p.CompleteLedgers != "" {
			entry["complete_ledgers"] = p.CompleteLedgers
		}
		if len(p.PublicKeyBytes) > 0 {
			if member, ok := o.cluster.Member(p.PublicKeyBytes); ok {
				entry["cluster"] = true
				if member.Name != "" {
					entry["name"] = member.Name
				}
			}
		}
		// Omit when converged.
		switch p.Tracking {
		case PeerTrackingDiverged:
			entry["track"] = "diverged"
		case PeerTrackingUnknown:
			entry["track"] = "unknown"
		}
		if p.HasLatency {
			entry["latency"] = uint32(p.Latency / time.Millisecond)
		}
		// Version sourced from User-Agent (inbound) or Server
		// (outbound) header.
		if p.Version != "" {
			entry["version"] = p.Version
		}
		// Emit unconditionally — a negotiated value always exists once
		// the handshake has completed.
		entry["protocol"] = p.Protocol
		// Emit only when the peer has reported a status.
		if s, known := nodeStatusRPCName(p.Status); s != "" {
			entry["status"] = s
		} else if !known && p.Status != 0 {
			// Log a warning when the status falls outside the known
			// enum, then drop the field so out-of-range values aren't
			// silent.
			slog.Warn("Unknown peer status",
				"t", "Overlay", "peer", p.ID, "status", int32(p.Status))
		}
		// Emit the metrics object; values are decimal strings to match
		// rippled's formatting.
		entry["metrics"] = map[string]any{
			"total_bytes_recv": strconv.FormatUint(p.TotalBytesRecv, 10),
			"total_bytes_sent": strconv.FormatUint(p.TotalBytesSent, 10),
			"avg_bps_recv":     strconv.FormatUint(p.AvgBpsRecv, 10),
			"avg_bps_sent":     strconv.FormatUint(p.AvgBpsSent, 10),
			"send_drops":       strconv.FormatUint(p.SendDrops, 10),
		}
		out = append(out, entry)
	}
	return out
}

// nodeStatusRPCName returns the rippled spelling for each known
// NodeStatus and a `known` flag distinguishing "no status reported"
// (nsUNKNOWN, known=true) from "unrecognized enum value" (known=false).
// The caller omits the `status` field for either case but logs only
// the unknown-enum case.
func nodeStatusRPCName(s message.NodeStatus) (string, bool) {
	switch s {
	case 0:
		return "", true
	case message.NodeStatusConnecting:
		return "connecting", true
	case message.NodeStatusConnected:
		return "connected", true
	case message.NodeStatusMonitoring:
		return "monitoring", true
	case message.NodeStatusValidating:
		return "validating", true
	case message.NodeStatusShutting:
		return "shutting", true
	default:
		return "", false
	}
}

// ClusterJSON returns the top-level cluster object for the `peers`
// RPC response.
func (o *Overlay) ClusterJSON() map[string]any {
	out := map[string]any{}
	if o == nil || o.cluster == nil {
		return out
	}

	var selfKey []byte
	if o.identity != nil {
		selfKey = o.identity.PublicKey()
	}

	now := o.cfg.Clock()

	o.cluster.ForEach(func(m cluster.Member) {
		if len(selfKey) > 0 && bytes.Equal(selfKey, m.Identity) {
			return
		}
		encoded, err := addresscodec.EncodeNodePublicKey(m.Identity)
		if err != nil || encoded == "" {
			return
		}
		entry := map[string]any{}
		if m.Name != "" {
			entry["tag"] = m.Name
		}
		if m.LoadFee != clusterFeeRef && m.LoadFee != 0 {
			entry["fee"] = float64(m.LoadFee) / float64(clusterFeeRef)
		}
		if !m.ReportTime.IsZero() {
			age := max(int64(now.Sub(m.ReportTime).Seconds()), 0)
			entry["age"] = age
		}
		out[encoded] = entry
	})
	return out
}
