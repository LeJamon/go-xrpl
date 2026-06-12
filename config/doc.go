// Package config defines and loads the xrpld node configuration.
//
// The [Config] type mirrors the structure of rippled's rippled.cfg, grouped into
// the same sections (server and ports, peer protocol, Ripple protocol, databases,
// validation, voting, and logging). Configuration is read from a TOML file via
// the loader and validated before use. The loader injects no defaults: required
// keys must be present in the file, while optional tuning keys fall back to
// documented built-in defaults at their consumers.
//
// Several fields accept rippled's mixed integer/keyword forms (for example
// ledger_history as an integer, "full", or "none"; network_id as an integer or a
// named network) and are represented by dedicated types that decode either form.
package config
