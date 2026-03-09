// Package permissioneddex provides test helpers for PermissionedDEX tests.
// Reference: rippled/src/test/jtx/impl/permissioned_dex.cpp
package permissioneddex

import (
	"encoding/hex"
	"testing"

	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/tx"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	cred "github.com/LeJamon/goXRPLd/internal/testing/credential"
	pd "github.com/LeJamon/goXRPLd/internal/testing/permissioneddomain"
	paymentBuilder "github.com/LeJamon/goXRPLd/internal/testing/payment"
	trustsetBuilder "github.com/LeJamon/goXRPLd/internal/testing/trustset"
)

// PermissionedDEXEnv holds the accounts and domain ID for a PermissionedDEX test environment.
// Reference: rippled PermissionedDEX struct in permissioned_dex.h
type PermissionedDEXEnv struct {
	GW          *jtx.Account
	DomainOwner *jtx.Account
	Alice       *jtx.Account
	Bob         *jtx.Account
	Carol       *jtx.Account
	DomainID    [32]byte
	DomainIDHex string
	CredType    string // hex-encoded credential type
}

// USD creates a USD IOU amount from the GW account.
func (e *PermissionedDEXEnv) USD(amount float64) tx.Amount {
	return jtx.USD(e.GW, amount)
}

// DomainIDHexStr returns the domain ID as a 64-char hex string.
func (e *PermissionedDEXEnv) DomainIDHexStr() string {
	return e.DomainIDHex
}

// SetupPermissionedDEX creates and initializes a PermissionedDEX test environment.
// Matches rippled's PermissionedDEX constructor in permissioned_dex.cpp.
//
// Accounts created:
//   - gw (gateway): issues USD
//   - domainOwner: creates the domain, credentials for members
//   - alice, bob, carol: domain members with USD trust lines (100 USD each)
//
// The domain has credentials issued by domainOwner for alice, bob, carol, gw.
func SetupPermissionedDEX(t *testing.T, env *jtx.TestEnv) *PermissionedDEXEnv {
	t.Helper()

	const credType = "7065726d6465782d6162636465" // hex("permdex-abcde")

	gw := jtx.NewAccount("permdex-gateway")
	domainOwner := jtx.NewAccount("permdex-domainOwner")
	alice := jtx.NewAccount("permdex-alice")
	bob := jtx.NewAccount("permdex-bob")
	carol := jtx.NewAccount("permdex-carol")

	// Fund all accounts with 100000 XRP (matching rippled's PermissionedDEX constructor)
	env.FundAmount(gw, uint64(jtx.XRP(100000)))
	env.FundAmount(alice, uint64(jtx.XRP(100000)))
	env.FundAmount(bob, uint64(jtx.XRP(100000)))
	env.FundAmount(carol, uint64(jtx.XRP(100000)))
	env.Close()

	// domainOwner is funded separately
	env.FundAmount(domainOwner, uint64(jtx.XRP(100000)))
	env.Close()

	// domainOwner creates domain with credentials
	domainSeq := env.Seq(domainOwner)
	result := env.Submit(
		pd.DomainSet(domainOwner).Credential(domainOwner, credType).Build(),
	)
	if !result.Success {
		t.Fatalf("SetupPermissionedDEX: failed to create domain: %s %s", result.Code, result.Message)
	}
	env.Close()

	// Compute domain ID
	domainKey := keylet.PermissionedDomain(domainOwner.ID, domainSeq)
	domainID := domainKey.Key
	domainIDHex := hex.EncodeToString(domainID[:])

	// Issue and accept credentials for alice, bob, carol, gw
	members := []*jtx.Account{alice, bob, carol, gw}
	for _, member := range members {
		result = env.Submit(cred.CredentialCreate(domainOwner, member, credType).Build())
		if !result.Success {
			t.Fatalf("SetupPermissionedDEX: credential create for %s: %s %s", member.Name, result.Code, result.Message)
		}
		env.Close()
		result = env.Submit(cred.CredentialAccept(member, domainOwner, credType).Build())
		if !result.Success {
			t.Fatalf("SetupPermissionedDEX: credential accept for %s: %s %s", member.Name, result.Code, result.Message)
		}
		env.Close()
	}

	// Set up USD trust lines for alice, bob, carol, domainOwner
	// Reference: rippled permissioned_dex.cpp: trust USD(1000), pay USD(100)
	usd100 := jtx.USD(gw, 100)
	for _, acc := range []*jtx.Account{alice, bob, carol, domainOwner} {
		result = env.Submit(trustsetBuilder.TrustLine(acc, "USD", gw, "1000").Build())
		if !result.Success {
			t.Fatalf("SetupPermissionedDEX: trust line for %s: %s %s", acc.Name, result.Code, result.Message)
		}
		env.Close()

		result = env.Submit(paymentBuilder.PayIssued(gw, acc, usd100).Build())
		if !result.Success {
			t.Fatalf("SetupPermissionedDEX: pay USD to %s: %s %s", acc.Name, result.Code, result.Message)
		}
		env.Close()
	}

	return &PermissionedDEXEnv{
		GW:          gw,
		DomainOwner: domainOwner,
		Alice:       alice,
		Bob:         bob,
		Carol:       carol,
		DomainID:    domainID,
		DomainIDHex: domainIDHex,
		CredType:    credType,
	}
}
