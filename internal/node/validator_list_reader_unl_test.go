package node

import (
	"encoding/json"
	"testing"

	rpctypes "github.com/LeJamon/go-xrpl/internal/rpc/types"
	validatorlist "github.com/LeJamon/go-xrpl/internal/validator/list"
	"github.com/stretchr/testify/require"
)

func TestListedValidatorsCombinesTrustSources(t *testing.T) {
	local, publisher, revoked, belowThreshold := [33]byte{0xed, 1}, [33]byte{0xed, 2}, [33]byte{0xed, 3}, [33]byte{0xed, 4}
	publishers := []validatorlist.PublisherInfo{{Validators: [][33]byte{belowThreshold, publisher, revoked}}}
	got := projectListedValidators(publishers, [][33]byte{local, publisher, revoked}, func(k [33]byte) bool { return k == revoked })
	require.Equal(t, []rpctypes.ListedValidator{
		{MasterKey: local, Trusted: true},
		{MasterKey: publisher, Trusted: true},
		{MasterKey: revoked},
		{MasterKey: belowThreshold},
	}, got)
}

func TestUnlListRPCWithoutPublishers(t *testing.T) {
	var trusted [][33]byte
	reader := &validatorListReader{trustedMasterKeys: func() [][33]byte { return trusted }}
	require.JSONEq(t, `{"unl":null}`, string(validatorListRPCJSON(t, "unl_list", reader)))

	static, local := [33]byte{0xed, 1}, [33]byte{0xed, 2}
	trusted = [][33]byte{static, local}
	reader.revokedMasterKey = func(key [33]byte) bool { return key == static }
	keys := validatorKeysBase58(trusted)
	require.Len(t, keys, 2)
	want, err := json.Marshal(map[string]any{"unl": []map[string]any{
		{"pubkey_validator": keys[0], "trusted": false},
		{"pubkey_validator": keys[1], "trusted": true},
	}})
	require.NoError(t, err)
	require.JSONEq(t, string(want), string(validatorListRPCJSON(t, "unl_list", reader)))
}
