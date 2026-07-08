package accountset

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	accounttx "github.com/LeJamon/go-xrpl/internal/tx/account"
)

// TestAccountSet_NFTokenMinterPrecedence pins finding AccountSet-order: setting
// asfAuthorizedNFTokenMinter without an sfNFTokenMinter field is temMALFORMED,
// enforced in the preflight body (gated on NonFungibleTokensV1, active in the
// default preset). It wins over the sequence gap that preclaim would otherwise
// report as terPRE_SEQ.
func TestAccountSet_NFTokenMinterPrecedence(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	// SetFlag asfAuthorizedNFTokenMinter with no NFTokenMinter present, plus a
	// future sequence: the field-pairing tem must beat the preclaim seq check.
	as := AccountSet(alice).
		SetFlag(accounttx.AccountSetFlagAuthorizedNFTokenMinter).
		Sequence(env.Seq(alice) + 10).
		Build()
	jtx.RequireTxFail(t, env.Submit(as), "temMALFORMED")
}
