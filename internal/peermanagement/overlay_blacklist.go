package peermanagement

import "github.com/LeJamon/goXRPLd/internal/peermanagement/resource"

// BlacklistJSON returns the resource manager's per-endpoint reputation table
// filtered by threshold, shaped like rippled's ResourceManager::getJson
// (doBlackList): endpoint address → {local, remote, type}. A nil threshold
// applies resource.WarningThreshold, matching rippled's getJson() default.
func (o *Overlay) BlacklistJSON(threshold *int) map[string]any {
	t := resource.WarningThreshold
	if threshold != nil {
		t = *threshold
	}
	ret := make(map[string]any)
	for _, e := range o.resourceManager.Snapshot(t) {
		ret[e.Address] = map[string]any{
			"local":  e.Local,
			"remote": e.Remote,
			"type":   e.Type,
		}
	}
	return ret
}
