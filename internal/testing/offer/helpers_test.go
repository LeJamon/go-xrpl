package offer

import (
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

type offerHelperView struct {
	data         map[[32]byte][]byte
	readErr      error
	readErrByKey map[[32]byte]error
}

func (v offerHelperView) Read(k keylet.Keylet) ([]byte, error) {
	if v.readErr != nil {
		return nil, v.readErr
	}
	if err := v.readErrByKey[k.Key]; err != nil {
		return nil, err
	}
	return v.data[k.Key], nil
}

func (v offerHelperView) Exists(k keylet.Keylet) (bool, error) {
	_, err := v.Read(k)
	if err != nil {
		return false, err
	}
	_, ok := v.data[k.Key]
	return ok, nil
}

func (offerHelperView) Insert(keylet.Keylet, []byte) error { return nil }
func (offerHelperView) Update(keylet.Keylet, []byte) error { return nil }
func (offerHelperView) Erase(keylet.Keylet) error          { return nil }
func (offerHelperView) Rules() *amendment.Rules            { return nil }

func TestOffersOnAccountCheckedPropagatesLedgerFailures(t *testing.T) {
	owner := [20]byte{1}
	account := &jtx.Account{ID: owner, Address: "rTestOwner"}
	ownerDir := keylet.OwnerDir(owner)

	t.Run("directory read", func(t *testing.T) {
		_, err := offersOnAccount(offerHelperView{readErr: errors.New("directory read failed")}, account)
		require.EqualError(t, err, "directory read failed")
	})

	t.Run("directory parse", func(t *testing.T) {
		view := offerHelperView{data: map[[32]byte][]byte{ownerDir.Key: {0xff}}}
		_, err := offersOnAccount(view, account)
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to parse directory root")
	})

	t.Run("owned offer read", func(t *testing.T) {
		offerKey := [32]byte{2}
		dir, err := state.SerializeDirectoryNode(&state.DirectoryNode{
			Owner:   owner,
			Indexes: [][32]byte{offerKey},
		}, false)
		require.NoError(t, err)
		view := offerHelperView{
			data:         map[[32]byte][]byte{ownerDir.Key: dir},
			readErrByKey: map[[32]byte]error{offerKey: errors.New("owned entry read failed")},
		}
		_, err = offersOnAccount(view, account)
		require.EqualError(t, err, "read owned entry: owned entry read failed")
	})

	t.Run("owned offer parse", func(t *testing.T) {
		offerKey := [32]byte{3}
		dir, err := state.SerializeDirectoryNode(&state.DirectoryNode{
			Owner:   owner,
			Indexes: [][32]byte{offerKey},
		}, false)
		require.NoError(t, err)
		view := offerHelperView{data: map[[32]byte][]byte{
			ownerDir.Key: dir,
			offerKey:     {0xff},
		}}
		_, err = offersOnAccount(view, account)
		require.Error(t, err)
		require.Contains(t, err.Error(), "decode owned entry type")
	})
}
