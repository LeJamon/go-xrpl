// Package cluster maintains the registry of cluster-trusted node
// identities — operators run a small set of nodes that they configure
// to know about each other via [cluster_nodes]. A peer that completes
// a handshake under one of these node-pubkeys is treated as a cluster
// member by the peers RPC.
//
// Mirrors rippled's overlay::Cluster (rippled/src/xrpld/overlay/Cluster.h
// and Cluster.cpp). The rippled-side resource-charge relaxation and
// raw-relay fast-path that depend on cluster membership are out of
// scope for this package — we only mirror the membership state and
// the Cluster::load parser semantics.
package cluster

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
)

// Member is one entry in the cluster registry. Identity is the raw
// 33-byte node public key (post-addresscodec decode). Name, LoadFee
// and ReportTime mirror rippled's ClusterNode (ClusterNode.h:31-78).
type Member struct {
	Identity   []byte
	Name       string
	LoadFee    uint32
	ReportTime time.Time
}

// Registry is a thread-safe set of cluster members keyed by raw
// NodePublic bytes.
type Registry struct {
	mu    sync.RWMutex
	nodes map[string]Member
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{nodes: make(map[string]Member)}
}

// Member looks up an entry by raw NodePublic bytes. A nil receiver and
// an empty key both yield (zero, false). Mirrors rippled
// Cluster::member (Cluster.cpp:37-46) — a member with an empty Name
// still returns ok=true.
func (r *Registry) Member(identity []byte) (Member, bool) {
	if r == nil || len(identity) == 0 {
		return Member{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.nodes[string(identity)]
	return m, ok
}

// Size returns the number of registered members. Nil-safe.
func (r *Registry) Size() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.nodes)
}

// ForEach invokes fn once per member, in deterministic order (sorted
// by raw identity bytes). The read lock is held for the whole walk so
// fn must not call back into Update/Load — same restriction as rippled
// Cluster::for_each (Cluster.h:96-100).
func (r *Registry) ForEach(fn func(Member)) {
	if r == nil || fn == nil {
		return
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	keys := make([]string, 0, len(r.nodes))
	for k := range r.nodes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fn(r.nodes[k])
	}
}

// Update inserts or refreshes a cluster member, returning true if
// state was changed. Mirrors rippled Cluster::update (Cluster.cpp:56-80):
//   - a reportTime that does not strictly exceed the existing entry's
//     reportTime is rejected;
//   - a freshly-empty name preserves the previously-recorded name;
//   - the first insert always succeeds.
func (r *Registry) Update(identity []byte, name string, loadFee uint32, reportTime time.Time) bool {
	if len(identity) == 0 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	key := string(identity)
	if prev, exists := r.nodes[key]; exists {
		if !reportTime.After(prev.ReportTime) {
			return false
		}
		if name == "" {
			name = prev.Name
		}
	}
	r.nodes[key] = Member{
		Identity:   append([]byte(nil), identity...),
		Name:       name,
		LoadFee:    loadFee,
		ReportTime: reportTime,
	}
	return true
}

// entryRE matches one [cluster_nodes] line: an optional leading
// whitespace block, a base58-shaped node identity token, then an
// optional comment. Mirrors the boost::regex in rippled
// Cluster.cpp:93-103. The comment captures up to the last non-space
// rune so trailing whitespace is trimmed inside the regex itself, the
// way rippled does it.
var entryRE = regexp.MustCompile(`^\s*([A-Za-z0-9]+)(?:\s+(.*[^\s]+)\s*)?\s*$`)

// Load parses [cluster_nodes] entries. Each entry is a base58 node
// public key, optionally followed by a comment (the human-readable
// name). Mirrors rippled Cluster::load (Cluster.cpp:90-134):
//   - empty / whitespace-only entries are skipped;
//   - a malformed entry or invalid pubkey returns an error;
//   - a duplicate pubkey is silently skipped (rippled logs a warning).
func (r *Registry) Load(entries []string) error {
	if r == nil {
		return errors.New("cluster: nil registry")
	}
	for i, raw := range entries {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		groups := entryRE.FindStringSubmatch(raw)
		if groups == nil {
			return fmt.Errorf("cluster_nodes[%d]: malformed entry %q", i, raw)
		}
		idBytes, err := addresscodec.DecodeNodePublicKey(groups[1])
		if err != nil {
			return fmt.Errorf("cluster_nodes[%d]: invalid node identity %q: %w", i, groups[1], err)
		}
		if _, dup := r.Member(idBytes); dup {
			continue
		}
		r.Update(idBytes, strings.TrimSpace(groups[2]), 0, time.Time{})
	}
	return nil
}
