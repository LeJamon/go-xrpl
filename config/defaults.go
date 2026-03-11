package config

// GetDefaultNetworkConfig returns default network-specific configurations.
// Used by the generate-config command to create template config files.
func GetDefaultNetworkConfig(networkID interface{}) map[string]interface{} {
	defaults := make(map[string]interface{})

	switch networkID {
	case 0, "main":
		// Mainnet defaults
		defaults["validators_file"] = "validators.txt"
		defaults["ips"] = []string{
			"r.ripple.com 51235",
			"sahyadri.isrdc.in 51235",
			"hubs.xrpkuwait.com 51235",
			"hub.xrpl-commons.org 51235",
		}

	case 1, "testnet":
		// Testnet defaults
		defaults["validators_file"] = "validators-testnet.txt"
		defaults["ips"] = []string{
			"r.altnet.rippletest.net 51235",
		}

	case 2, "devnet":
		// Devnet defaults - typically empty, configured per deployment
		defaults["validators_file"] = "validators-devnet.txt"
		defaults["ips"] = []string{}
	}

	return defaults
}

// GetDefaultValidatorsConfig returns default validators configuration based on network.
// Used by the generate-config command to create template validator files.
func GetDefaultValidatorsConfig(networkID interface{}) ValidatorsConfig {
	switch networkID {
	case 0, "main":
		return ValidatorsConfig{
			ValidatorListSites: []string{
				"https://vl.ripple.com",
				"https://unl.xrplf.org",
			},
			ValidatorListKeys: []string{
				"ED2677ABFFD1B33AC6FBC3062B71F1E8397C1505E1C42C64D11AD1B28FF73F4734", // vl.ripple.com
				"ED42AEC58B701EEBB77356FFFEC26F83C1F0407263530F068C7C73D392C7E06FD1", // unl.xrplf.org
			},
			ValidatorListThreshold: 0, // Auto-calculate
		}

	case 1, "testnet":
		return ValidatorsConfig{
			ValidatorListSites: []string{
				"https://vl.altnet.rippletest.net",
			},
			ValidatorListKeys: []string{
				"ED264807102805220DA0F312E71FC2C69E1552C9C5790F6C25E3729DEB573D5860",
			},
			ValidatorListThreshold: 0, // Auto-calculate
		}

	case 2, "devnet":
		return ValidatorsConfig{}

	default:
		return GetDefaultValidatorsConfig("main")
	}
}
