package testing

import (
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
)

// EnableFeature enables an amendment by name for subsequent transactions.
// Matches rippled's Env::enableFeature() in test/jtx/impl/Env.cpp: the
// amendment is staged into the rules builder and only takes effect after the
// next Close() — call e.Close() before submitting transactions that depend on
// the new amendment.
func (e *TestEnv) EnableFeature(name string) {
	e.t.Helper()
	e.rulesBuilder.EnableByName(name)
}

// DisableFeature disables an amendment by name for subsequent transactions.
// See EnableFeature for the Close()-required semantic.
// Reference: rippled's Env::disableFeature() in test/jtx/impl/Env.cpp.
func (e *TestEnv) DisableFeature(name string) {
	e.t.Helper()
	e.rulesBuilder.DisableByName(name)
}

// SetVerifySignatures enables or disables signature verification in the engine.
func (e *TestEnv) SetVerifySignatures(verify bool) {
	e.VerifySignatures = verify
}

// SetNetworkID sets the network identifier for the test environment.
// Networks with ID > 1024 require NetworkID in transactions.
// Networks with ID <= 1024 are legacy networks and cannot have NetworkID in transactions.
// Reference: rippled's Config::NETWORK_ID
func (e *TestEnv) SetNetworkID(id uint32) {
	e.networkID = id
}

// FeatureEnabled returns true if the named amendment is currently enabled.
// Reference: rippled's Env::enabled() in test/jtx/Env.h
func (e *TestEnv) FeatureEnabled(name string) bool {
	f := amendment.GetFeatureByName(name)
	if f == nil {
		return false
	}
	rules := e.rulesBuilder.Build()
	return rules.Enabled(f.ID)
}

// Now returns the current time on the test clock.
func (e *TestEnv) Now() time.Time {
	return e.clock.Now()
}

// AdvanceTime advances the test clock by the specified duration.
func (e *TestEnv) AdvanceTime(d time.Duration) {
	e.clock.Advance(d)
}

// SetTime sets the test clock to a specific time.
func (e *TestEnv) SetTime(t time.Time) {
	e.clock.Set(t)
}
