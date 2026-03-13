package log

// discardLogger is a no-op Logger used in tests and as the default root.
// All methods are empty; With and Named return the same instance.
type discardLogger struct{}

// Discard returns a Logger that silently drops all records.
// Use this in test environments to keep test output clean.
func Discard() Logger { return discardLogger{} }

func (discardLogger) Trace(_ string, _ ...any) {}
func (discardLogger) Debug(_ string, _ ...any) {}
func (discardLogger) Info(_ string, _ ...any)  {}
func (discardLogger) Warn(_ string, _ ...any)  {}
func (discardLogger) Error(_ string, _ ...any) {}
func (discardLogger) Fatal(_ string, _ ...any) {}

func (d discardLogger) With(_ ...any) Logger  { return d }
func (d discardLogger) Named(_ string) Logger { return d }
