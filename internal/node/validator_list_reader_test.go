package node

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	rpctypes "github.com/LeJamon/go-xrpl/internal/rpc/types"
	validatorlist "github.com/LeJamon/go-xrpl/internal/validator/list"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/require"
)

func TestPublisherInfosProjectsStates(t *testing.T) {
	cases := []struct {
		name       string
		status     validatorlist.PublisherStatus
		pending    bool
		available  bool
		remainingN int
	}{
		{name: "unavailable", status: validatorlist.StatusUnavailable},
		{name: "available", status: validatorlist.StatusAvailable, available: true},
		{name: "expired", status: validatorlist.StatusExpired},
		{name: "revoked", status: validatorlist.StatusRevoked},
		{name: "pending", status: validatorlist.StatusUnavailable, pending: true, remainingN: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := validatorlist.PublisherInfo{MasterKey: [33]byte{0xed, 1}, Status: tc.status}
			if tc.pending {
				info.Remaining = map[uint32]*validatorlist.RemainingInfo{9: {
					Sequence: 9, Effective: time.Now().Add(time.Hour), EffectiveSet: true,
				}}
			}
			got := publisherInfos([]validatorlist.PublisherInfo{info})
			if len(got) != 1 || got[0].Available != tc.available || got[0].Status != tc.status.String() {
				t.Fatalf("projection: got %+v", got)
			}
			if len(got[0].Remaining) != tc.remainingN {
				t.Fatalf("remaining: got %d want %d", len(got[0].Remaining), tc.remainingN)
			}
		})
	}
}

func validatorListRPCJSON(t *testing.T, name string, reader rpctypes.ValidatorListReader) []byte {
	t.Helper()
	registry, err := handlers.BuildRegistry()
	require.NoError(t, err)
	method, ok := registry.Get(name)
	require.True(t, ok)
	result, rpcErr := method.Handle(&rpctypes.RpcContext{
		Context:    t.Context(),
		Role:       rpctypes.RoleAdmin,
		ApiVersion: rpctypes.ApiVersion1,
		Services:   rpctypes.NewTestServiceGraph(&rpctypes.ServiceContainer{ValidatorList: reader}),
	}, nil)
	require.Nil(t, rpcErr)
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	return raw
}

func TestValidatorsRPCEffectiveTime(t *testing.T) {
	epoch := time.Unix(protocol.RippleEpochUnix, 0).UTC()
	for _, tc := range []struct {
		name      string
		set       bool
		effective time.Time
		want      string
	}{
		{name: "absent", effective: epoch},
		{name: "explicit zero", set: true, effective: epoch},
		{name: "nonzero", set: true, effective: epoch.Add(123 * time.Second), want: "2000-Jan-01 00:02:03.000000000 UTC"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expiration := epoch.Add(time.Hour)
			projected := publisherInfos([]validatorlist.PublisherInfo{{
				Effective: tc.effective, EffectiveSet: tc.set, Expiration: expiration,
				Remaining: map[uint32]*validatorlist.RemainingInfo{2: {
					Sequence: 2, Effective: tc.effective, EffectiveSet: tc.set, Expiration: expiration,
				}},
			}})
			require.Equal(t, tc.set, projected[0].EffectiveSet)
			raw := validatorListRPCJSON(t, "validators", httpProviderValidatorList{publishers: projected})
			var response struct {
				Publishers []map[string]json.RawMessage `json:"publisher_lists"`
			}
			require.NoError(t, json.Unmarshal(raw, &response))
			require.Len(t, response.Publishers, 1)
			current := response.Publishers[0]
			var remaining []map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(current["remaining"], &remaining))
			require.Len(t, remaining, 1)
			for _, entry := range []map[string]json.RawMessage{current, remaining[0]} {
				if tc.want == "" {
					require.NotContains(t, entry, "effective")
				} else {
					var got string
					require.NoError(t, json.Unmarshal(entry["effective"], &got))
					require.Equal(t, tc.want, got)
				}
			}
		})
	}
}

