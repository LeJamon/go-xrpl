package node

import (
	"testing"
	"time"

	validatorlist "github.com/LeJamon/go-xrpl/internal/validator/list"
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

func TestValidatorListReaderNilIsSafe(t *testing.T) {
	for _, reader := range []*validatorListReader{nil, newValidatorListReader(nil)} {
		if reader.HasConfiguredPublishers() || reader.PublisherCount() != 0 || reader.Threshold() != 0 || reader.IsUNLBlocked() {
			t.Fatal("nil reader returned configured state")
		}
		if reader.Publishers() != nil || reader.Sites() != nil || reader.TrustedMasterKeys() != nil || reader.ListedValidators() != nil {
			t.Fatal("nil reader returned non-empty projection")
		}
	}
}
