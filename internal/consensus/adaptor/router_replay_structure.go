package adaptor

// Includes map capacity at the prepared limit and transient growth overlap.
const standardReplayMapMemoryBytes = standardReplayPreparedLimit * 128

func (c *catchupReplayCoordinator) prepareStandardReplayEntriesLocked() bool {
	if c.standardReplay.entries != nil {
		return true
	}
	account := c.standardReplayMemoryAccount()
	if account != nil && !account.TryReserveReplayHead(standardReplayMapMemoryBytes) {
		return false
	}
	c.standardReplay.entriesMemory = account
	c.standardReplay.entries = make(map[uint32]*standardReplayEntry, standardReplayPreparedLimit)
	return true
}

func (c *catchupReplayCoordinator) dropStandardReplayEntriesLocked() {
	c.standardReplay.entries = nil
	if account := c.standardReplay.entriesMemory; account != nil {
		c.standardReplay.entriesMemory = nil
		account.ReleaseReplayHead(standardReplayMapMemoryBytes)
	}
}
