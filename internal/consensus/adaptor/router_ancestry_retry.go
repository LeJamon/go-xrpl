package adaptor

import "time"

// Caller holds headerDiscoveryMu. Repeated notifications for the same failure
// must not move the cooldown forward indefinitely.
func (s *headerDiscoverySession) markUnavailable(now time.Time) {
	s.pending = false
	s.terminal = true
	if s.repairAfter.IsZero() && s.repairRound < headerDiscoveryMaxRepairs {
		s.repairAfter = now.Add(headerDiscoveryRepairBackoff << s.repairRound)
	}
}

// Header repair has its own bounded deadline/backoff budget. Do not discard a
// verified replay base while that budget is still doing useful recovery work.
func (r *Router) headerDiscoveryRepairPending(now time.Time) bool {
	r.headerDiscoveryMu.Lock()
	defer r.headerDiscoveryMu.Unlock()
	s := r.headerDiscovery
	if s == nil {
		return false
	}
	if !s.terminal {
		return now.Before(s.deadline)
	}
	return !s.repairAfter.IsZero() && now.Before(s.repairAfter.Add(headerDiscoveryDeadline))
}
