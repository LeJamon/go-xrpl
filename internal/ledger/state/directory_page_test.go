package state

import (
	"encoding/binary"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubView is an in-memory LedgerView for exercising the DirInsert/DirRemove
// page-management logic in isolation. It mirrors the read/write semantics of
// the production snapshotView (internal/ledger/service/snapshot_view.go): Read
// returns (nil, nil) on a miss, and Insert/Update both upsert. Keys are taken
// from keylet.Key only, exactly as the SHAMap-backed view does.
type stubView struct {
	entries map[[32]byte][]byte
	rules   *amendment.Rules
}

func newStubView() *stubView {
	return &stubView{entries: make(map[[32]byte][]byte)}
}

func (v *stubView) Read(k keylet.Keylet) ([]byte, error) {
	data, ok := v.entries[k.Key]
	if !ok {
		return nil, nil
	}
	return data, nil
}

func (v *stubView) Exists(k keylet.Keylet) (bool, error) {
	_, ok := v.entries[k.Key]
	return ok, nil
}

func (v *stubView) Insert(k keylet.Keylet, data []byte) error {
	v.entries[k.Key] = data
	return nil
}

func (v *stubView) Update(k keylet.Keylet, data []byte) error {
	v.entries[k.Key] = data
	return nil
}

func (v *stubView) Erase(k keylet.Keylet) error {
	delete(v.entries, k.Key)
	return nil
}

func (v *stubView) Rules() *amendment.Rules { return v.rules }

// hasPage reports whether page n of the directory rooted at dir exists.
func (v *stubView) hasPage(dir keylet.Keylet, n uint64) bool {
	_, ok := v.entries[keylet.DirPage(dir.Key, n).Key]
	return ok
}

// page reads and parses page n of the directory rooted at dir, failing the
// test if it does not exist.
func (v *stubView) page(t *testing.T, dir keylet.Keylet, n uint64) *DirectoryNode {
	t.Helper()
	data, ok := v.entries[keylet.DirPage(dir.Key, n).Key]
	require.Truef(t, ok, "page %d must exist", n)
	node, err := ParseDirectoryNode(data)
	require.NoErrorf(t, err, "parse page %d", n)
	return node
}

// itemKeyN builds a distinct directory entry key for the integer n. The value
// lands in the low 8 bytes big-endian, so larger n sorts after smaller n.
func itemKeyN(n int) [32]byte {
	var k [32]byte
	binary.BigEndian.PutUint64(k[24:], uint64(n))
	return k
}

// testDir returns a deterministic owner-directory keylet for tests.
func testDir() keylet.Keylet {
	var acct [20]byte
	for i := range acct {
		acct[i] = byte(i + 1)
	}
	return keylet.OwnerDir(acct)
}

// makePages inserts n empty, linked pages numbered [0, n-1] into the directory
// rooted at dir. It is a direct port of rippled's Directory_test.cpp makePages
// helper (src/test/ledger/Directory_test.cpp): each page's IndexNext points to
// the next page (0 on the last), and IndexPrevious points to the previous page
// (n-1 on the root, forming the circular back-link rippled uses).
func makePages(t *testing.T, v *stubView, dir keylet.Keylet, n uint64) {
	t.Helper()
	for i := range n {
		node := &DirectoryNode{RootIndex: dir.Key}
		if i+1 != n {
			node.IndexNext = i + 1
		}
		if i == 0 {
			node.IndexPrevious = n - 1
		} else {
			node.IndexPrevious = i - 1
		}
		data, err := SerializeDirectoryNode(node, false)
		require.NoErrorf(t, err, "serialize page %d", i)
		require.NoErrorf(t, v.Insert(keylet.DirPage(dir.Key, i), data), "insert page %d", i)
	}
}

// TestDirectoryNode_EmptyRoundTrip pins that an empty directory page (no
// Indexes) round-trips through serialize/parse, since makePages and the
// page-cleanup paths rely on empty pages existing in state.
func TestDirectoryNode_EmptyRoundTrip(t *testing.T) {
	t.Parallel()

	dir := testDir()
	node := &DirectoryNode{RootIndex: dir.Key, IndexNext: 1, IndexPrevious: 7}
	data, err := SerializeDirectoryNode(node, false)
	require.NoError(t, err)

	got, err := ParseDirectoryNode(data)
	require.NoError(t, err)
	assert.Empty(t, got.Indexes)
	assert.Equal(t, uint64(1), got.IndexNext)
	assert.Equal(t, uint64(7), got.IndexPrevious)
	assert.Equal(t, dir.Key, got.RootIndex)
}

// TestDirInsert_CreatesRoot covers the first insert into an empty directory:
// the root page (page 0) is created in place.
func TestDirInsert_CreatesRoot(t *testing.T) {
	t.Parallel()

	v := newStubView()
	dir := testDir()

	res, err := DirInsert(v, dir, itemKeyN(1), false, nil)
	require.NoError(t, err)
	assert.True(t, res.Created)
	assert.False(t, res.Modified)
	assert.Equal(t, uint64(0), res.Page)

	root := v.page(t, dir, 0)
	require.Len(t, root.Indexes, 1)
	assert.Equal(t, itemKeyN(1), root.Indexes[0])
	assert.Equal(t, uint64(0), root.IndexNext)
	assert.Equal(t, uint64(0), root.IndexPrevious)
}

// TestDirInsert_FillsRootPageNoSplit fills the root page to exactly the
// per-page entry cap and asserts no second page is created.
func TestDirInsert_FillsRootPageNoSplit(t *testing.T) {
	t.Parallel()

	v := newStubView()
	dir := testDir()

	for i := 1; i <= dirNodeMaxEntries; i++ {
		res, err := DirInsert(v, dir, itemKeyN(i), true, nil)
		require.NoErrorf(t, err, "insert %d", i)
		assert.Equalf(t, uint64(0), res.Page, "item %d must land on the root page", i)
		assert.False(t, res.NewPageCreated, "no split before the cap is exceeded")
	}

	root := v.page(t, dir, 0)
	assert.Len(t, root.Indexes, dirNodeMaxEntries)
	assert.Equal(t, uint64(0), root.IndexNext, "single full page has no next link")
	assert.False(t, v.hasPage(dir, 1), "page 1 must not exist yet")
}

// TestDirInsert_SplitsToSecondPage forces a split by inserting one past the
// per-page cap, then asserts the IndexNext/IndexPrevious links between the root
// and the freshly created page.
func TestDirInsert_SplitsToSecondPage(t *testing.T) {
	t.Parallel()

	v := newStubView()
	dir := testDir()

	for i := 1; i <= dirNodeMaxEntries; i++ {
		_, err := DirInsert(v, dir, itemKeyN(i), true, nil)
		require.NoError(t, err)
	}

	res, err := DirInsert(v, dir, itemKeyN(dirNodeMaxEntries+1), true, nil)
	require.NoError(t, err)
	assert.True(t, res.NewPageCreated)
	assert.True(t, res.RootModified)
	assert.Equal(t, uint64(1), res.Page, "the overflow item lands on page 1")

	root := v.page(t, dir, 0)
	assert.Len(t, root.Indexes, dirNodeMaxEntries)
	assert.Equal(t, uint64(1), root.IndexNext, "root forward-links to page 1")
	assert.Equal(t, uint64(1), root.IndexPrevious, "root back-links to the last page")

	page1 := v.page(t, dir, 1)
	require.Len(t, page1.Indexes, 1)
	assert.Equal(t, itemKeyN(dirNodeMaxEntries+1), page1.Indexes[0])
	assert.Equal(t, uint64(0), page1.IndexNext, "page 1 is the last page")
	assert.Equal(t, uint64(0), page1.IndexPrevious, "page 1 back-links to root (page 0)")
}

// TestDirInsert_ThreePageLinks builds a three-page chain and verifies the full
// IndexNext/IndexPrevious wiring across pages 0, 1 and 2.
func TestDirInsert_ThreePageLinks(t *testing.T) {
	t.Parallel()

	v := newStubView()
	dir := testDir()

	total := 2*dirNodeMaxEntries + 1 // 65: pages of 32, 32, 1
	for i := 1; i <= total; i++ {
		_, err := DirInsert(v, dir, itemKeyN(i), true, nil)
		require.NoErrorf(t, err, "insert %d", i)
	}

	root := v.page(t, dir, 0)
	assert.Len(t, root.Indexes, dirNodeMaxEntries)
	assert.Equal(t, uint64(1), root.IndexNext)
	assert.Equal(t, uint64(2), root.IndexPrevious, "root back-links to the last page")

	page1 := v.page(t, dir, 1)
	assert.Len(t, page1.Indexes, dirNodeMaxEntries)
	assert.Equal(t, uint64(0), page1.IndexPrevious)
	assert.Equal(t, uint64(2), page1.IndexNext)

	page2 := v.page(t, dir, 2)
	assert.Len(t, page2.Indexes, 1)
	assert.Equal(t, uint64(1), page2.IndexPrevious)
	assert.Equal(t, uint64(0), page2.IndexNext, "last page has no forward link")
}

// TestDirInsert_SortsWithinPageAndRejectsDuplicate covers the owner-directory
// path (preserveOrder=false): entries are kept sorted within a page, and a
// repeated key is rejected with rippled's "double insertion" guard.
func TestDirInsert_SortsWithinPageAndRejectsDuplicate(t *testing.T) {
	t.Parallel()

	v := newStubView()
	dir := testDir()

	// Insert in descending order so the sort has to reorder them.
	for i := 5; i >= 1; i-- {
		_, err := DirInsert(v, dir, itemKeyN(i), false, nil)
		require.NoErrorf(t, err, "insert %d", i)
	}

	root := v.page(t, dir, 0)
	require.Len(t, root.Indexes, 5)
	for i := 1; i < len(root.Indexes); i++ {
		assert.Truef(t,
			lessIndex(root.Indexes[i-1], root.Indexes[i]),
			"indexes must be ascending at position %d", i)
	}

	_, err := DirInsert(v, dir, itemKeyN(3), false, nil)
	require.Error(t, err, "re-inserting an existing key must fail")
	assert.Contains(t, err.Error(), "double insertion")
}

// TestDirInsert_PreserveOrderAppends covers the book-directory path
// (preserveOrder=true): entries keep insertion order within a page.
func TestDirInsert_PreserveOrderAppends(t *testing.T) {
	t.Parallel()

	v := newStubView()
	dir := testDir()

	order := []int{5, 2, 9, 1, 7}
	for _, n := range order {
		_, err := DirInsert(v, dir, itemKeyN(n), true, nil)
		require.NoErrorf(t, err, "insert %d", n)
	}

	root := v.page(t, dir, 0)
	require.Len(t, root.Indexes, len(order))
	for i, n := range order {
		assert.Equalf(t, itemKeyN(n), root.Indexes[i], "position %d preserves insertion order", i)
	}
}

func lessIndex(a, b [32]byte) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// TestDirRemove_ModifiesPage removes one of several entries on a page and
// asserts the page is updated (not deleted) with the rest intact.
func TestDirRemove_ModifiesPage(t *testing.T) {
	t.Parallel()

	v := newStubView()
	dir := testDir()
	for i := 1; i <= 3; i++ {
		_, err := DirInsert(v, dir, itemKeyN(i), false, nil)
		require.NoError(t, err)
	}

	res, err := DirRemove(v, dir, 0, itemKeyN(2), false)
	require.NoError(t, err)
	assert.True(t, res.Success)
	assert.True(t, res.PageModified)
	assert.False(t, res.PageDeleted)
	assert.False(t, res.RootDeleted)

	root := v.page(t, dir, 0)
	require.Len(t, root.Indexes, 2)
	assert.NotContains(t, root.Indexes, itemKeyN(2))
	assert.Contains(t, root.Indexes, itemKeyN(1))
	assert.Contains(t, root.Indexes, itemKeyN(3))
}

// TestDirRemove_NotFound returns success=false when the key is absent.
func TestDirRemove_NotFound(t *testing.T) {
	t.Parallel()

	v := newStubView()
	dir := testDir()
	_, err := DirInsert(v, dir, itemKeyN(1), false, nil)
	require.NoError(t, err)

	res, err := DirRemove(v, dir, 0, itemKeyN(99), false)
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.False(t, res.PageModified)
	assert.True(t, v.hasPage(dir, 0), "root must be untouched")
}

// TestDirRemove_RootRetainedWhenKeepRoot empties the only page with keepRoot
// set; the root page must survive (empty) rather than be erased.
func TestDirRemove_RootRetainedWhenKeepRoot(t *testing.T) {
	t.Parallel()

	v := newStubView()
	dir := testDir()
	_, err := DirInsert(v, dir, itemKeyN(1), false, nil)
	require.NoError(t, err)

	res, err := DirRemove(v, dir, 0, itemKeyN(1), true)
	require.NoError(t, err)
	assert.True(t, res.Success)
	assert.False(t, res.RootDeleted, "keepRoot must retain the root page")
	require.True(t, v.hasPage(dir, 0), "root page must still exist")
	assert.Empty(t, v.page(t, dir, 0).Indexes, "retained root is empty")
}

// TestDirRemove_RootErasedWhenEmpty empties the only page without keepRoot; the
// whole directory must be erased.
func TestDirRemove_RootErasedWhenEmpty(t *testing.T) {
	t.Parallel()

	v := newStubView()
	dir := testDir()
	_, err := DirInsert(v, dir, itemKeyN(1), false, nil)
	require.NoError(t, err)

	res, err := DirRemove(v, dir, 0, itemKeyN(1), false)
	require.NoError(t, err)
	assert.True(t, res.Success)
	assert.True(t, res.PageDeleted)
	assert.True(t, res.RootDeleted)
	assert.False(t, v.hasPage(dir, 0), "empty root must be erased")
}

// TestDirRemove_SplitThenDrainErasesAllPages exercises the full insert/remove
// life cycle: grow a directory across three real pages via DirInsert, then
// remove every entry and assert all pages are gone.
func TestDirRemove_SplitThenDrainErasesAllPages(t *testing.T) {
	t.Parallel()

	v := newStubView()
	dir := testDir()

	total := 2*dirNodeMaxEntries + 1
	pageOf := make(map[int]uint64, total)
	for i := 1; i <= total; i++ {
		res, err := DirInsert(v, dir, itemKeyN(i), true, nil)
		require.NoErrorf(t, err, "insert %d", i)
		pageOf[i] = res.Page
	}

	for i := total; i >= 1; i-- {
		res, err := DirRemove(v, dir, pageOf[i], itemKeyN(i), false)
		require.NoErrorf(t, err, "remove %d", i)
		assert.Truef(t, res.Success, "remove %d must succeed", i)
	}

	assert.False(t, v.hasPage(dir, 0), "root erased after draining")
	assert.False(t, v.hasPage(dir, 1), "page 1 erased after draining")
	assert.False(t, v.hasPage(dir, 2), "page 2 erased after draining")
}

// TestDirRemove_DrainsSoleNonRootPageResetsRootLinks isolates the shared-page
// branch of DirRemove: draining the only non-root page makes prevPage and
// nextPage both resolve to the root, so rippled updates IndexNext and
// IndexPrevious on one shared SLE (ApplyView.cpp:303-315). Pins that the root's
// back-link is reset to 0 at the drain, not left dangling at the erased page.
func TestDirRemove_DrainsSoleNonRootPageResetsRootLinks(t *testing.T) {
	t.Parallel()

	v := newStubView()
	dir := testDir()

	// 32 items fill the root; the 33rd spills onto page 1 (the sole non-root page).
	for i := 1; i <= dirNodeMaxEntries+1; i++ {
		_, err := DirInsert(v, dir, itemKeyN(i), true, nil)
		require.NoErrorf(t, err, "insert %d", i)
	}
	require.True(t, v.hasPage(dir, 1), "precondition: page 1 exists")

	res, err := DirRemove(v, dir, 1, itemKeyN(dirNodeMaxEntries+1), false)
	require.NoError(t, err)
	assert.True(t, res.Success)

	assert.False(t, v.hasPage(dir, 1), "drained non-root page must be erased")

	root := v.page(t, dir, 0)
	assert.Len(t, root.Indexes, dirNodeMaxEntries, "root keeps its own entries")
	assert.Equal(t, uint64(0), root.IndexNext, "root forward-link reset to itself")
	assert.Equal(t, uint64(0), root.IndexPrevious,
		"root back-link reset, not left dangling at the erased page")
}

// TestDirRemove_CollapsedRootKeepsPresentZeroLinks pins the fix for issue 983.
// When a multi-page directory drains back to a single root page, rippled writes
// the root's IndexNext / IndexPrevious to 0 with setFieldU64, keeping both links
// *present* in the serialized SLE. go-xrpl previously dropped them because the
// value was 0, forking the SLE bytes (account_hash) and — because the metadata
// layer re-parses those bytes for its present-field set — the ModifiedNode
// FinalFields (transaction_hash). A fresh single-page root, by contrast, never
// has the links and must keep omitting them.
func TestDirRemove_CollapsedRootKeepsPresentZeroLinks(t *testing.T) {
	t.Parallel()

	v := newStubView()
	dir := testDir()

	// A fresh single-page root must omit both links entirely (rippled's
	// dirAdd never writes them — "save some space"): nothing has grown yet.
	_, err := DirInsert(v, dir, itemKeyN(1), false, nil)
	require.NoError(t, err)
	freshData, ok := v.entries[keylet.DirPage(dir.Key, 0).Key]
	require.True(t, ok)
	freshFields, err := binarycodec.DecodeBytes(freshData)
	require.NoError(t, err)
	_, hasNext := freshFields["IndexNext"]
	_, hasPrev := freshFields["IndexPrevious"]
	assert.False(t, hasNext, "fresh single-page root must omit IndexNext")
	assert.False(t, hasPrev, "fresh single-page root must omit IndexPrevious")

	// Grow to two pages: entries 2..33 (33 total) overflow the root onto page 1.
	for i := 2; i <= dirNodeMaxEntries+1; i++ {
		_, err := DirInsert(v, dir, itemKeyN(i), false, nil)
		require.NoErrorf(t, err, "insert %d", i)
	}
	require.True(t, v.hasPage(dir, 1), "precondition: page 1 exists")

	// Drain page 1, collapsing the directory back to the root page.
	res, err := DirRemove(v, dir, 1, v.page(t, dir, 1).Indexes[0], false)
	require.NoError(t, err)
	require.True(t, res.Success)
	require.False(t, v.hasPage(dir, 1), "collapsed page must be erased")

	rootData, ok := v.entries[keylet.DirPage(dir.Key, 0).Key]
	require.True(t, ok, "root page must survive the collapse")

	// account_hash level: the serialized SLE must carry both links at value 0.
	fields, err := binarycodec.DecodeBytes(rootData)
	require.NoError(t, err)
	assert.Equal(t, "0", fields["IndexNext"], "collapsed root keeps IndexNext present at 0")
	assert.Equal(t, "0", fields["IndexPrevious"], "collapsed root keeps IndexPrevious present at 0")

	// Round-trip: parse recovers both as present, which is what drives the
	// ModifiedNode FinalFields present-field set.
	root, err := ParseDirectoryNode(rootData)
	require.NoError(t, err)
	assert.True(t, root.indexNextSet, "IndexNext present after collapse")
	assert.True(t, root.indexPreviousSet, "IndexPrevious present after collapse")
	assert.Equal(t, uint64(0), root.IndexNext)
	assert.Equal(t, uint64(0), root.IndexPrevious)
}

// TestDirRemove_EmptyChainThreePages is a direct port of rippled's
// Directory_test.cpp "Empty Chain on Delete" first case: a three-page chain
// with a single item on the middle page; removing it must cascade-delete every
// page in the chain.
func TestDirRemove_EmptyChainThreePages(t *testing.T) {
	t.Parallel()

	v := newStubView()
	dir := testDir()
	makePages(t, v, dir, 3)

	// Put a single item on the middle page (page 1).
	item := itemKeyN(42)
	page1 := v.page(t, dir, 1)
	page1.Indexes = [][32]byte{item}
	data, err := SerializeDirectoryNode(page1, false)
	require.NoError(t, err)
	require.NoError(t, v.Update(keylet.DirPage(dir.Key, 1), data))

	res, err := DirRemove(v, dir, 1, item, false)
	require.NoError(t, err)
	assert.True(t, res.Success)

	assert.False(t, v.hasPage(dir, 2), "page 2 must be deleted")
	assert.False(t, v.hasPage(dir, 1), "page 1 must be deleted")
	assert.False(t, v.hasPage(dir, 0), "page 0 must be deleted")
}

// TestDirRemove_EmptyChainFourPages is a direct port of rippled's
// Directory_test.cpp "Empty Chain on Delete" second case: a four-page chain
// with items on pages 1 and 2. Removing the item on page 2 deletes pages 2 and
// 3, and re-links page 1 and the root.
func TestDirRemove_EmptyChainFourPages(t *testing.T) {
	t.Parallel()

	v := newStubView()
	dir := testDir()
	makePages(t, v, dir, 4)

	itemA := itemKeyN(1) // page 1
	itemB := itemKeyN(2) // page 2

	page1 := v.page(t, dir, 1)
	page1.Indexes = [][32]byte{itemA}
	d1, err := SerializeDirectoryNode(page1, false)
	require.NoError(t, err)
	require.NoError(t, v.Update(keylet.DirPage(dir.Key, 1), d1))

	page2 := v.page(t, dir, 2)
	page2.Indexes = [][32]byte{itemB}
	d2, err := SerializeDirectoryNode(page2, false)
	require.NoError(t, err)
	require.NoError(t, v.Update(keylet.DirPage(dir.Key, 2), d2))

	res, err := DirRemove(v, dir, 2, itemB, false)
	require.NoError(t, err)
	assert.True(t, res.Success)

	assert.False(t, v.hasPage(dir, 3), "page 3 must be deleted")
	assert.False(t, v.hasPage(dir, 2), "page 2 must be deleted")

	require.True(t, v.hasPage(dir, 1), "page 1 must survive")
	p1 := v.page(t, dir, 1)
	assert.Equal(t, uint64(0), p1.IndexNext, "page 1 becomes the last page")
	assert.Equal(t, uint64(0), p1.IndexPrevious, "page 1 back-links to root")

	require.True(t, v.hasPage(dir, 0), "root must survive")
	p0 := v.page(t, dir, 0)
	assert.Equal(t, uint64(1), p0.IndexNext, "root forward-links to page 1")
	assert.Equal(t, uint64(1), p0.IndexPrevious, "root back-links to page 1")
}

// TestDirForEach_AcrossPages walks a multi-page directory and confirms every
// inserted entry is visited in page/insertion order.
func TestDirForEach_AcrossPages(t *testing.T) {
	t.Parallel()

	v := newStubView()
	dir := testDir()

	total := 2*dirNodeMaxEntries + 1
	want := make([][32]byte, 0, total)
	for i := 1; i <= total; i++ {
		_, err := DirInsert(v, dir, itemKeyN(i), true, nil)
		require.NoErrorf(t, err, "insert %d", i)
		want = append(want, itemKeyN(i))
	}

	got := make([][32]byte, 0, total)
	err := DirForEach(v, dir, func(k [32]byte) error {
		got = append(got, k)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, want, got, "DirForEach visits every entry in insertion order")
}
