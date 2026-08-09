package handlers

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// MethodDescriptor is the authoritative registration and dispatch metadata for
// an RPC method. The generated RPC catalogue is derived from these descriptors.
type MethodDescriptor struct {
	Name        string
	Handler     types.MethodHandler
	Role        types.Role
	Condition   types.Condition
	APIVersions []int
}

var allAPIVersions = []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}

func guest(name string, handler types.MethodHandler) MethodDescriptor {
	return MethodDescriptor{
		Name: name, Handler: handler, Role: types.RoleGuest,
		Condition: types.NoCondition, APIVersions: allAPIVersions,
	}
}

func user(name string, handler types.MethodHandler) MethodDescriptor {
	return MethodDescriptor{
		Name: name, Handler: handler, Role: types.RoleUser,
		Condition: types.NoCondition, APIVersions: allAPIVersions,
	}
}

func admin(name string, handler types.MethodHandler) MethodDescriptor {
	return MethodDescriptor{
		Name: name, Handler: handler, Role: types.RoleAdmin,
		Condition: types.NoCondition, APIVersions: allAPIVersions,
	}
}

func requiring(descriptor MethodDescriptor, condition types.Condition) MethodDescriptor {
	descriptor.Condition = condition
	return descriptor
}

func v1Only(descriptor MethodDescriptor) MethodDescriptor {
	descriptor.APIVersions = []int{types.ApiVersion1}
	return descriptor
}

var methodDescriptors = []MethodDescriptor{
	guest("server_info", &ServerInfoMethod{}),
	guest("server_state", &ServerStateMethod{}),
	guest("ping", &PingMethod{}),
	guest("random", &randomMethod{}),
	guest("server_definitions", &ServerDefinitionsMethod{}),
	guest("feature", &FeatureMethod{}),
	requiring(guest("fee", &FeeMethod{}), types.NeedsCurrentLedger),
	guest("version", &VersionMethod{}),

	guest("ledger", &LedgerMethod{}),
	requiring(guest("ledger_closed", &LedgerClosedMethod{}), types.NeedsClosedLedger),
	requiring(guest("ledger_current", &ledgerCurrentMethod{}), types.NeedsCurrentLedger),
	guest("ledger_data", &LedgerDataMethod{}),
	guest("ledger_entry", &LedgerEntryMethod{}),
	admin("ledger_range", &LedgerRangeMethod{}),
	v1Only(guest("ledger_header", &LedgerHeaderMethod{})),
	admin("ledger_request", &LedgerRequestMethod{}),
	requiring(admin("ledger_cleaner", &LedgerCleanerMethod{}), types.NeedsNetworkConnection),
	requiring(admin("ledger_accept", &LedgerAcceptMethod{}), types.NeedsCurrentLedger),

	guest("account_info", &AccountInfoMethod{}),
	guest("account_channels", &AccountChannelsMethod{}),
	guest("account_currencies", &AccountCurrenciesMethod{}),
	guest("account_lines", &AccountLinesMethod{}),
	guest("account_nfts", &AccountNftsMethod{}),
	guest("account_objects", &AccountObjectsMethod{}),
	guest("account_offers", &AccountOffersMethod{}),
	guest("account_tx", &AccountTxMethod{}),
	guest("gateway_balances", &GatewayBalancesMethod{}),
	guest("noripple_check", &NoRippleCheckMethod{}),
	requiring(guest("owner_info", &OwnerInfoMethod{}), types.NeedsCurrentLedger),

	requiring(user("tx", &TxMethod{}), types.NeedsNetworkConnection),
	v1Only(user("tx_history", &TxHistoryMethod{})),
	requiring(user("submit", &SubmitMethod{}), types.NeedsCurrentLedger),
	requiring(user("submit_multisigned", &SubmitMultisignedMethod{}), types.NeedsCurrentLedger),
	user("sign", &SignMethod{}),
	user("sign_for", &SignForMethod{}),
	user("transaction_entry", &TransactionEntryMethod{}),
	requiring(guest("simulate", &SimulateMethod{}), types.NeedsCurrentLedger),
	user("tx_reduce_relay", &TxReduceRelayMethod{}),

	guest("book_changes", &BookChangesMethod{}),
	guest("book_offers", &BookOffersMethod{}),
	requiring(guest("path_find", &pathFindMethod{}), types.NeedsCurrentLedger),
	guest("ripple_path_find", &ripplePathFindMethod{}),

	user("channel_authorize", &ChannelAuthorizeMethod{}),
	guest("channel_verify", &ChannelVerifyMethod{}),
	guest("json", &jsonMethod{}),
	admin("wallet_propose", &WalletProposeMethod{}),
	requiring(guest("deposit_authorized", &DepositAuthorizedMethod{}), types.NeedsCurrentLedger),
	guest("nft_buy_offers", &NftBuyOffersMethod{}),
	guest("nft_sell_offers", &NftSellOffersMethod{}),

	admin("stop", &StopMethod{}),
	admin("validation_create", &ValidationCreateMethod{}),
	user("manifest", &ManifestMethod{}),
	admin("peer_reservations_add", &PeerReservationsAddMethod{}),
	admin("peer_reservations_del", &PeerReservationsDelMethod{}),
	admin("peer_reservations_list", &PeerReservationsListMethod{}),
	admin("peers", &PeersMethod{}),
	admin("consensus_info", &ConsensusInfoMethod{}),
	admin("validator_list_sites", &validatorListSitesMethod{}),
	admin("validators", &ValidatorsMethod{}),
	admin("validator_info", &ValidatorInfoMethod{}),
	admin("unl_list", &UnlListMethod{}),
	admin("can_delete", &CanDeleteMethod{}),
	admin("get_counts", &GetCountsMethod{}),
	admin("log_level", &LogLevelMethod{}),
	admin("logrotate", &LogRotateMethod{}),
	admin("blacklist", &BlackListMethod{}),
	admin("fetch_info", &FetchInfoMethod{}),
	admin("connect", &ConnectMethod{}),
	admin("print", &PrintMethod{}),

	guest("amm_info", &AMMInfoMethod{}),
	guest("vault_info", &VaultInfoMethod{}),
	requiring(guest("get_aggregate_price", &GetAggregatePriceMethod{}), types.NeedsCurrentLedger),

	guest("subscribe", &SubscribeMethod{}),
	guest("unsubscribe", &UnsubscribeMethod{}),
}

