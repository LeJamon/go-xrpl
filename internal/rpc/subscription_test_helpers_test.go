package rpc

import (
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/subscription"
	"github.com/stretchr/testify/require"
)

var testRegistrations = struct {
	sync.Mutex
	byManager map[*subscription.Manager]map[*subscription.Connection]*subscription.Registration
}{byManager: make(map[*subscription.Manager]map[*subscription.Connection]*subscription.Registration)}

func testRegistration(t testing.TB, manager *subscription.Manager, connection *subscription.Connection) *subscription.Registration {
	t.Helper()
	testRegistrations.Lock()
	registrations := testRegistrations.byManager[manager]
	if registration := registrations[connection]; registration != nil {
		testRegistrations.Unlock()
		return registration
	}
	registration, attached := manager.Attach(connection)
	require.True(t, attached)
	if registrations == nil {
		registrations = make(map[*subscription.Connection]*subscription.Registration)
		testRegistrations.byManager[manager] = registrations
	}
	registrations[connection] = registration
	testRegistrations.Unlock()
	t.Cleanup(func() {
		manager.Detach(registration)
		testRegistrations.Lock()
		delete(testRegistrations.byManager[manager], connection)
		if len(testRegistrations.byManager[manager]) == 0 {
			delete(testRegistrations.byManager, manager)
		}
		testRegistrations.Unlock()
	})
	return registration
}
