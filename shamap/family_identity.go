package shamap

import "reflect"

// backingFamily returns the persistent Family used by this map. A Family
// wrapper may expose the durable Family it has promoted into; before that
// promotion the wrapper itself remains the identity so clean nodes are copied
// into the destination.
func (sm *SHAMap) backingFamily() Family {
	if sm == nil {
		return nil
	}
	sm.backing.mu.RLock()
	access := sm.backing.access
	sm.backing.mu.RUnlock()
	if access == nil || !access.available() {
		return nil
	}
	family := access.Family
	if provider, ok := family.(interface{ BackingFamily() Family }); ok {
		if promoted := provider.BackingFamily(); promoted != nil {
			return promoted
		}
	}
	return family
}

// UsesFamily reports whether a backed map can use family without copying clean
// nodes. Non-comparable Family values are conservatively treated as distinct;
// StoreAllBatched remains correct for that case.
func (sm *SHAMap) UsesFamily(family Family) bool {
	if sm == nil || !sm.IsBacked() {
		return false
	}
	return sameFamily(sm.backingFamily(), family)
}

func sameFamily(left, right Family) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	leftValue, rightValue := reflect.ValueOf(left), reflect.ValueOf(right)
	if leftValue.Type() != rightValue.Type() || !leftValue.Comparable() {
		return false
	}
	return leftValue.Interface() == rightValue.Interface()
}
