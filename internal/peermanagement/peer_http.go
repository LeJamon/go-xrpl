package peermanagement

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type HTTPConfig struct {
	CrawlOverlay  bool
	CrawlServer   bool
	CrawlCounts   bool
	CrawlUNL      bool
	ValidatorList bool
}

// HTTPProviders return request-owned snapshots and may be called concurrently.
type HTTPProviders struct {
	ServerInfo func() map[string]any
	Counts     func() map[string]any
	UNL        func() map[string]any
	// A missing list returns false; an unsupported version returns nil, true.
	ValidatorList func(key string, version uint32) (map[string]any, bool)
}

func WithHTTPConfig(cfg HTTPConfig) Option {
	return func(c *Config) { c.HTTP = cfg }
}

func (o *Overlay) SetHTTPProviders(providers HTTPProviders) {
	o.providersMu.Lock()
	o.httpProviders = providers
	o.providersMu.Unlock()
}

var errInboundHTTPHandled = errors.New("peer HTTP request handled")

func (o *Overlay) processHTTPRequest(req *http.Request, w io.Writer) (bool, error) {
	o.providersMu.RLock()
	providers := o.httpProviders
	o.providersMu.RUnlock()
	cfg := o.cfg.HTTP
	status := http.StatusOK
	var body map[string]any
	switch {
	case req.RequestURI == "/crawl" && (cfg.CrawlOverlay || cfg.CrawlServer || cfg.CrawlCounts || cfg.CrawlUNL):
		body = map[string]any{"version": uint32(2)}
		if cfg.CrawlOverlay {
			body["overlay"] = o.crawlOverlay()
		}
		if cfg.CrawlServer {
			body["server"] = httpSnapshot(providers.ServerInfo)
		}
		if cfg.CrawlCounts {
			body["counts"] = httpSnapshot(providers.Counts)
		}
		if cfg.CrawlUNL {
			body["unl"] = httpSnapshot(providers.UNL)
		}
	case strings.HasPrefix(req.RequestURI, "/vl/") && cfg.ValidatorList:
		status, body = validatorListResponse(strings.TrimPrefix(req.RequestURI, "/vl/"), providers.ValidatorList)
	case req.RequestURI == "/health":
		var err error
		status, body, err = healthResponse(httpSnapshot(providers.ServerInfo))
		if err != nil {
			return true, err
		}
	case req.RequestURI != "/" && validateHandshakeRequest(req) != nil:
		// The root target retains the existing peer-handshake error handling.
		return true, o.writeHTTPResponse(req, w, http.StatusForbidden, "application/json; charset=UTF-8", []byte("Forbidden\r\n"))
	default:
		return false, nil
	}
	data, err := json.Marshal(body)
	if err != nil {
		return true, err
	}
	return true, o.writeHTTPResponse(req, w, status, "application/json", data)
}

