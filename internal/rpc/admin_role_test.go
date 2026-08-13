package rpc

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMethodDescriptorsMatchHandlerMetadata(t *testing.T) {
	for _, descriptor := range handlers.MethodDescriptors() {
		t.Run(descriptor.Name, func(t *testing.T) {
			assert.Equal(t, descriptor.Role, descriptor.Handler.RequiredRole())
			assert.Equal(t, descriptor.Condition, descriptor.Handler.RequiredCondition())
			assert.Equal(t, descriptor.APIVersions, descriptor.Handler.SupportedApiVersions())
		})
	}
}

func TestMethodDescriptorCatalogue(t *testing.T) {
	descriptors := handlers.MethodDescriptors()
	require.Len(t, descriptors, 74)

	roleCounts := map[types.Role]int{}
	seen := make(map[string]struct{}, len(descriptors))
	for _, descriptor := range descriptors {
		assert.NotEmpty(t, descriptor.Name)
		assert.NotNil(t, descriptor.Handler)
		assert.NotEmpty(t, descriptor.APIVersions)
		if _, duplicate := seen[descriptor.Name]; duplicate {
			t.Errorf("duplicate RPC method descriptor %q", descriptor.Name)
		}
		seen[descriptor.Name] = struct{}{}
		roleCounts[descriptor.Role]++
	}

	assert.Equal(t, 24, roleCounts[types.RoleAdmin])
	assert.Equal(t, 40, roleCounts[types.RoleGuest])
	assert.Equal(t, 10, roleCounts[types.RoleUser])

	registry, err := handlers.BuildRegistry()
	assert.NoError(t, err)
	assert.ElementsMatch(t, registry.List(), mapKeys(seen))
	for _, descriptor := range descriptors {
		registered, ok := registry.Get(descriptor.Name)
		require.True(t, ok)
		assert.Equal(t, descriptor.Role, registered.RequiredRole())
		assert.Equal(t, descriptor.Condition, registered.RequiredCondition())
		assert.Equal(t, descriptor.APIVersions, registered.SupportedApiVersions())
	}
}

func TestMethodDescriptorsReturnIsolatedVersions(t *testing.T) {
	first := handlers.MethodDescriptors()
	second := handlers.MethodDescriptors()
	require.NotEmpty(t, first)
	require.NotEmpty(t, second)

	first[0].APIVersions[0] = 99
	assert.NotEqual(t, first[0].APIVersions, second[0].APIVersions)
}

func mapKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
