package conformance

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/LeJamon/go-xrpl/protocol"
)

// Fixture represents a single xrpl-fixtures test vector file.
type Fixture struct {
	RippledVersion string     `json:"rippled_version"`
	Suite          string     `json:"suite"`
	Testcase       string     `json:"testcase"`
	DependsOn      string     `json:"depends_on,omitempty"`
	Env            *EnvConfig `json:"env,omitempty"`
	Steps          []Step     `json:"steps"`
}

// EnvConfig holds the ledger environment configuration.
type EnvConfig struct {
	AmendmentsEnabled []string `json:"amendments_enabled"`
	BaseFee           uint64   `json:"base_fee"`
	ReserveBase       uint64   `json:"reserve_base"`
	ReserveIncrement  uint64   `json:"reserve_increment"`
	NetworkID         *uint32  `json:"network_id,omitempty"`
	InitialLedgerSeq  *uint32  `json:"initial_ledger_seq,omitempty"`
}

// Step represents a single operation in a fixture.
type Step struct {
	Op               string          `json:"op"`
	Account          string          `json:"account,omitempty"`
	Address          string          `json:"address,omitempty"`
	Amount           json.RawMessage `json:"amount,omitempty"`
	SetDefaultRipple *bool           `json:"set_default_ripple,omitempty"`
	LimitAmount      *LimitAmount    `json:"limit_amount,omitempty"`
	TxBlob           string          `json:"tx_blob,omitempty"`
	TxJSON           json.RawMessage `json:"tx_json,omitempty"`
	ExpectTER        string          `json:"expect_ter,omitempty"`
	PostState        *PostState      `json:"post_state,omitempty"`
	Env              *EnvConfig      `json:"env,omitempty"`
	Amendment        string          `json:"amendment,omitempty"`
	ModifyState      *ModifyState    `json:"modify_state,omitempty"`
	CloseTime        *uint32         `json:"close_time,omitempty"`
	LedgerSeq        *uint32         `json:"ledger_seq,omitempty"`
	ParentCloseTime  *uint32         `json:"parent_close_time,omitempty"`
	TxSetHash        *string         `json:"tx_set_hash,omitempty"`
}

// ModifyState describes direct ledger state modifications that bypass normal
// transaction processing.  This is used when rippled tests hack the open
// ledger (via env.app().openLedger().modify()) to set up boundary conditions
// that cannot be reached through regular transactions (e.g., setting
// MintedNFTokens to 0xFFFFFFFE to test overflow detection).
type ModifyState struct {
	Account              string        `json:"account"`
	MintedNFTokens       *uint32       `json:"minted_nftokens,omitempty"`
	FirstNFTokenSequence *uint32       `json:"first_nftoken_sequence,omitempty"`
	BumpLastPage         *BumpLastPage `json:"bump_last_page,omitempty"`
}

// BumpLastPage describes a directory page bump operation.
// This mirrors rippled's test::jtx::directory::bumpLastPage() which moves
// the last page of a directory to a target page number near the limit,
// allowing tests to exercise the directory page limit check.
type BumpLastPage struct {
	Directory   string `json:"directory"`    // "owner" for owner directory
	TargetPage  uint64 `json:"-"`            // New page number for the last page (parsed from string or number)
	AdjustField string `json:"adjust_field"` // SLE field to update on moved entries (e.g. "IssuerNode")
}

// UnmarshalJSON implements custom unmarshaling for BumpLastPage to handle
// target_page as either a JSON string or number. v2 fixtures serialize
// uint64 values as strings.
func (b *BumpLastPage) UnmarshalJSON(data []byte) error {
	// Use an alias to avoid infinite recursion
	type Alias struct {
		Directory   string          `json:"directory"`
		TargetPage  json.RawMessage `json:"target_page"`
		AdjustField string          `json:"adjust_field"`
	}
	var a Alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	b.Directory = a.Directory
	b.AdjustField = a.AdjustField

	if len(a.TargetPage) > 0 {
		// Try as string first (quoted number)
		var s string
		if err := json.Unmarshal(a.TargetPage, &s); err == nil {
			val, err := strconv.ParseUint(s, 10, 64)
			if err != nil {
				return fmt.Errorf("invalid target_page string %q: %w", s, err)
			}
			b.TargetPage = val
			return nil
		}
		// Try as number
		var n uint64
		if err := json.Unmarshal(a.TargetPage, &n); err == nil {
			b.TargetPage = n
			return nil
		}
		return fmt.Errorf("cannot parse target_page: %s", string(a.TargetPage))
	}
	return nil
}

// LimitAmount is an IOU amount for trust line setup.
type LimitAmount struct {
	Currency string `json:"currency"`
	Issuer   string `json:"issuer"`
	Value    string `json:"value"`
}

// PostState holds expected post-transaction account states.
type PostState struct {
	Accounts []AccountState `json:"accounts"`
}

// AccountState holds expected account state after a transaction.
type AccountState struct {
	Name       string  `json:"name"`
	Address    string  `json:"address"`
	XRPBalance string  `json:"xrp_balance"`
	OwnerCount uint32  `json:"owner_count"`
	Sequence   *uint32 `json:"sequence,omitempty"`
	Flags      *uint32 `json:"flags,omitempty"`
}

// rippleEpoch is Jan 1, 2000 00:00:00 UTC as a time.Time value, derived
// from the canonical protocol.RippleEpochUnix constant.
var rippleEpoch = time.Unix(protocol.RippleEpochUnix, 0).UTC()
