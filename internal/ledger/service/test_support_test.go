package service

import (
	"context"
	"encoding/hex"
	"strconv"
	"testing"

	appconfig "github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/require"
)

const historyWindow = uint32(appconfig.DefaultLedgerCacheSize)

func formatRange(min, max uint32) string {
	return strconv.FormatUint(uint64(min), 10) + "-" + strconv.FormatUint(uint64(max), 10)
}

func DefaultConfig() Config {
	return Config{
		Standalone:    true,
		GenesisConfig: genesis.DefaultConfig(),
	}
}

func (s *Service) getLedgerForQuery(ledgerIndex string) (*ledger.Ledger, bool, error) {
	return s.resolveLedgerForQuery(context.Background(), ledgerIndex)
}

func (s *Service) persistValidatedTip(ctx context.Context, l *ledger.Ledger) error {
	return s.persistValidatedTipJob(ctx, l, false, nil)
}

func encodeVLForTest(length int) []byte {
	switch {
	case length <= 192:
		return []byte{byte(length)}
	case length <= 12480:
		length -= 193
		return []byte{byte((length >> 8) + 193), byte(length)}
	default:
		length -= 12481
		return []byte{byte((length >> 16) + 241), byte(length >> 8), byte(length)}
	}
}

func makeTxMetaBlobForTest(t *testing.T, txBytes []byte, txIndex uint32, affectedAccounts ...string) ([]byte, [32]byte) {
	t.Helper()
	affectedNodes := make([]any, 0, len(affectedAccounts))
	for _, account := range affectedAccounts {
		affectedNodes = append(affectedNodes, map[string]any{
			"ModifiedNode": map[string]any{
				"FinalFields": map[string]any{"Account": account},
			},
		})
	}
	metaHex, err := binarycodec.Encode(map[string]any{
		"TransactionResult": "tesSUCCESS",
		"TransactionIndex":  txIndex,
		"AffectedNodes":     affectedNodes,
	})
	require.NoError(t, err)
	metaBytes, err := hex.DecodeString(metaHex)
	require.NoError(t, err)

	txID := sha512half.Sum(protocol.HashPrefixTransactionID().Bytes(), txBytes)
	blob := make([]byte, 0, len(txBytes)+len(metaBytes)+4)
	blob = append(blob, encodeVLForTest(len(txBytes))...)
	blob = append(blob, txBytes...)
	blob = append(blob, encodeVLForTest(len(metaBytes))...)
	blob = append(blob, metaBytes...)
	return blob, txID
}
