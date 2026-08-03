package peermanagement

import "sync"

type FeatureSet struct {
	mu       sync.RWMutex
	features map[Feature]bool
}

func NewFeatureSet() *FeatureSet {
	return &FeatureSet{
		features: make(map[Feature]bool),
	}
}

func DefaultFeatureSet() *FeatureSet {
	fs := NewFeatureSet()
	fs.Enable(FeatureCompression)
	fs.Enable(FeatureReduceRelay)
	fs.Enable(FeatureValidatorListPropagation)
	return fs
}

func (fs *FeatureSet) Enable(f Feature) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.features[f] = true
}

func (fs *FeatureSet) Disable(f Feature) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	delete(fs.features, f)
}

func (fs *FeatureSet) Has(f Feature) bool {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	return fs.features[f]
}

func (fs *FeatureSet) List() []Feature {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	result := make([]Feature, 0, len(fs.features))
	for f := range fs.features {
		result = append(result, f)
	}
	return result
}

func (fs *FeatureSet) Intersect(other *FeatureSet) *FeatureSet {
	if fs == nil || other == nil {
		return NewFeatureSet()
	}
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	other.mu.RLock()
	defer other.mu.RUnlock()

	result := NewFeatureSet()
	for f := range fs.features {
		if other.features[f] {
			result.features[f] = true
		}
	}
	return result
}

func (fs *FeatureSet) clone() *FeatureSet {
	result := NewFeatureSet()
	if fs == nil {
		return result
	}
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	for feature := range fs.features {
		result.features[feature] = true
	}
	return result
}

// PeerCapabilities contains the features negotiated during the handshake.
type PeerCapabilities struct {
	mu       sync.RWMutex
	Features *FeatureSet
}

func NewPeerCapabilities() *PeerCapabilities {
	return &PeerCapabilities{
		Features: NewFeatureSet(),
	}
}

func (pc *PeerCapabilities) clone() *PeerCapabilities {
	if pc == nil {
		return nil
	}
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	return &PeerCapabilities{Features: pc.Features.clone()}
}

func (pc *PeerCapabilities) HasFeature(f Feature) bool {
	if pc == nil {
		return false
	}
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	if pc.Features == nil {
		return false
	}
	return pc.Features.Has(f)
}

func (pc *PeerCapabilities) SupportsCompression() bool {
	return pc.HasFeature(FeatureCompression)
}

func (pc *PeerCapabilities) SupportsReduceRelay() bool {
	return pc.HasFeature(FeatureReduceRelay)
}
