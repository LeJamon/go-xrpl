package peermanagement

// BlacklistJSON returns the resource manager's per-endpoint reputation table
// filtered by threshold, shaped like rippled's ResourceManager::getJson
// (doBlackList): endpoint address → {local, remote, type}. A nil threshold
// applies resource.WarningThreshold, matching rippled's getJson() default.
func (o *Overlay) BlacklistJSON(threshold *int) map[string]any {
	if o == nil || o.resourceManager == nil {
		return map[string]any{}
	}
	return o.resourceManager.BlacklistJSON(threshold)
}
