package list_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/validator/list"
	"github.com/stretchr/testify/require"
)

func TestValidatorPublisherRPCTimestamps(t *testing.T) {
	pub := newPublisher(t, 1, 2)
	validators := [][33]byte{derivedValidatorKey(3)}
	now := fixedClock()()
	agg, err := list.New(list.Config{
		PublisherKeys: []list.PublisherKey{list.PublisherKey(pub.masterPub)},
		Clock:         fixedClock(),
	})
	require.NoError(t, err)
	apply := func(sequence uint32, effective int64, expiration time.Time, want list.Disposition) {
		t.Helper()
		blob, signature := pub.signList(t, sequence, effective, expiration.Unix(), validators)
		disposition, _, _ := agg.ApplyList(pub.manifestB64, blob, signature, 2, "https://publisher.example/list")
		require.Equal(t, want, disposition)
	}

	apply(1, 0, now.Add(24*time.Hour), list.Accepted)
	response := validatorRPCResult(t, agg, "validators")
	publishers := response["publisher_lists"].([]any)
	require.Len(t, publishers, 1)
	publisher := publishers[0].(map[string]any)
	require.NotContains(t, publisher, "effective")
	require.NotContains(t, publisher, "remaining")
	require.Equal(t, "2026-May-02 12:00:00.000000000 UTC", publisher["expiration"])

	apply(2, now.Add(-time.Hour).Unix(), now.Add(24*time.Hour), list.Accepted)
	apply(3, now.Add(time.Hour).Unix(), now.Add(48*time.Hour), list.Pending)
	response = validatorRPCResult(t, agg, "validators")
	publishers = response["publisher_lists"].([]any)
	require.Len(t, publishers, 1)
	publisher = publishers[0].(map[string]any)
	require.Equal(t, "2026-May-01 11:00:00.000000000 UTC", publisher["effective"])
	require.Equal(t, "2026-May-02 12:00:00.000000000 UTC", publisher["expiration"])
	remaining := publisher["remaining"].([]any)
	require.Len(t, remaining, 1)
	next := remaining[0].(map[string]any)
	require.Equal(t, "2026-May-01 13:00:00.000000000 UTC", next["effective"])
	require.Equal(t, "2026-May-03 12:00:00.000000000 UTC", next["expiration"])
	summary := response["validator_list"].(map[string]any)
	require.Equal(t, "2026-May-03 12:00:00.000000000 UTC", summary["expiration"])
}

func TestValidatorSiteRPCTimestamps(t *testing.T) {
	const uri = "https://publisher.example/list"
	now := time.Date(2026, time.May, 1, 14, 0, 0, 123456789, time.FixedZone("UTC+2", 2*60*60))
	agg, err := list.New(list.Config{
		SiteURIs: []string{uri},
		Clock:    func() time.Time { return now },
	})
	require.NoError(t, err)
	agg.SetNextRefresh(uri, now)
	response := validatorRPCResult(t, agg, "validator_list_sites")
	sites := response["validator_sites"].([]any)
	require.Len(t, sites, 1)
	site := sites[0].(map[string]any)
	require.Equal(t, "2026-May-01 12:00:00.123456789 UTC", site["next_refresh_time"])
	require.NotContains(t, site, "last_refresh_time")
	require.NotContains(t, site, "last_refresh_status")

	agg.UpdateSiteState(uri, now, now, "", list.Accepted, 300, now.Add(5*time.Minute))
	response = validatorRPCResult(t, agg, "validator_list_sites")
	sites = response["validator_sites"].([]any)
	require.Len(t, sites, 1)
	site = sites[0].(map[string]any)
	require.Equal(t, "2026-May-01 12:00:00.123456789 UTC", site["last_refresh_time"])
	require.Equal(t, "2026-May-01 12:05:00.123456789 UTC", site["next_refresh_time"])
	require.Equal(t, "accepted", site["last_refresh_status"])
}

func validatorRPCResult(t *testing.T, agg *list.Aggregator, name string) map[string]any {
	t.Helper()
	registry, err := handlers.BuildRegistry()
	require.NoError(t, err)
	method, ok := registry.Get(name)
	require.True(t, ok)
	result, rpcErr := method.Handle(&types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleAdmin,
		ApiVersion: types.ApiVersion1,
		Services: types.NewTestServiceGraph(&types.ServiceContainer{
			ValidatorList: list.NewRPCReader(agg),
		}),
	}, json.RawMessage(`{}`))
	require.Nil(t, rpcErr)
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	var response map[string]any
	require.NoError(t, json.Unmarshal(raw, &response))
	return response
}
