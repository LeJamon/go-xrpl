// Package config defines and loads the xrpld node configuration.
//
// The [Config] type mirrors the structure of rippled's rippled.cfg, grouped into
// the same sections (server and ports, peer protocol, Ripple protocol, HTTPS
// client, databases, validation, voting, and logging). Configuration is read from
// a TOML file via the loader, with [github.com/LeJamon/goXRPLd/config] defaults
// applied for any unset field, and validated before use.
//
// Several fields accept rippled's mixed integer/keyword forms (for example
// ledger_history as an integer, "full", or "none"; network_id as an integer or a
// named network) and are represented by dedicated types that decode either form.
package config
