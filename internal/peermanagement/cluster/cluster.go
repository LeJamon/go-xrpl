// Package cluster maintains the registry of cluster-trusted node
// identities — operators run a small set of nodes that they configure
// to know about each other via [cluster_nodes]. A peer that completes
// a handshake under one of these node-pubkeys is treated as a cluster
// member by the peers RPC.
//
// The resource-charge relaxation and raw-relay fast-path that depend
// on cluster membership are out of scope for this package — it only
// holds the membership state and the [cluster_nodes] parser semantics.
package cluster

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/crypto"
)

// Identity is a fixed-width raw NodePublic key.
type Identity [addresscodec.NodePublicKeyLength]byte

// Member is one entry in the cluster registry.
type Member struct {
	Identity   Identity
	Name       string
	LoadFee    uint32
	ReportTime time.Time
}

// Registry is a thread-safe set of cluster members. Its zero value is ready to
// use. Nil receivers behave as empty registries; mutating them fails.
type Registry struct {
	mu    sync.RWMutex
	nodes map[Identity]Member
}

// New returns an empty registry.
func New() *Registry {
	return new(Registry)
}

func identityFromBytes(raw []byte) (Identity, bool) {
	if crypto.PublicKeyType(raw) == crypto.KeyTypeUnknown {
		return Identity{}, false
	}
	var identity Identity
	copy(identity[:], raw)
	return identity, true
}

// Member looks up an entry by raw NodePublic bytes. Invalid identities and nil
// receivers yield (zero, false). A member with an empty Name still returns
// ok=true.
func (r *Registry) Member(identity []byte) (Member, bool) {
	if r == nil {
		return Member{}, false
	}
	key, valid := identityFromBytes(identity)
	if !valid {
		return Member{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.nodes[key]
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

// ForEach invokes fn once per member from a stable snapshot, in raw identity
// byte order. Callbacks run without the registry lock and may call back into r;
// their updates are visible to later walks, not the current one.
func (r *Registry) ForEach(fn func(Member)) {
	if r == nil || fn == nil {
		return
	}
	r.mu.RLock()
	members := make([]Member, 0, len(r.nodes))
	for _, member := range r.nodes {
		members = append(members, member)
	}
	r.mu.RUnlock()

	slices.SortFunc(members, func(a, b Member) int {
		return bytes.Compare(a.Identity[:], b.Identity[:])
	})
	for _, member := range members {
		fn(member)
	}
}

// Update inserts or refreshes a cluster member, returning true if
// state was changed:
//   - a reportTime that does not strictly exceed the existing entry's
//     reportTime is rejected;
//   - a freshly-empty name preserves the previously-recorded name;
//   - the first insert always succeeds.
func (r *Registry) Update(identity []byte, name string, loadFee uint32, reportTime time.Time) bool {
	if r == nil {
		return false
	}
	key, valid := identityFromBytes(identity)
	if !valid {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if prev, exists := r.nodes[key]; exists {
		if !reportTime.After(prev.ReportTime) {
			return false
		}
		if name == "" {
			name = prev.Name
		}
	}
	if r.nodes == nil {
		r.nodes = make(map[Identity]Member)
	}
	r.nodes[key] = Member{
		Identity:   key,
		Name:       name,
		LoadFee:    loadFee,
		ReportTime: reportTime,
	}
	return true
}

// MedianFee returns the median LoadFee across members whose ReportTime
// is not older than thresh, and ok=true when at least one member
// qualified: stale members are dropped, the remaining loadFees are
// sorted and the middle element is taken.
func (r *Registry) MedianFee(thresh time.Time) (uint32, bool) {
	if r == nil {
		return 0, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	fees := make([]uint32, 0, len(r.nodes))
	for _, m := range r.nodes {
		if m.ReportTime.Before(thresh) {
			continue
		}
		fees = append(fees, m.LoadFee)
	}
	if len(fees) == 0 {
		return 0, false
	}
	slices.Sort(fees)
	return fees[len(fees)/2], true
}

// entryRE matches one [cluster_nodes] entry: a base58 identity plus
// an optional trailing comment. The POSIX [[:space:]] / [[:alnum:]]
// classes are load-bearing: Go's \s drops \v and other characters
// [[:space:]] matches, and rippled accepts those.
var entryRE = regexp.MustCompile(`^[[:space:]]*([[:alnum:]]+)(?:[[:space:]]+(?:(.*[^[:space:]]+)[[:space:]]*)?)?$`)

// Load parses [cluster_nodes] entries. Blank entries are skipped —
// rippled's config parser strips them before its loader runs, while
// go-xrpl's TOML []string can legally contain them, so we filter here
// to preserve the composition.
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
		if _, valid := identityFromBytes(idBytes); !valid {
			return fmt.Errorf(
				"cluster_nodes[%d]: invalid node identity %q: expected a %d-byte public key",
				i, groups[1], addresscodec.NodePublicKeyLength,
			)
		}
		if _, dup := r.Member(idBytes); dup {
			continue
		}
		r.Update(idBytes, strings.TrimSpace(groups[2]), 0, time.Time{})
	}
	return nil
}
