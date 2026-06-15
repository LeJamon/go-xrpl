package tx

import (
	"bytes"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/keylet"
)

// mockBaseView implements LedgerView for core tx tests.
type mockBaseView struct {
	data map[[32]byte][]byte
}

func newMockBaseView() *mockBaseView {
	return &mockBaseView{data: make(map[[32]byte][]byte)}
}

func (m *mockBaseView) Read(k keylet.Keylet) ([]byte, error) {
	return m.data[k.Key], nil
}

func (m *mockBaseView) Exists(k keylet.Keylet) (bool, error) {
	_, ok := m.data[k.Key]
	return ok, nil
}

func (m *mockBaseView) Insert(k keylet.Keylet, data []byte) error {
	m.data[k.Key] = data
	return nil
}

func (m *mockBaseView) Update(k keylet.Keylet, data []byte) error {
	m.data[k.Key] = data
	return nil
}

func (m *mockBaseView) Erase(k keylet.Keylet) error {
	delete(m.data, k.Key)
	return nil
}

func (m *mockBaseView) AdjustDropsDestroyed(drops.XRPAmount) {}

func (m *mockBaseView) TxExists([32]byte) bool { return false }

func (m *mockBaseView) Rules() *amendment.Rules { return nil }

func (m *mockBaseView) LedgerSeq() uint32 { return 0 }

func (m *mockBaseView) ForEach(fn func(key [32]byte, data []byte) bool) error {
	for k, v := range m.data {
		if !fn(k, v) {
			break
		}
	}
	return nil
}

func (m *mockBaseView) Succ(key [32]byte) ([32]byte, []byte, bool, error) {
	var best [32]byte
	found := false
	for k := range m.data {
		if bytes.Compare(k[:], key[:]) > 0 {
			if !found || bytes.Compare(k[:], best[:]) < 0 {
				best = k
				found = true
			}
		}
	}
	if found {
		return best, m.data[best], true, nil
	}
	return [32]byte{}, nil, false, nil
}
