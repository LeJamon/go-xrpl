package service

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
)

// nftIDFromByte builds a deterministic 32-byte NFTokenID from a seed.
func nftIDFromByte(seed byte) [32]byte {
	var id [32]byte
	for i := range id {
		id[i] = seed + byte(i)
	}
	return id
}

// insertNFTokenOfferEntry serializes an NFTokenOffer ledger entry the way the
// goXRPL apply path does (sfAccount carries the owner) and inserts it under a
// real NFTokenOffer keylet. The returned key is what the NFT directory must
// reference. amount is either a drops string (XRP) or a {currency,issuer,value}
// map (IOU).
func insertNFTokenOfferEntry(t *testing.T, svc *Service, ownerAddr string, seq uint32, tokenID [32]byte, amount any, flags uint32, dest string, expiration *uint32) [32]byte {
	t.Helper()
	_, ownerBytes, err := addresscodec.DecodeClassicAddressToAccountID(ownerAddr)
	if err != nil {
		t.Fatalf("decode owner: %v", err)
	}
	var ownerID [20]byte
	copy(ownerID[:], ownerBytes)

	jsonObj := map[string]any{
		"LedgerEntryType":  "NFTokenOffer",
		"Account":          ownerAddr,
		"Amount":           amount,
		"NFTokenID":        strings.ToUpper(hex.EncodeToString(tokenID[:])),
		"OwnerNode":        "0",
		"NFTokenOfferNode": "0",
		"Flags":            flags,
	}
	if expiration != nil {
		jsonObj["Expiration"] = *expiration
	}
	if dest != "" {
		jsonObj["Destination"] = dest
	}

	data, err := binarycodec.EncodeBytes(jsonObj)
	if err != nil {
		t.Fatalf("encode NFTokenOffer: %v", err)
	}
	k := keylet.NFTokenOffer(ownerID, seq)
	if err := svc.openLedger.Insert(k, data); err != nil {
		t.Fatalf("insert NFTokenOffer: %v", err)
	}
	return k.Key
}

// insertNFTDir creates the buy/sell offer directory for an NFToken containing
// the given offer keys (in order), so parseDirectoryIndexesForNFT can walk it.
func insertNFTDir(t *testing.T, svc *Service, nftID [32]byte, offerKeys [][32]byte, isSell bool) {
	t.Helper()
	var dirKey keylet.Keylet
	if isSell {
		dirKey = keylet.NFTSells(nftID)
	} else {
		dirKey = keylet.NFTBuys(nftID)
	}
	dir := &state.DirectoryNode{
		RootIndex: dirKey.Key,
		Indexes:   offerKeys,
	}
	data, err := state.SerializeDirectoryNode(dir, false)
	if err != nil {
		t.Fatalf("serialize directory: %v", err)
	}
	if err := svc.openLedger.Insert(dirKey, data); err != nil {
		t.Fatalf("insert NFT directory: %v", err)
	}
}

func TestGetNFTOffers_DirectoryNotFound(t *testing.T) {
	svc := newOfferTestService(t)
	nftID := nftIDFromByte(0x10)

	_, err := svc.GetNFTBuyOffers(context.Background(), nftID, "current", 0, "")
	if !errors.Is(err, svcerr.ErrObjectNotFound) {
		t.Fatalf("missing buy directory must yield ErrObjectNotFound, got %v", err)
	}
	_, err = svc.GetNFTSellOffers(context.Background(), nftID, "current", 0, "")
	if !errors.Is(err, svcerr.ErrObjectNotFound) {
		t.Fatalf("missing sell directory must yield ErrObjectNotFound, got %v", err)
	}
}

func TestGetNFTOffers_EmptyDirectory(t *testing.T) {
	svc := newOfferTestService(t)
	nftID := nftIDFromByte(0x20)
	insertNFTDir(t, svc, nftID, nil, false)

	res, err := svc.GetNFTBuyOffers(context.Background(), nftID, "current", 0, "")
	if err != nil {
		t.Fatalf("GetNFTBuyOffers: %v", err)
	}
	if len(res.Offers) != 0 {
		t.Fatalf("empty directory must return 0 offers, got %d", len(res.Offers))
	}
	if res.NFTID != strings.ToUpper(hex.EncodeToString(nftID[:])) {
		t.Errorf("NFTID = %s, want hex of nftID", res.NFTID)
	}
}

