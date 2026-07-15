package node

import (
	"testing"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateTrustedValidatorConfig(t *testing.T) {
	const (
		validatorKey = "n9KorY8QtTdRx7TVDpwnG9NvyxsDwHUKUEeDLY3AkiGncVaSXZi5"
		publisherKey = "ED264807102805220DA0F312E71FC2C69E1552C9C5790F6C25E3729DEB573D5860"
	)

	tests := []struct {
		name       string
		standalone bool
		validators config.ValidatorsConfig
		seed       string
		wantErr    bool
	}{
		{name: "empty network config", wantErr: true},
		{name: "empty standalone config", standalone: true},
		{name: "manual validator", validators: config.ValidatorsConfig{Validators: []string{validatorKey}}},
		{name: "publisher key", validators: config.ValidatorsConfig{ValidatorListKeys: []string{publisherKey}}},
		{name: "site without publisher key", validators: config.ValidatorsConfig{ValidatorListSites: []string{"https://vl.altnet.rippletest.net"}}, wantErr: true},
		{name: "local validation key is not a trust anchor", seed: "snoPBrXtMeMyMHUVTgbuqAfg1SUTb", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTrustedValidatorConfig(&config.Config{
				Validators:     tt.validators,
				ValidationSeed: tt.seed,
			}, tt.standalone)
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "validators")
			assert.Contains(t, err.Error(), "validator_list_keys")
			assert.Contains(t, err.Error(), "--standalone")
		})
	}
}
