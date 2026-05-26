package config

import "fmt"

// AmendmentsConfig represents the [amendments] section: the operator's
// amendment voting preferences. Entries are amendment names as defined in the
// amendment registry. Mirrors rippled's [amendments] (upvote) and
// [veto_amendments] (veto) stanzas.
type AmendmentsConfig struct {
	// Upvote lists amendments this node actively votes FOR, beyond the registry
	// defaults. Equivalent to rippled's [amendments] stanza.
	Upvote []string `toml:"upvote" mapstructure:"upvote"`

	// Veto lists amendments this node refuses to vote for. Equivalent to
	// rippled's [veto_amendments] stanza.
	Veto []string `toml:"veto" mapstructure:"veto"`
}

// Validate rejects an amendment listed in both upvote and veto, which is
// contradictory.
func (a *AmendmentsConfig) Validate() error {
	up := make(map[string]struct{}, len(a.Upvote))
	for _, n := range a.Upvote {
		up[n] = struct{}{}
	}
	for _, n := range a.Veto {
		if _, ok := up[n]; ok {
			return fmt.Errorf("amendment %q is listed in both upvote and veto", n)
		}
	}
	return nil
}

// IsEmpty returns true when no operator amendment preferences are configured.
func (a *AmendmentsConfig) IsEmpty() bool {
	return len(a.Upvote) == 0 && len(a.Veto) == 0
}
