package vault

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func parseAssetsMaximumJSON(raw json.RawMessage) (*string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("invalid AssetsMaximum: %w", err)
	}

	var number string
	switch value := value.(type) {
	case string:
		number = value
	case json.Number:
		number = value.String()
		if strings.ContainsAny(number, ".eE") {
			return nil, fmt.Errorf("invalid AssetsMaximum: JSON number must be an integer")
		}
		if strings.HasPrefix(number, "-") {
			if _, err := strconv.ParseInt(number, 10, 32); err != nil {
				return nil, fmt.Errorf("invalid AssetsMaximum: %w", err)
			}
		} else if _, err := strconv.ParseUint(number, 10, 32); err != nil {
			return nil, fmt.Errorf("invalid AssetsMaximum: %w", err)
		}
	default:
		return nil, fmt.Errorf("invalid AssetsMaximum: expected string or integer")
	}

	if _, err := parseCreateSetNumber(number); err != nil {
		return nil, fmt.Errorf("invalid AssetsMaximum: %w", err)
	}
	return &number, nil
}

func validateAssetsMaximum(value *string) error {
	if value == nil {
		return nil
	}
	number, err := parseCreateSetNumber(*value)
	if err != nil {
		return ter.Errorf(ter.TemMALFORMED, "AssetsMaximum is not a valid NUMBER")
	}
	if number.Signum() < 0 {
		return ErrVaultAssetsMaxNeg
	}
	return nil
}

func parseCreateSetNumber(s string) (state.XRPLNumber, error) {
	return state.ParseXRPLNumber(
		s,
		state.MantissaScaleLarge,
		state.RoundToNearest,
	)
}

func parseCreateSetNumberForRules(s string, rules *amendment.Rules) (state.XRPLNumber, error) {
	scale := state.MantissaScaleLarge
	if rules != nil {
		scale = state.MantissaScaleForRulesWithFix(
			true,
			rules.Enabled(amendment.FeatureSingleAssetVault),
			rules.Enabled(amendment.FeatureLendingProtocol),
			rules.FixCleanup3_2_0Enabled(),
		)
	}
	return state.ParseXRPLNumber(s, scale, state.RoundToNearest)
}

func canonicalCreateSetAssetsMaximum(asset tx.Asset, value state.XRPLNumber) string {
	integral := asset.IsNative() || asset.IsMPT()
	var rounded state.XRPLNumber
	var remove bool
	if integral {
		rounded, remove = state.AssociateAssetField(value, true, true)
	} else {
		if value.IsZero() {
			return ""
		}
		mantissa, exponent := value.NormalizeToRange(uint64(state.MinMantissa), uint64(state.MaxMantissa))
		if exponent > state.MaxExponent {
			panic("issued amount exponent exceeds maximum")
		}
		if exponent < state.MinExponent {
			return ""
		}
		rounded = state.NewXRPLNumber(mantissa, exponent)
	}

	if asset.IsNative() && rounded.Cmp(state.NewXRPLNumberScaled(
		int64(state.MaxNativeDrops),
		0,
		state.MantissaScaleLarge,
		state.RoundToNearest,
	)) > 0 {
		panic("native amount exceeds maximum XRP")
	}

	if remove {
		return ""
	}
	return numberToString(rounded)
}
