package config

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfigEmptyValidatorTrustSections(t *testing.T) {
	for _, test := range []struct {
		name    string
		file    string
		content string
	}{
		{"validators", "validators.txt", "[validators]\n"},
		{"legacy validators", "validators.txt", "[validator_keys]\n"},
		{"publishers", "validators.txt", "[validator_list_keys]\n"},
		{"comments and CRLF", "validators.txt", "# trust\r\n[validators]\r\n# no entries\r\n"},
		{"legacy CR", "validators.cfg", "[validator_keys]\r# no entries\r"},
		{"all sections", "validators.txt", "[validators]\n[validator_keys]\n[validator_list_keys]\n"},
		{"TOML validators", "validators.toml", "validators = []\n"},
		{"TOML publishers", "validators.toml", "validator_list_keys = []\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeConfig(t, dir, "goxrpl.toml", fmt.Sprintf("validators_file = %q\n", test.file)+minimalTestConfig())
			writeConfig(t, dir, test.file, test.content)

			cfg, err := LoadConfig(Paths{Main: path})
			require.NoError(t, err)
			assert.Empty(t, cfg.Validators.Validators)
			assert.Empty(t, cfg.Validators.ValidatorListKeys)
		})
	}
}

func TestLoadConfigRejectsInvalidValidatorFiles(t *testing.T) {
	for _, test := range []struct {
		name    string
		file    string
		content string
		wantErr string
	}{
		{"empty TXT", "validators.txt", "", "validators file must contain"},
		{"comments only", "validators.txt", "# [validators]\n", "validators file must contain"},
		{"unrecognized section", "validators.txt", "[unrelated]\n", "validators file must contain"},
		{"extra brackets", "validators.txt", "[[validators]]\n", "validators file must contain"},
		{"sites only", "validators.txt", "[validator_list_sites]\n", "validators file must contain"},
		{"empty TOML", "validators.toml", "", "validators file must contain"},
		{"TOML sites only", "validators.toml", "validator_list_sites = []\n", "validators file must contain"},
		{"TOML table instead of array", "validators.toml", "[validators]\n", "validators file must contain"},
		{"sites without keys", "validators.txt", "[validator_list_keys]\n[validator_list_sites]\nhttps://vl.example.com\n", "validator_list_sites requires"},
		{"TOML sites without keys", "validators.toml", "validator_list_keys = []\nvalidator_list_sites = [\"https://vl.example.com\"]\n", "validator_list_sites requires"},
		{"threshold without keys", "validators.txt", "[validators]\n[validator_list_threshold]\n1\n", "requires at least one validator_list_keys"},
		{"invalid validator", "validators.txt", "[validators]\ninvalid\n", "invalid validator"},
		{"invalid publisher", "validators.toml", "validator_list_keys = [\"invalid\"]\n", "invalid validator_list_key"},
		{"malformed TOML", "validators.toml", "validators = [\n", "failed to read validators file"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeConfig(t, dir, "goxrpl.toml", fmt.Sprintf("validators_file = %q\n", test.file)+minimalTestConfig())
			writeConfig(t, dir, test.file, test.content)

			_, err := LoadConfig(Paths{Main: path})
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}

func TestLoadConfigRepeatedValidatorSectionsPreserveEntries(t *testing.T) {
	const validator = "n9KorY8QtTdRx7TVDpwnG9NvyxsDwHUKUEeDLY3AkiGncVaSXZi5"
	const publisher = "ED264807102805220DA0F312E71FC2C69E1552C9C5790F6C25E3729DEB573D5860"
	dir := t.TempDir()
	path := writeConfig(t, dir, "goxrpl.toml", minimalTestConfig())
	writeConfig(t, dir, "validators.txt", "[validators]\n"+validator+
		"\n[validators]\n[validator_keys]\n[validator_list_keys]\n"+publisher+"\n[validator_list_keys]\n")

	cfg, err := LoadConfig(Paths{Main: path})
	require.NoError(t, err)
	assert.Equal(t, []string{validator}, cfg.Validators.Validators)
	assert.Equal(t, []string{publisher}, cfg.Validators.ValidatorListKeys)
}