func TestValidatorsRPCTimestampsPreserveFractionalSeconds(t *testing.T) {
	currentEffective := time.Date(2026, time.May, 1, 11, 0, 0, 123456789, time.UTC)
	currentExpiration := time.Date(2026, time.May, 2, 12, 0, 0, 987654321, time.UTC)
	pendingEffective := time.Date(2026, time.May, 1, 13, 0, 0, 111111111, time.UTC)
	pendingExpiration := time.Date(2026, time.May, 3, 12, 0, 0, 222222222, time.UTC)
	projected := publisherInfos([]validatorlist.PublisherInfo{{
		MasterKey:    [33]byte{1},
		Status:       validatorlist.StatusAvailable,
		Sequence:     2,
		Version:      1,
		Effective:    currentEffective,
		EffectiveSet: true,
		Expiration:   currentExpiration,
		Remaining: map[uint32]*validatorlist.RemainingInfo{3: {
			Sequence:     3,
			Effective:    pendingEffective,
			EffectiveSet: true,
			Expiration:   pendingExpiration,
		}},
	}})

	var response map[string]any
	raw := validatorListRPCJSON(t, "validators", httpProviderValidatorList{publishers: projected})
	require.NoError(t, json.Unmarshal(raw, &response))
	publishers := response["publisher_lists"].([]any)
	require.Len(t, publishers, 1)
	publisher := publishers[0].(map[string]any)
	require.Equal(t, "2026-May-01 11:00:00.123456789 UTC", publisher["effective"])
	require.Equal(t, "2026-May-02 12:00:00.987654321 UTC", publisher["expiration"])
	remaining := publisher["remaining"].([]any)
	require.Len(t, remaining, 1)
	next := remaining[0].(map[string]any)
	require.Equal(t, "2026-May-01 13:00:00.111111111 UTC", next["effective"])
	require.Equal(t, "2026-May-03 12:00:00.222222222 UTC", next["expiration"])

	summary := response["validator_list"].(map[string]any)
	require.Equal(t, "2026-May-03 12:00:00.000000000 UTC", summary["expiration"])
}

func TestSiteInfosProjection(t *testing.T) {
	got := siteInfos([]validatorlist.SiteInfo{{
		URI:                "https://a (redirects to https://b)",
		RefreshSeconds:     150,
		LastDispositionSet: true,
		LastDisposition:    validatorlist.Malformed,
	}})
	if len(got) != 1 || got[0].URI != "https://a (redirects to https://b)" ||
		got[0].RefreshIntervalMin != 2 || got[0].LastDisposition != validatorlist.Invalid.String() {
		t.Fatalf("site projection: got %+v", got)
	}
}

func TestValidatorListSitesRPC(t *testing.T) {
	const uri = "https://validators.example/list"
	now := time.Date(2026, 11, 20, 12, 0, 0, 123456789, time.UTC)
	agg, err := validatorlist.New(validatorlist.Config{
		SiteURIs: []string{uri},
		Clock:    func() time.Time { return now },
	})
	require.NoError(t, err)
	reader := &validatorListReader{agg: agg}
	require.JSONEq(t, `{"validator_sites":[{
		"uri":"https://validators.example/list", "refresh_interval_min":5,
		"next_refresh_time":"2026-Nov-20 12:05:00.123456789 UTC"
	}]}`, string(validatorListRPCJSON(t, "validator_list_sites", reader)))

	last := now.Add(time.Hour)
	agg.UpdateSiteState(uri, last, time.Time{}, "bad response", validatorlist.Malformed, 150, last.Add(150*time.Second))
	require.JSONEq(t, `{"validator_sites":[{
		"uri":"https://validators.example/list", "refresh_interval_min":2,
		"last_refresh_time":"2026-Nov-20 13:00:00.123456789 UTC", "last_refresh_status":"invalid",
		"last_refresh_message":"bad response", "next_refresh_time":"2026-Nov-20 13:02:30.123456789 UTC"
	}]}`, string(validatorListRPCJSON(t, "validator_list_sites", reader)))
}

func TestValidatorListReaderNilIsSafe(t *testing.T) {
	for _, reader := range []*validatorListReader{nil, {}} {
		if reader.HasConfiguredPublishers() || reader.PublisherCount() != 0 || reader.Threshold() != 0 || reader.IsUNLBlocked() {
			t.Fatal("nil reader returned configured state")
		}
		if reader.Publishers() != nil || reader.Sites() != nil || reader.TrustedMasterKeys() != nil || reader.ListedValidators() != nil {
			t.Fatal("nil reader returned non-empty projection")
		}
	}
}
