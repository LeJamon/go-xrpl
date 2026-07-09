package ledgerstatefix

import (
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	lsfpkg "github.com/LeJamon/go-xrpl/internal/tx/ledgerstatefix"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/stretchr/testify/require"
)

// setupBook funds accounts, creates an offer to materialise a book directory,
// and returns the book-directory root key and the quality it should encode.
func setupBook(t *testing.T, env *jtx.TestEnv) (alice *jtx.Account, bookKey [32]byte, quality uint64) {
	t.Helper()
	gw := jtx.NewAccount("gw")
	alice = jtx.NewAccount("alice")
	env.Fund(gw, alice)
	env.Close()

	offerSeq := env.Seq(alice)
	res := env.CreateOffer(alice, tx.NewXRPAmount(1_000_000_000), jtx.IssuedCurrency(gw, "USD", 100))
	require.Equal(t, "tesSUCCESS", res.Code, res.Message)
	env.Close()

	offerData, err := env.LedgerEntry(keylet.Offer(alice.ID, offerSeq))
	require.NoError(t, err)
	require.NotEmpty(t, offerData)
	offer, err := state.ParseLedgerOffer(offerData)
	require.NoError(t, err)
	bookKey = offer.BookDirectory
	quality = binary.BigEndian.Uint64(bookKey[24:])
	return alice, bookKey, quality
}

func readBookExchangeRate(t *testing.T, env *jtx.TestEnv, bookKey [32]byte) uint64 {
	t.Helper()
	kl := keylet.Keylet{Type: entry.TypeDirectoryNode, Key: bookKey}
	data, err := env.Ledger().Read(kl)
	require.NoError(t, err)
	require.NotEmpty(t, data)
	dir, err := state.ParseDirectoryNode(data)
	require.NoError(t, err)
	return dir.ExchangeRate
}

func corruptBookExchangeRate(t *testing.T, env *jtx.TestEnv, bookKey [32]byte, wrong uint64) {
	t.Helper()
	kl := keylet.Keylet{Type: entry.TypeDirectoryNode, Key: bookKey}
	data, err := env.Ledger().Read(kl)
	require.NoError(t, err)
	dir, err := state.ParseDirectoryNode(data)
	require.NoError(t, err)
	dir.ExchangeRate = wrong
	corrupt, err := state.SerializeDirectoryNode(dir, true)
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Update(kl, corrupt))
}

func submitBookFix(env *jtx.TestEnv, acc *jtx.Account, bookKey [32]byte) jtx.TxResult {
	l := lsfpkg.NewBookExchangeRateFix(acc.Address, strings.ToUpper(hex.EncodeToString(bookKey[:])))
	l.Fee = strconv.FormatUint(env.ReserveIncrement(), 10)
	seq := env.Seq(acc)
	l.Sequence = &seq
	return env.Submit(l)
}

func TestBookExchangeRateFix_RepairsRate(t *testing.T) {
	env := jtx.NewTestEnv(t) // fixCleanup3_2_0 enabled by default (SupportedYes)
	alice, bookKey, quality := setupBook(t, env)

	// Freshly-created book directory already has the correct exchange rate.
	require.Equal(t, quality, readBookExchangeRate(t, env, bookKey))

	// Corrupt it, then repair via the fix.
	corruptBookExchangeRate(t, env, bookKey, quality^0xABCD)
	require.NotEqual(t, quality, readBookExchangeRate(t, env, bookKey))

	res := submitBookFix(env, alice, bookKey)
	require.Equal(t, "tesSUCCESS", res.Code, res.Message)
	env.Close()

	require.Equal(t, quality, readBookExchangeRate(t, env, bookKey))
}

func TestBookExchangeRateFix_AlreadyCorrect(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice, bookKey, _ := setupBook(t, env)

	// No corruption: the rate already matches, so there is nothing to fix.
	res := submitBookFix(env, alice, bookKey)
	require.Equal(t, "tecNO_PERMISSION", res.Code, res.Message)
}

func TestBookExchangeRateFix_MissingDirectory(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	var missing [32]byte
	missing[0] = 0xDE
	res := submitBookFix(env, alice, missing)
	require.Equal(t, "tecOBJECT_NOT_FOUND", res.Code, res.Message)
}

func TestBookExchangeRateFix_AmendmentDisabled(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.DisableFeature("fixCleanup3_2_0")
	env.Close()

	var anyKey [32]byte
	anyKey[0] = 0x01
	res := submitBookFix(env, alice, anyKey)
	require.Equal(t, "temDISABLED", res.Code, res.Message)
}
