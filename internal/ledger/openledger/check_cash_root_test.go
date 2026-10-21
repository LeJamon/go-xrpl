package openledger_test

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/shamap"
)

type transactionRootFixture struct {
	Result struct {
		Ledger struct {
			Transactions    []map[string]any `json:"transactions"`
			TransactionHash string           `json:"transaction_hash"`
		} `json:"ledger"`
	} `json:"result"`
}

func readTransactionRootFixture(t *testing.T, name string) transactionRootFixture {
	t.Helper()

	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var fixture transactionRootFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func reconstructTransactionRoot(t *testing.T, records []map[string]any, mutateMetadata func(map[string]any) error) string {
	t.Helper()

	tree := shamap.New(shamap.TypeTransaction)
	for _, record := range records {
		fields := make(map[string]any, len(record))
		for key, value := range record {
			if key != "hash" && key != "metaData" {
				fields[key] = value
			}
		}
		if deliverMax, ok := fields["DeliverMax"]; ok {
			if amount, hasAmount := fields["Amount"]; hasAmount {
				if !reflect.DeepEqual(amount, deliverMax) {
					t.Fatalf("transaction %v has conflicting Amount and DeliverMax", record["hash"])
				}
			} else {
				fields["Amount"] = deliverMax
			}
			delete(fields, "DeliverMax")
		}

		metadataValue, ok := record["metaData"].(map[string]any)
		if !ok {
			t.Fatalf("transaction %v has invalid metaData", record["hash"])
		}
		metadata := make(map[string]any, len(metadataValue))
		for key, value := range metadataValue {
			metadata[key] = value
		}
		for _, key := range []string{"delivered_amount", "nftoken_id", "offer_id"} {
			delete(metadata, key)
		}
		if mutateMetadata != nil {
			if err := mutateMetadata(metadata); err != nil {
				t.Fatal(err)
			}
		}

		txBlob, err := binarycodec.EncodeBytes(fields)
		if err != nil {
			t.Fatalf("encode transaction %v: %v", record["hash"], err)
		}
		metaBlob, err := binarycodec.EncodeBytesTrusted(metadata)
		if err != nil {
			t.Fatalf("encode metadata for transaction %v: %v", record["hash"], err)
		}
		vlTx, err := tx.EncodeWithVL(txBlob)
		if err != nil {
			t.Fatal(err)
		}
		vlMeta, err := tx.EncodeWithVL(metaBlob)
		if err != nil {
			t.Fatal(err)
		}
		id, err := hex.DecodeString(record["hash"].(string))
		if err != nil {
			t.Fatalf("decode transaction %v: %v", record["hash"], err)
		}
		if len(id) != 32 {
			t.Fatalf("transaction %v has %d-byte hash", record["hash"], len(id))
		}
		var hash [32]byte
		copy(hash[:], id)
		if err := tree.PutWithNodeType(hash, append(vlTx, vlMeta...), shamap.NodeTypeTransactionWithMeta); err != nil {
			t.Fatalf("insert transaction %v: %v", record["hash"], err)
		}
	}
	root, err := tree.Hash()
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", root)
}

func shiftTransactionIndex(from, to int) func(map[string]any) error {
	return func(metadata map[string]any) error {
		value, ok := metadata["TransactionIndex"].(float64)
		if !ok {
			return fmt.Errorf("TransactionIndex has type %T", metadata["TransactionIndex"])
		}
		index := int(value)
		switch {
		case index == from:
			metadata["TransactionIndex"] = to
		case index >= to && index < from:
			metadata["TransactionIndex"] = index + 1
		}
		return nil
	}
}

func transactionIndex(t *testing.T, metadata map[string]any) int {
	t.Helper()
	value, ok := metadata["TransactionIndex"].(float64)
	if !ok {
		t.Fatalf("TransactionIndex has type %T", metadata["TransactionIndex"])
	}
	return int(value)
}

func TestTestnetCheckCashIndexExplainsDivergence(t *testing.T) {
	tests := []struct {
		name             string
		fixture          string
		checkCashHash    string
		networkRoot      string
		earlyRoot        string
		localPrefix      string
		networkIndex     int
		earlyIndex       int
		transactionCount int
	}{
		{
			name:             "21705421",
			fixture:          "testnet-21705421.json",
			checkCashHash:    "D3888EB7F98B0D225887A43081D75518D6834802FBB89BE09B72B9088361F877",
			networkRoot:      "F7DCD5CF48C1B85D6075FCCA998EEADF8F3378A0E65B5C6A7BCD442B35A17778",
			earlyRoot:        "38F8800B5FE5B2A0B6ADF0C6C3C7410E7A17B84B87D9B3E609E43127E5EEA5B3",
			localPrefix:      "38f8800b5fe5b2a0",
			networkIndex:     38,
			earlyIndex:       9,
			transactionCount: 39,
		},
		{
			name:             "21707200",
			fixture:          "testnet-21707200.json",
			checkCashHash:    "1133024E8943EF44C24429ED5D665C3A53E46B683E56BA52F67AF5FEBE805179",
			networkRoot:      "803766444CFDA7389C66F2AEA7278E3CC9013C4E0CA6503F1E21D09DF80FE7BA",
			earlyRoot:        "AD4A03EE3CE0A492F9BD7660B20307F3D4C1D98E2934BF2446FBE6A98CCA1A7C",
			localPrefix:      "ad4a03ee3ce0a492",
			networkIndex:     5,
			earlyIndex:       1,
			transactionCount: 6,
		},
		{
			name:             "21707290",
			fixture:          "testnet-21707290.json",
			checkCashHash:    "4971BEE34D4A270FA6B45C39D91A4969410DADDE4613AD20D02EA16B8DFF9B28",
			networkRoot:      "CF0A1AC4F6A26886465D0B852714B918C15E915CC1311BBF7CF69AE49105276B",
			earlyRoot:        "2BC5C34C6841CCC87B7AB7E1BDC4322206134B9A4AB0E6AB25300DDD98EAB638",
			localPrefix:      "2bc5c34c6841ccc8",
			networkIndex:     31,
			earlyIndex:       24,
			transactionCount: 32,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := readTransactionRootFixture(t, test.fixture)
			ledger := fixture.Result.Ledger
			if len(ledger.Transactions) != test.transactionCount {
				t.Fatalf("transaction count = %d, want %d", len(ledger.Transactions), test.transactionCount)
			}
			if !strings.EqualFold(ledger.TransactionHash, test.networkRoot) {
				t.Fatalf("fixture transaction root = %s, want %s", ledger.TransactionHash, test.networkRoot)
			}

			var checkCash map[string]any
			for _, record := range ledger.Transactions {
				if strings.EqualFold(record["hash"].(string), test.checkCashHash) {
					if checkCash != nil {
						t.Fatalf("duplicate CheckCash transaction %s", test.checkCashHash)
					}
					checkCash = record
				}
			}
			if checkCash == nil {
				t.Fatalf("CheckCash transaction %s not found", test.checkCashHash)
			}
			if checkCash["TransactionType"] != "CheckCash" {
				t.Fatalf("selected transaction type = %v, want CheckCash", checkCash["TransactionType"])
			}
			metadata, ok := checkCash["metaData"].(map[string]any)
			if !ok {
				t.Fatal("selected transaction has invalid metaData")
			}
			if metadata["TransactionResult"] != "tecEXPIRED" {
				t.Fatalf("selected transaction result = %v, want tecEXPIRED", metadata["TransactionResult"])
			}
			if got := transactionIndex(t, metadata); got != test.networkIndex {
				t.Fatalf("selected transaction index = %d, want %d", got, test.networkIndex)
			}

			gotNetworkRoot := reconstructTransactionRoot(t, ledger.Transactions, nil)
			if !strings.EqualFold(gotNetworkRoot, test.networkRoot) {
				t.Fatalf("reconstructed network root = %s, want %s", gotNetworkRoot, test.networkRoot)
			}
			gotEarlyRoot := reconstructTransactionRoot(t, ledger.Transactions, shiftTransactionIndex(test.networkIndex, test.earlyIndex))
			if !strings.EqualFold(gotEarlyRoot, test.earlyRoot) {
				t.Fatalf("reconstructed early CheckCash root = %s, want %s", gotEarlyRoot, test.earlyRoot)
			}
			if !strings.HasPrefix(strings.ToLower(gotEarlyRoot), strings.ToLower(test.localPrefix)) {
				t.Fatalf("early CheckCash root = %s, want observed prefix %s", gotEarlyRoot, test.localPrefix)
			}
			t.Logf("network transaction root=%s early CheckCash root=%s", gotNetworkRoot, gotEarlyRoot)
		})
	}
}
