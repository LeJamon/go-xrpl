package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPProfHandler(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	response := httptest.NewRecorder()
	PProfHandler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if !strings.Contains(response.Body.String(), "profile") {
		t.Fatalf("body does not contain pprof index:\n%s", response.Body.String())
	}
}
