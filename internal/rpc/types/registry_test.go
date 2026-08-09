package types

import (
	"encoding/json"
	"reflect"
	"sync"
	"testing"
)

type registryTestHandler struct{}

func (registryTestHandler) Handle(*RpcContext, json.RawMessage) (any, *RpcError) { return nil, nil }
func (registryTestHandler) RequiredRole() Role                                   { return RoleGuest }
func (registryTestHandler) SupportedApiVersions() []int                          { return nil }
func (registryTestHandler) RequiredCondition() Condition                         { return NoCondition }

func TestMethodRegistryBuilderValidationAndPublication(t *testing.T) {
	var builder MethodRegistryBuilder
	handler := registryTestHandler{}
	var typedNil *registryTestHandler
	for _, test := range []struct {
		name    string
		handler MethodHandler
	}{
		{name: "", handler: handler},
		{name: " ", handler: handler},
		{name: " rpc", handler: handler},
		{name: "rpc ", handler: handler},
		{name: "nil", handler: nil},
		{name: "typed nil", handler: typedNil},
	} {
		if err := builder.Register(test.name, test.handler); err == nil {
			t.Errorf("Register(%q, %T) unexpectedly succeeded", test.name, test.handler)
		}
	}
	if err := builder.Register("zeta", handler); err != nil {
		t.Fatalf("register zeta: %v", err)
	}
	if err := builder.Register("alpha", handler); err != nil {
		t.Fatalf("register alpha: %v", err)
	}
	if err := builder.Register("alpha", handler); err == nil {
		t.Fatal("duplicate registration unexpectedly succeeded")
	}
	registry, err := builder.Build()
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	if err := builder.Register("late", handler); err == nil {
		t.Fatal("registration after build unexpectedly succeeded")
	}
	if _, err := builder.Build(); err == nil {
		t.Fatal("second build unexpectedly succeeded")
	}

	want := []string{"alpha", "zeta"}
	if got := registry.List(); !reflect.DeepEqual(got, want) {
		t.Fatalf("List() = %v, want sorted %v", got, want)
	}
	listed := registry.List()
	listed[0] = "changed"
	if got := registry.List(); !reflect.DeepEqual(got, want) {
		t.Fatalf("List() exposed internal storage: %v", got)
	}
	if got, ok := registry.Get("alpha"); !ok || got != handler {
		t.Fatalf("Get(alpha) = (%T, %v), want registered handler", got, ok)
	}
}

func TestMethodRegistryNilReceiversAndConcurrentReads(t *testing.T) {
	var nilBuilder *MethodRegistryBuilder
	if err := nilBuilder.Register("rpc", registryTestHandler{}); err == nil {
		t.Fatal("nil builder Register unexpectedly succeeded")
	}
	if registry, err := nilBuilder.Build(); err == nil || registry != nil {
		t.Fatalf("nil builder Build = (%v, %v), want nil/error", registry, err)
	}
	var nilRegistry *MethodRegistry
	if handler, ok := nilRegistry.Get("rpc"); ok || handler != nil {
		t.Fatalf("nil registry Get = (%v, %v), want (nil, false)", handler, ok)
	}
	if got := nilRegistry.List(); got != nil {
		t.Fatalf("nil registry List = %v, want nil", got)
	}

	registry, err := (&MethodRegistryBuilder{}).Build()
	if err != nil {
		t.Fatalf("build empty registry: %v", err)
	}
	var readers sync.WaitGroup
	for range 16 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 100 {
				registry.Get("rpc")
				registry.List()
			}
		}()
	}
	readers.Wait()
}
