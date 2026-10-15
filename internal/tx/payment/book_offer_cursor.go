package payment

import (
	"bytes"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
)

// bookOfferCursor belongs to one forEachOffer pass. Crossing only removes book
// entries, so a page snapshot preserves insertion order across index compaction.
// Keeping its next link also lets the walk advance after the page is unlinked.
// Offer contents and funding are read from the live sandbox by the caller.
type bookOfferCursor struct {
	root     [32]byte
	page     keylet.Keylet
	indexes  [][32]byte
	nextPage uint64
	done     bool
}

func (c *bookOfferCursor) next(sb *PaymentSandbox) ([32]byte, bool) {
	for !c.done {
		if len(c.indexes) > 0 {
			key := c.indexes[0]
			c.indexes = c.indexes[1:]
			return key, true
		}

		var data []byte
		if c.nextPage != 0 {
			c.page = keylet.DirPage(c.root, c.nextPage)
			c.nextPage = 0
			var err error
			data, err = sb.Read(c.page)
			if err != nil || data == nil {
				continue
			}
		} else {
			key, foundData, found, err := sb.Succ(c.root)
			if err != nil || !found || !bytes.Equal(key[:24], c.root[:24]) {
				c.done = true
				return [32]byte{}, false
			}
			c.root = key
			c.page = keylet.DirPage(key, 0)
			data = foundData
		}

		dir, err := state.ParseDirectoryNode(data)
		if err != nil {
			continue
		}
		c.indexes = dir.Indexes
		c.nextPage = dir.IndexNext
	}
	return [32]byte{}, false
}