func TestGetNFTSellOffers_XRPAndIOU(t *testing.T) {
	svc := newOfferTestService(t)
	ownerAddr, _ := addressFromBytes(t, 0x30)
	issuerAddr, _ := addressFromBytes(t, 0x40)
	nftID := nftIDFromByte(0x50)

	exp := uint32(800000000)
	destAddr, _ := addressFromBytes(t, 0x60)

	xrpKey := insertNFTokenOfferEntry(t, svc, ownerAddr, 1, nftID, "2500000", 1, "", nil)
	iouKey := insertNFTokenOfferEntry(t, svc, ownerAddr, 2, nftID,
		map[string]any{"currency": "USD", "issuer": issuerAddr, "value": "100"},
		1, destAddr, &exp)
	insertNFTDir(t, svc, nftID, [][32]byte{xrpKey, iouKey}, true)

	res, err := svc.GetNFTSellOffers(context.Background(), nftID, "current", 10, "")
	if err != nil {
		t.Fatalf("GetNFTSellOffers: %v", err)
	}
	if len(res.Offers) != 2 {
		t.Fatalf("expected 2 sell offers, got %d", len(res.Offers))
	}

	byIndex := map[string]NFTOfferInfo{}
	for _, o := range res.Offers {
		byIndex[o.NFTOfferIndex] = o
	}

	xrpOffer := byIndex[formatHashHex(xrpKey)]
	if amt, ok := xrpOffer.Amount.(string); !ok || amt != "2500000" {
		t.Errorf("XRP offer amount = %v (%T), want \"2500000\"", xrpOffer.Amount, xrpOffer.Amount)
	}
	if xrpOffer.Owner != ownerAddr {
		t.Errorf("XRP offer owner = %s, want %s", xrpOffer.Owner, ownerAddr)
	}

	iouOffer := byIndex[formatHashHex(iouKey)]
	amtMap, ok := iouOffer.Amount.(map[string]string)
	if !ok {
		t.Fatalf("IOU offer amount should be a map, got %T", iouOffer.Amount)
	}
	if amtMap["currency"] != "USD" || amtMap["issuer"] != issuerAddr {
		t.Errorf("IOU amount = %+v, want USD/%s", amtMap, issuerAddr)
	}
	if iouOffer.Destination != destAddr {
		t.Errorf("destination = %s, want %s", iouOffer.Destination, destAddr)
	}
	if iouOffer.Expiration != exp {
		t.Errorf("expiration = %d, want %d", iouOffer.Expiration, exp)
	}
}

func TestGetNFTBuyOffers_Pagination(t *testing.T) {
	svc := newOfferTestService(t)
	ownerAddr, _ := addressFromBytes(t, 0x30)
	nftID := nftIDFromByte(0x70)

	const total = 3
	keys := make([][32]byte, 0, total)
	for i := 0; i < total; i++ {
		k := insertNFTokenOfferEntry(t, svc, ownerAddr, uint32(i+1), nftID, "1000000", 0, "", nil)
		keys = append(keys, k)
	}
	insertNFTDir(t, svc, nftID, keys, false)

	// Page 1: limit 2 over 3 offers → 2 returned plus a marker.
	page1, err := svc.GetNFTBuyOffers(context.Background(), nftID, "current", 2, "")
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1.Offers) != 2 {
		t.Fatalf("page1 expected 2 offers, got %d", len(page1.Offers))
	}
	if page1.Marker == "" {
		t.Fatalf("page1 must emit a marker when more offers remain")
	}

	// Page 2: feed the marker back → remaining offer, no further marker.
	page2, err := svc.GetNFTBuyOffers(context.Background(), nftID, "current", 2, page1.Marker)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if page2.Marker != "" {
		t.Errorf("page2 should not emit a marker, got %q", page2.Marker)
	}

	seen := map[string]bool{}
	for _, o := range page1.Offers {
		seen[o.NFTOfferIndex] = true
	}
	for _, o := range page2.Offers {
		seen[o.NFTOfferIndex] = true
	}
	if len(seen) != total {
		t.Fatalf("expected %d distinct offers across pages, got %d", total, len(seen))
	}
}

func TestGetNFTOffers_InvalidMarkers(t *testing.T) {
	svc := newOfferTestService(t)
	ownerAddr, _ := addressFromBytes(t, 0x30)
	nftID := nftIDFromByte(0x80)
	otherNFT := nftIDFromByte(0x90)

	k1 := insertNFTokenOfferEntry(t, svc, ownerAddr, 1, nftID, "1000000", 0, "", nil)
	insertNFTDir(t, svc, nftID, [][32]byte{k1}, false)

	// An offer for a different NFT, not in this directory.
	wrongNFTKey := insertNFTokenOfferEntry(t, svc, ownerAddr, 2, otherNFT, "1000000", 0, "", nil)
	// A well-formed offer for THIS nft but absent from the directory.
	notInDirKey := insertNFTokenOfferEntry(t, svc, ownerAddr, 3, nftID, "1000000", 0, "", nil)

	cases := []struct {
		name   string
		marker string
	}{
		{"non-hex", strings.Repeat("Z", 64)},
		{"wrong-length", "DEADBEEF"},
		{"wrong-nft", formatHashHex(wrongNFTKey)},
		{"not-in-directory", formatHashHex(notInDirKey)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.GetNFTBuyOffers(context.Background(), nftID, "current", 2, tc.marker)
			if !errors.Is(err, svcerr.ErrInvalidMarker) {
				t.Fatalf("marker %q: want ErrInvalidMarker, got %v", tc.marker, err)
			}
		})
	}
}
