package vault

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

type sendAssetsOracleBalance struct {
	Account string `json:"account"`
	Balance string `json:"balance"`
}

type sendAssetsOracleRow struct {
	Label     string `json:"label"`
	Asset     string `json:"asset"`
	Sender    string `json:"sender"`
	Issuer    string `json:"issuer"`
	Currency  string `json:"currency"`
	Receivers []struct {
		Account string `json:"account"`
		Amount  string `json:"amount"`
	} `json:"receivers"`
	TER                  string                    `json:"ter"`
	SenderBefore         string                    `json:"sender_before"`
	SenderAfter          string                    `json:"sender_after"`
	ReceiverBefore       []sendAssetsOracleBalance `json:"receiver_before"`
	ReceiverAfter        []sendAssetsOracleBalance `json:"receiver_after"`
	OutstandingBefore    string                    `json:"outstanding_before"`
	OutstandingAfter     string                    `json:"outstanding_after"`
	FixCleanup313        bool                      `json:"fixCleanup3_1_3"`
	WaiveTransferFee     bool                      `json:"waive_transfer_fee"`
	MPTIssuanceID        string                    `json:"mpt_issuance_id"`
	MaximumAmount        string                    `json:"maximum_amount"`
	TransferFee          uint16                    `json:"transfer_fee"`
	MPTokensV2           bool                      `json:"MPTokensV2"`
	NumberScale          string                    `json:"number_scale"`
	UniversalNumber      bool                      `json:"universal_number"`
	SandboxBaseUnchanged bool                      `json:"sandbox_base_unchanged"`
}

