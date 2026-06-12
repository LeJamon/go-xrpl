package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// doorAccountID is the AccountID of r3e7qTG44Mg8pHXgxPtyRx286Re5Urtx2p.
var doorAccountID = []byte{
	83, 223, 129, 195, 127, 70, 21, 146, 66, 247, 202, 145,
	99, 224, 159, 4, 64, 41, 204, 18,
}

// issuerAccountID is the AccountID of rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn.
var issuerAccountID = []byte{
	174, 18, 58, 133, 86, 243, 207, 145, 21, 71,
	17, 55, 106, 251, 15, 137, 79, 131, 43, 61,
}

// xrpXrpBridgeJSON is an XRP→XRP bridge in rippled's JSON shape: door
// accounts as address strings, issues as Issue objects.
func xrpXrpBridgeJSON() map[string]any {
	return map[string]any{
		"LockingChainDoor":  "r3e7qTG44Mg8pHXgxPtyRx286Re5Urtx2p",
		"LockingChainIssue": map[string]any{"currency": "XRP"},
		"IssuingChainDoor":  "r3e7qTG44Mg8pHXgxPtyRx286Re5Urtx2p",
		"IssuingChainIssue": map[string]any{"currency": "XRP"},
	}
}

// xrpXrpBridgeBytes is the rippled wire form of xrpXrpBridgeJSON:
// STAccount (VL 0x14 + 20 bytes) then STIssue (20 zero bytes for XRP), twice.
func xrpXrpBridgeBytes() []byte {
	side := append([]byte{0x14}, doorAccountID...)
	side = append(side, make([]byte, 20)...)
	return append(append([]byte{}, side...), side...)
}

// iouBridgeBytes is a bridge whose issuing side carries an IOU issue
// (currency + issuer, 40 bytes).
func iouBridgeBytes() []byte {
	out := append([]byte{0x14}, doorAccountID...)
	out = append(out, make([]byte, 20)...) // locking issue: XRP
	out = append(out, 0x14)
	out = append(out, doorAccountID...)
	out = append(out, []byte{
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 85, 83, 68, 0, 0, 0, 0, 0, // "USD"
	}...)
	out = append(out, issuerAccountID...)
	return out
}

func iouBridgeJSON() map[string]any {
	return map[string]any{
		"LockingChainDoor":  "r3e7qTG44Mg8pHXgxPtyRx286Re5Urtx2p",
		"LockingChainIssue": map[string]any{"currency": "XRP"},
		"IssuingChainDoor":  "r3e7qTG44Mg8pHXgxPtyRx286Re5Urtx2p",
		"IssuingChainIssue": map[string]any{
			"currency": "USD",
			"issuer":   "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn",
		},
	}
}

func TestXChainBridge_FromJson(t *testing.T) {
	tt := []struct {
		name string
		json any
		want []byte
		err  bool
	}{
		{
			name: "valid XRP-XRP bridge",
			json: xrpXrpBridgeJSON(),
			want: xrpXrpBridgeBytes(),
		},
		{
			name: "valid bridge with IOU issuing issue",
			json: iouBridgeJSON(),
			want: iouBridgeBytes(),
		},
		{
			name: "invalid LockingChainDoor classic address",
			json: map[string]any{
				"LockingChainDoor":  "r3e7qTG44Mg8pHXgxPtyRx286Re5Urtx2p1",
				"LockingChainIssue": map[string]any{"currency": "XRP"},
				"IssuingChainDoor":  "r3e7qTG44Mg8pHXgxPtyRx286Re5Urtx2p",
				"IssuingChainIssue": map[string]any{"currency": "XRP"},
			},
			err: true,
		},
		{
			name: "issue as plain string is rejected, not parsed as address",
			json: map[string]any{
				"LockingChainDoor":  "r3e7qTG44Mg8pHXgxPtyRx286Re5Urtx2p",
				"LockingChainIssue": "r3e7qTG44Mg8pHXgxPtyRx286Re5Urtx2p",
				"IssuingChainDoor":  "r3e7qTG44Mg8pHXgxPtyRx286Re5Urtx2p",
				"IssuingChainIssue": "r3e7qTG44Mg8pHXgxPtyRx286Re5Urtx2p",
			},
			err: true,
		},
		{
			name: "non-string door does not panic",
			json: map[string]any{
				"LockingChainDoor":  42,
				"LockingChainIssue": map[string]any{"currency": "XRP"},
				"IssuingChainDoor":  "r3e7qTG44Mg8pHXgxPtyRx286Re5Urtx2p",
				"IssuingChainIssue": map[string]any{"currency": "XRP"},
			},
			err: true,
		},
		{
			name: "missing field",
			json: map[string]any{
				"LockingChainDoor":  "r3e7qTG44Mg8pHXgxPtyRx286Re5Urtx2p",
				"IssuingChainDoor":  "r3e7qTG44Mg8pHXgxPtyRx286Re5Urtx2p",
				"IssuingChainIssue": map[string]any{"currency": "XRP"},
			},
			err: true,
		},
		{
			name: "not a valid json",
			json: "not a valid json",
			err:  true,
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			xcb := &XChainBridge{}
			got, err := xcb.FromJSON(tc.json)
			if tc.err {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestXChainBridge_ToJson(t *testing.T) {
	tt := []struct {
		name  string
		input []byte
		want  map[string]any
		err   bool
	}{
		{
			name:  "valid XRP-XRP bridge",
			input: xrpXrpBridgeBytes(),
			want:  xrpXrpBridgeJSON(),
		},
		{
			name:  "valid bridge with IOU issuing issue",
			input: iouBridgeBytes(),
			want:  iouBridgeJSON(),
		},
		{
			name: "legacy 80-byte fixed encoding is rejected",
			// Four raw 20-byte values without VL prefixes: the first byte (83)
			// is read as a VL length != 20.
			input: append(append(append(append([]byte{}, doorAccountID...), doorAccountID...), doorAccountID...), doorAccountID...),
			err:   true,
		},
		{
			name:  "truncated bridge",
			input: xrpXrpBridgeBytes()[:30],
			err:   true,
		},
		{
			name:  "empty input",
			input: []byte{},
			err:   true,
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			xcb := &XChainBridge{}
			got, err := xcb.ToJSON(testParser(tc.input))
			if tc.err {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestXChainBridge_RoundTrip asserts decode(encode(x)) == x and
// encode(decode(b)) == b for both issue shapes.
func TestXChainBridge_RoundTrip(t *testing.T) {
	for _, blob := range [][]byte{xrpXrpBridgeBytes(), iouBridgeBytes()} {
		xcb := &XChainBridge{}
		decoded, err := xcb.ToJSON(testParser(blob))
		require.NoError(t, err)
		reencoded, err := xcb.FromJSON(decoded)
		require.NoError(t, err)
		require.Equal(t, blob, reencoded)
	}
}