func (o *Overlay) writeHTTPResponse(req *http.Request, w io.Writer, status int, contentType string, data []byte) error {
	resp := &http.Response{
		StatusCode: status,
		ProtoMajor: req.ProtoMajor,
		ProtoMinor: req.ProtoMinor,
		Header: http.Header{
			"Server":       {o.cfg.UserAgent},
			"Content-Type": {contentType},
			"Connection":   {"close"},
		},
		Body:          io.NopCloser(bytes.NewReader(data)),
		ContentLength: int64(len(data)),
		Close:         true,
	}
	if status == http.StatusForbidden {
		resp.ProtoMajor, resp.ProtoMinor = 1, 1
		resp.Close = false
		resp.Header.Set("Connection", "Keep-Alive")
		resp.Header.Set("Date", time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 +0000"))
	}
	return resp.Write(w)
}

func httpSnapshot(provider func() map[string]any) map[string]any {
	if provider != nil {
		if snapshot := provider(); snapshot != nil {
			return snapshot
		}
	}
	return map[string]any{}
}

func validatorListResponse(target string, provider func(string, uint32) (map[string]any, bool)) (int, map[string]any) {
	version := uint32(1)
	key := target
	if prefix, rest, ok := strings.Cut(target, "/"); ok {
		negative := strings.HasPrefix(prefix, "-")
		if strings.HasPrefix(prefix, "+") || negative {
			prefix = prefix[1:]
		}
		parsed, err := strconv.ParseUint(prefix, 10, 32)
		if err != nil {
			return http.StatusBadRequest, nil
		}
		version, key = uint32(parsed), rest
		if negative {
			version = -version
		}
	}
	if key == "" {
		return http.StatusBadRequest, nil
	}
	if provider == nil {
		return http.StatusNotFound, nil
	}
	list, found := provider(key, version)
	if !found {
		return http.StatusNotFound, nil
	}
	if list == nil {
		return http.StatusBadRequest, nil
	}
	return http.StatusOK, list
}

func healthResponse(server map[string]any) (int, map[string]any, error) {
	var snapshot struct {
		ValidatedLedger *struct {
			Age int `json:"age"`
		} `json:"validated_ledger"`
		Peers            int     `json:"peers"`
		ServerState      string  `json:"server_state"`
		LoadFactorServer float64 `json:"load_factor_server"`
		LoadBase         float64 `json:"load_base"`
	}
	data, err := json.Marshal(server)
	if err != nil {
		return 0, nil, err
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return 0, nil, err
	}
	severity := 0
	info := map[string]any{}
	age := -1
	if snapshot.ValidatedLedger != nil {
		age = snapshot.ValidatedLedger.Age
	}
	if age >= 7 || age < 0 {
		info["validated_ledger"] = age
		severity = 1
		if age >= 20 {
			severity = 2
		}
	}
	if _, blocked := server["amendment_blocked"]; blocked {
		info["amendment_blocked"] = true
		severity = 2
	}
	if snapshot.Peers <= 7 {
		info["peers"] = snapshot.Peers
		severity = max(severity, 1)
		if snapshot.Peers == 0 {
			severity = 2
		}
	}
	switch snapshot.ServerState {
	case "full", "validating", "proposing":
	case "syncing", "tracking", "connected":
		info["server_state"] = snapshot.ServerState
		severity = max(severity, 1)
	default:
		info["server_state"] = snapshot.ServerState
		severity = 2
	}
	load := snapshot.LoadFactorServer / snapshot.LoadBase
	if load > 100 {
		info["load_factor"] = load
		severity = max(severity, 1)
		if load >= 1000 {
			severity = 2
		}
	}
	status := [...]int{http.StatusOK, http.StatusServiceUnavailable, http.StatusInternalServerError}[severity]
	return status, map[string]any{"info": info}, nil
}

func (o *Overlay) crawlOverlay() map[string]any {
	o.peersMu.RLock()
	peers := make([]*Peer, 0, len(o.peers))
	for _, peer := range o.peers {
		peers = append(peers, peer)
	}
	o.peersMu.RUnlock()
	active := make([]map[string]any, 0, len(peers))
	for _, peer := range peers {
		remote, hasRemote := peer.remoteEndpoint()
		peer.mu.RLock()
		entry := map[string]any{
			"public_key": "",
			"type":       "out",
			"uptime":     uint32(max(0, time.Since(peer.createdAt)/time.Second)),
		}
		if peer.remotePubKey != nil {
			entry["public_key"] = base64.StdEncoding.EncodeToString(peer.remotePubKey.Bytes())
		}
		if peer.inbound {
			entry["type"] = "in"
		}
		if peer.crawlPublic {
			if !hasRemote {
				remote = peer.endpoint
			}
			entry["ip"] = remote.Host
			if !peer.inbound {
				entry["port"] = remote.Port
			} else if peer.listeningPort != 0 {
				entry["port"] = peer.listeningPort
			}
		}
		if peer.userAgent != "" {
			entry["version"] = peer.userAgent
		}
		if peer.firstLedgerSeq != 0 || peer.lastLedgerSeq != 0 {
			entry["complete_ledgers"] = fmt.Sprintf("%d-%d", peer.firstLedgerSeq, peer.lastLedgerSeq)
		}
		peer.mu.RUnlock()
		active = append(active, entry)
	}
	return map[string]any{"active": active}
}