func TestSendAssetsOracle(t *testing.T) {
	data, err := os.ReadFile("testdata/send_assets_oracle.json")
	require.NoError(t, err)
	var fixture struct {
		Commit string                `json:"commit"`
		Rows   []sendAssetsOracleRow `json:"rows"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.Equal(t, "d147fccf54a500fce586522f28d6044c37fd8d29", fixture.Commit)
	require.Len(t, fixture.Rows, 36)
	var replayed int
	for _, row := range fixture.Rows {
		// Both lending callers waive fees; nonwaived oracle rows are controls.
		if !row.WaiveTransferFee {
			continue
		}
		replayed++
		t.Run(row.Label, func(t *testing.T) {
			replaySendAssetsOracle(t, row)
		})
	}
	require.Equal(t, 32, replayed)
}

func replaySendAssetsOracle(t *testing.T, row sendAssetsOracleRow) {
	t.Helper()
	decodeAccount := func(address string) [20]byte {
		id, err := state.DecodeAccountID(address)
		require.NoError(t, err)
		return id
	}
	parseBalance := func(value string) uint64 {
		balance, err := strconv.ParseUint(strings.TrimSuffix(value, "/XRP"), 10, 64)
		require.NoError(t, err)
		return balance
	}
	rules := amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported)
	if row.FixCleanup313 {
		rules.Enable(amendment.FeatureFixCleanup3_1_3)
	} else {
		rules.Disable(amendment.FeatureFixCleanup3_1_3)
	}
	if row.MPTokensV2 {
		rules.Enable(amendment.FeatureMPTokensV2)
	} else {
		rules.Disable(amendment.FeatureMPTokensV2)
	}
	view := newMPTArmsView()
	sender := decodeAccount(row.Sender)
	ctx := buildArmsCtx(t, view, sender, rules.Build())
	require.Equal(t, row.FixCleanup313, ctx.Rules().Enabled(amendment.FeatureFixCleanup3_1_3))
	require.Equal(t, row.MPTokensV2, ctx.Rules().MPTokensV2Enabled())
	scale := state.MantissaScaleLarge
	if row.NumberScale == "small" {
		scale = state.MantissaScaleSmall
	} else {
		require.Equal(t, "large", row.NumberScale)
	}
	numbers := state.NewNumberContext(scale, row.UniversalNumber)
	ctx.Config.NumberContextOverride = &numbers
	asset := tx.Asset{Currency: "XRP"}
	var id [24]byte
	var issuer [20]byte
	if row.Asset == "MPT" {
		asset = tx.Asset{MPTIssuanceID: row.MPTIssuanceID}
		raw, err := hex.DecodeString(row.MPTIssuanceID)
		require.NoError(t, err)
		require.Len(t, raw, len(id))
		copy(id[:], raw)
		issuance := &state.MPTokenIssuanceData{
			Issuer:            mptIDIssuer(id),
			OutstandingAmount: parseBalance(row.OutstandingBefore),
			TransferFee:       row.TransferFee,
		}
		if row.MaximumAmount != "" {
			maximum := parseBalance(row.MaximumAmount)
			issuance.MaximumAmount = &maximum
		}
		blob, err := state.SerializeMPTokenIssuance(issuance)
		require.NoError(t, err)
		require.NoError(t, view.Insert(keylet.MPTIssuance(id), blob))
	} else if row.Asset == "IOU" {
		require.NotEmpty(t, row.Currency)
		asset = tx.Asset{Currency: row.Currency, Issuer: row.Issuer}
		issuer = decodeAccount(row.Issuer)
	} else {
		require.Equal(t, "XRP", row.Asset)
	}
	parseIOU := func(value string) state.Amount {
		text, _, _ := strings.Cut(value, "/")
		number, err := state.ParseXRPLNumber(text, scale, state.RoundToNearest)
		require.NoError(t, err)
		prototype := state.NewIssuedAmountFromValue(0, 0, asset.Currency, state.AccountOneAddress)
		return numbers.ToAmount(number, prototype, state.RoundToNearest)
	}
	for _, balance := range append([]sendAssetsOracleBalance{{row.Sender, row.SenderBefore}}, row.ReceiverBefore...) {
		if balance.Balance == "missing" {
			continue
		}
		accountID := decodeAccount(balance.Account)
		if row.Asset == "MPT" {
			token := &state.MPTokenData{Account: accountID, MPTokenIssuanceID: id, MPTAmount: parseBalance(balance.Balance)}
			blob, err := state.SerializeMPToken(token)
			require.NoError(t, err)
			require.NoError(t, view.Insert(keylet.MPTokenByID(id, accountID), blob))
		} else if row.Asset == "IOU" {
			low, high := balance.Account, row.Issuer
			amount := parseIOU(balance.Balance)
			if state.CompareAccountIDs(accountID, issuer) > 0 {
				low, high = high, low
				amount = amount.Negate()
			}
			line := &state.RippleState{
				Balance:   amount,
				LowLimit:  state.NewIssuedAmountFromValue(1, 18, asset.Currency, low),
				HighLimit: state.NewIssuedAmountFromValue(1, 18, asset.Currency, high),
			}
			blob, err := state.SerializeRippleState(line)
			require.NoError(t, err)
			require.NoError(t, view.Insert(keylet.Line(accountID, issuer, asset.Currency), blob))
		} else {
			account := &state.AccountRoot{Account: balance.Account, Balance: parseBalance(balance.Balance), Sequence: 1}
			blob, err := state.SerializeAccountRoot(account)
			require.NoError(t, err)
			require.NoError(t, view.Insert(keylet.Account(accountID), blob))
			if accountID == sender {
				ctx.Account = account
			}
		}
	}
	payments := make([]AssetPayment, 0, len(row.Receivers))
	for _, payment := range row.Receivers {
		amount, err := state.ParseXRPLNumber(payment.Amount, scale, state.RoundToNearest)
		require.NoError(t, err)
		payments = append(payments, AssetPayment{Account: decodeAccount(payment.Account), Amount: amount})
	}
	require.True(t, row.SandboxBaseUnchanged)
	require.Equal(t, row.TER, SendAssets(ctx, sender, asset, payments).String())
	for _, balance := range append([]sendAssetsOracleBalance{{row.Sender, row.SenderAfter}}, row.ReceiverAfter...) {
		accountID := decodeAccount(balance.Account)
		if row.Asset == "MPT" {
			token, err := readMPToken(view, keylet.MPTokenByID(id, accountID))
			require.NoError(t, err)
			if balance.Balance == "missing" {
				require.Nil(t, token)
			} else {
				require.NotNil(t, token)
				require.Equal(t, parseBalance(balance.Balance), token.MPTAmount, balance.Account)
			}
		} else if row.Asset == "IOU" {
			line, err := tx.ReadRippleState(view, accountID, issuer, asset.Currency)
			require.NoError(t, err)
			require.NotNil(t, line)
			amount := line.Balance
			if state.CompareAccountIDs(accountID, issuer) > 0 {
				amount = amount.Negate()
			}
			require.Equal(t, 0, amount.Compare(parseIOU(balance.Balance)), balance.Account)
		} else {
			account, err := sendXRPAccount(ctx, accountID)
			require.NoError(t, err)
			if balance.Balance == "missing" {
				require.Nil(t, account)
			} else {
				require.NotNil(t, account)
				require.Equal(t, parseBalance(balance.Balance), account.Balance, balance.Account)
			}
		}
	}
	if row.Asset == "MPT" {
		issuance, err := readMPTIssuance(view, id)
		require.NoError(t, err)
		require.Equal(t, parseBalance(row.OutstandingAfter), issuance.OutstandingAmount)
	}
}
