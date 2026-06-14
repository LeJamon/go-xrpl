package testing

import (
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
)

// EnableFeature stages an amendment to be enabled on the next Close(), matching
// rippled's Env::enableFeature() (test/jtx/impl/Env.cpp), which requires close()
// for the change to take effect — call e.Close() before submitting transactions
// that depend on the amendment. An unknown amendment name fails the test
// immediately rather than being silently dropped.
func (e *TestEnv) EnableFeature(name string) {
	e.t.Helper()
	e.requireKnownAmendment(name)
	e.pendingEnable = append(e.pendingEnable, name)
}

// DisableFeature stages an amendment to be disabled on the next Close().
// See EnableFeature for the Close()-required semantic and name validation.
// Reference: rippled's Env::disableFeature() in test/jtx/impl/Env.cpp.
func (e *TestEnv) DisableFeature(name string) {
	e.t.Helper()
	e.requireKnownAmendment(name)
	e.pendingDisable = append(e.pendingDisable, name)
}

// EnableFeatureNow enables an amendment and applies it immediately, without
// waiting for a Close(). It exists for the conformance runner, whose recorded
// enable_amendment fixture steps expect the amendment to be active for the very
// next transaction in the same ledger. Ordinary tests should use EnableFeature,
// which defers to the next Close() like rippled's Env::enableFeature.
func (e *TestEnv) EnableFeatureNow(name string) {
	e.t.Helper()
	e.requireKnownAmendment(name)
	e.rulesBuilder.EnableByName(name)
}

// requireKnownAmendment fails the test if name is not a registered amendment.
// Used by EnableFeature/DisableFeature/SetAmendments so a typo'd amendment name
// surfaces loudly instead of silently running the test against the wrong rules.
func (e *TestEnv) requireKnownAmendment(name string) {
	e.t.Helper()
	if amendment.GetFeatureByName(name) == nil {
		e.t.Fatalf("unknown amendment name %q", name)
	}
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

// FeatureEnabled returns true if the named amendment will be enabled once the
// staged toggles take effect at the next Close(). Reading it right after
// EnableFeature/DisableFeature therefore reflects the staged change, even though
// the engine rules themselves only switch at Close.
// Reference: rippled's Env::enabled() in test/jtx/Env.h
func (e *TestEnv) FeatureEnabled(name string) bool {
	f := amendment.GetFeatureByName(name)
	if f == nil {
		return false
	}
	return e.pendingRulesBuilder().Build().Enabled(f.ID)
}

// Now returns the current time on the test clock.
func (e *TestEnv) Now() time.Time {
	return e.clock.Now()
}

// NowRipple returns the current test-clock time as seconds since the Ripple
// epoch (the uint32 form used by on-ledger time fields such as Expiration,
// FinishAfter and CancelAfter).
func (e *TestEnv) NowRipple() uint32 {
	return toRippleTime(e.clock.Now())
}

// AdvanceTime advances the test clock by the specified duration.
func (e *TestEnv) AdvanceTime(d time.Duration) {
	e.clock.Advance(d)
}

// SetTime sets the test clock to a specific time.
func (e *TestEnv) SetTime(t time.Time) {
	e.clock.Set(t)
}