// MethodDescriptors returns an isolated copy of the live RPC catalogue.
func MethodDescriptors() []MethodDescriptor {
	descriptors := make([]MethodDescriptor, len(methodDescriptors))
	for i, descriptor := range methodDescriptors {
		descriptor.Handler = newHandlerOfSameType(descriptor.Handler)
		descriptor.APIVersions = slices.Clone(descriptor.APIVersions)
		descriptors[i] = descriptor
	}
	return descriptors
}

func newHandlerOfSameType(handler types.MethodHandler) types.MethodHandler {
	handlerType := reflect.TypeOf(handler)
	if handlerType == nil || handlerType.Kind() != reflect.Pointer {
		panic(fmt.Sprintf("RPC method handler prototype has invalid type %T", handler))
	}
	clone, ok := reflect.New(handlerType.Elem()).Interface().(types.MethodHandler)
	if !ok {
		panic(fmt.Sprintf("RPC method handler prototype %T does not implement MethodHandler", handler))
	}
	return clone
}

type registeredMethod struct {
	descriptor MethodDescriptor
}

func (method registeredMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	return method.descriptor.Handler.Handle(ctx, params)
}

func (method registeredMethod) RequiredRole() types.Role {
	return method.descriptor.Role
}

func (method registeredMethod) SupportedApiVersions() []int {
	return slices.Clone(method.descriptor.APIVersions)
}

func (method registeredMethod) RequiredCondition() types.Condition {
	return method.descriptor.Condition
}

// RegisterAll adds the immutable method catalogue to a builder. The caller
// must build the registry before publishing it to a transport.
func RegisterAll(registry *types.MethodRegistryBuilder) error {
	if registry == nil {
		return fmt.Errorf("RPC method registry builder is nil")
	}
	seen := make(map[string]struct{}, len(methodDescriptors))
	for _, descriptor := range methodDescriptors {
		if _, duplicate := seen[descriptor.Name]; duplicate {
			return fmt.Errorf("duplicate RPC method descriptor %q", descriptor.Name)
		}
		seen[descriptor.Name] = struct{}{}
		descriptor.Handler = newHandlerOfSameType(descriptor.Handler)
		if err := registry.Register(descriptor.Name, registeredMethod{descriptor: descriptor}); err != nil {
			return fmt.Errorf("register RPC method %q: %w", descriptor.Name, err)
		}
	}
	return nil
}

// BuildRegistry returns a freshly built copy of the production RPC method
// catalogue.
func BuildRegistry() (*types.MethodRegistry, error) {
	builder := types.NewMethodRegistryBuilder()
	if err := RegisterAll(builder); err != nil {
		return nil, err
	}
	return builder.Build()
}

// BuildRegistryWithOverrides builds a pre-publication registry for transport
// tests that need to replace or add a small number of handlers. The returned
// registry remains immutable once built.
func BuildRegistryWithOverrides(overrides map[string]types.MethodHandler) (*types.MethodRegistry, error) {
	builder := types.NewMethodRegistryBuilder()
	seen := make(map[string]struct{}, len(methodDescriptors)+len(overrides))
	for _, descriptor := range methodDescriptors {
		if _, duplicate := seen[descriptor.Name]; duplicate {
			return nil, fmt.Errorf("duplicate RPC method descriptor %q", descriptor.Name)
		}
		seen[descriptor.Name] = struct{}{}
		if override, ok := overrides[descriptor.Name]; ok {
			if methodHandlerIsNil(override) {
				return nil, fmt.Errorf("RPC method %q has a nil override", descriptor.Name)
			}
			descriptor.Handler = override
		} else {
			descriptor.Handler = newHandlerOfSameType(descriptor.Handler)
		}
		if err := builder.Register(descriptor.Name, registeredMethod{descriptor: descriptor}); err != nil {
			return nil, fmt.Errorf("register RPC method %q: %w", descriptor.Name, err)
		}
	}
	for name, handler := range overrides {
		if _, exists := seen[name]; exists {
			continue
		}
		if err := builder.Register(name, handler); err != nil {
			return nil, fmt.Errorf("register RPC method %q: %w", name, err)
		}
	}
	return builder.Build()
}

func methodHandlerIsNil(handler types.MethodHandler) bool {
	if handler == nil {
		return true
	}
	value := reflect.ValueOf(handler)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
