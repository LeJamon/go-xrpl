package service

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
)

// lsfCredentialAccepted mirrors credential.LsfCredentialAccepted; duplicated
// here to avoid importing the tx/credential package into the service tests.
const lsfCredentialAccepted uint32 = 0x00010000

// buildNFTokenID composes a 32-byte NFTokenID with the supplied flags, transfer
// fee, issuer, (deciphered) taxon and serial, applying rippled's taxon cipher so
// extractNFTInfo recovers the same taxon.
func buildNFTokenID(flags, transferFee uint16, issuerID [20]byte, taxon, serial uint32) [32]byte {
	var id [32]byte
	binary.BigEndian.PutUint16(id[0:2], flags)
	binary.BigEndian.PutUint16(id[2:4], transferFee)
	copy(id[4:24], issuerID[:])
	cipheredTaxon := taxon ^ ((serial ^ 384160001) * 2357503715)
	binary.BigEndian.PutUint32(id[24:28], cipheredTaxon)
	binary.BigEndian.PutUint32(id[28:32], serial)
	return id
}

// insertNFTokenPageEntry inserts an NFTokenPage owned by the account holding the
// given tokens. The page key carries the account ID in its first 20 bytes, which
// is how GetAccountNFTs attributes the page.
func insertNFTokenPageEntry(t *testing.T, svc *Service, ownerAddr string, tokens []state.NFTokenData) {
	t.Helper()
	_, ownerBytes, err := addresscodec.DecodeClassicAddressToAccountID(ownerAddr)
	if err != nil {
		t.Fatalf("decode owner: %v", err)
	}
	var ownerID [20]byte
	copy(ownerID[:], ownerBytes)

	nftArray := make([]any, 0, len(tokens))
	for _, tok := range tokens {
		nftArray = append(nftArray, map[string]any{
			"NFToken": map[string]any{
				"NFTokenID": strings.ToUpper(hex.EncodeToString(tok.NFTokenID[:])),
				"URI":       tok.URI,
			},
		})
	}
	jsonObj := map[string]any{
		"LedgerEntryType": "NFTokenPage",
		"NFTokens":        nftArray,
	}
	data, err := binarycodec.EncodeBytes(jsonObj)
	if err != nil {
		t.Fatalf("encode NFTokenPage: %v", err)
	}
	if err := svc.openLedger.Insert(keylet.NFTokenPageMax(ownerID), data); err != nil {
		t.Fatalf("insert NFTokenPage: %v", err)
	}
}

func TestGetAccountNFTs_DecodesTokenFields(t *testing.T) {
	svc := newOfferTestService(t)
	ownerAddr, _ := addressFromBytes(t, 0x20)
	insertAccountRoot(t, svc, ownerAddr, 1_000_000_000_000, 0)

	issuerAddr, issuerID := addressFromBytes(t, 0x40)
	const (
		flags       = uint16(0x0001)
		transferFee = uint16(314)
		taxon       = uint32(98765)
		serial      = uint32(7)
	)
	tokenID := buildNFTokenID(flags, transferFee, issuerID, taxon, serial)
	uriHex := hex.EncodeToString([]byte("ipfs://test"))

	insertNFTokenPageEntry(t, svc, ownerAddr, []state.NFTokenData{
		{NFTokenID: tokenID, URI: uriHex},
	})

	res, err := svc.GetAccountNFTs(context.Background(), ownerAddr, "current", 0)
	if err != nil {
		t.Fatalf("GetAccountNFTs: %v", err)
	}
	if len(res.AccountNFTs) != 1 {
		t.Fatalf("expected 1 NFT, got %d", len(res.AccountNFTs))
	}
	nft := res.AccountNFTs[0]
	if nft.Flags != flags {
		t.Errorf("flags = %d, want %d", nft.Flags, flags)
	}
	if nft.TransferFee != transferFee {
		t.Errorf("transfer_fee = %d, want %d", nft.TransferFee, transferFee)
	}
	if nft.NFTokenTaxon != taxon {
		t.Errorf("taxon = %d, want %d (cipher round-trip)", nft.NFTokenTaxon, taxon)
	}
	if nft.NFTSerial != serial {
		t.Errorf("serial = %d, want %d", nft.NFTSerial, serial)
	}
	if nft.Issuer != issuerAddr {
		t.Errorf("issuer = %s, want %s", nft.Issuer, issuerAddr)
	}
	if nft.NFTokenID != strings.ToUpper(hex.EncodeToString(tokenID[:])) {
		t.Errorf("NFTokenID = %s, want hex of tokenID", nft.NFTokenID)
	}

	t.Run("account not found", func(t *testing.T) {
		stranger, _ := addressFromBytes(t, 0x99)
		_, err := svc.GetAccountNFTs(context.Background(), stranger, "current", 0)
		if !errors.Is(err, svcerr.ErrAccountNotFound) {
			t.Fatalf("want ErrAccountNotFound, got %v", err)
		}
	})
}

// insertCredentialEntry inserts a Credential ledger entry and returns its key.
// The hex of that key is the credential ID a client passes to deposit_authorized.
func insertCredentialEntry(t *testing.T, svc *Service, subjectID, issuerID [20]byte, credType []byte, accepted bool, expiration *uint32) [32]byte {
	t.Helper()
	subjectAddr, err := addresscodec.EncodeAccountIDToClassicAddress(subjectID[:])
	if err != nil {
		t.Fatalf("encode subject: %v", err)
	}
	issuerAddr, err := addresscodec.EncodeAccountIDToClassicAddress(issuerID[:])
	if err != nil {
		t.Fatalf("encode issuer: %v", err)
	}
	jsonObj := map[string]any{
		"LedgerEntryType": "Credential",
		"Subject":         subjectAddr,
		"Issuer":          issuerAddr,
		"CredentialType":  hex.EncodeToString(credType),
		"IssuerNode":      "0",
		"SubjectNode":     "0",
	}
	if accepted {
		jsonObj["Flags"] = lsfCredentialAccepted
	}
	if expiration != nil {
		jsonObj["Expiration"] = *expiration
	}
	data, err := binarycodec.EncodeBytes(jsonObj)
	if err != nil {
		t.Fatalf("encode credential: %v", err)
	}
	k := keylet.Credential(subjectID, issuerID, credType)
	if err := svc.openLedger.Insert(k, data); err != nil {
		t.Fatalf("insert credential: %v", err)
	}
	return k.Key
}

func TestGetDepositAuthorized_AccountChecks(t *testing.T) {
	svc := newOfferTestService(t)
	srcAddr, _ := addressFromBytes(t, 0x10)
	dstAddr, _ := addressFromBytes(t, 0x20)
	insertAccountRoot(t, svc, srcAddr, 1_000_000_000, 0)
	insertAccountRoot(t, svc, dstAddr, 1_000_000_000, 0)

	t.Run("no deposit auth → authorized", func(t *testing.T) {
		res, err := svc.GetDepositAuthorized(context.Background(), srcAddr, dstAddr, "current", nil)
		if err != nil {
			t.Fatalf("GetDepositAuthorized: %v", err)
		}
		if !res.DepositAuthorized {
			t.Errorf("absent DepositAuth flag must authorize")
		}
	})

	t.Run("source not found", func(t *testing.T) {
		stranger, _ := addressFromBytes(t, 0x98)
		_, err := svc.GetDepositAuthorized(context.Background(), stranger, dstAddr, "current", nil)
		if !errors.Is(err, svcerr.ErrSrcAccountNotFound) {
			t.Fatalf("want ErrSrcAccountNotFound, got %v", err)
		}
	})

	t.Run("destination not found", func(t *testing.T) {
		stranger, _ := addressFromBytes(t, 0x97)
		_, err := svc.GetDepositAuthorized(context.Background(), srcAddr, stranger, "current", nil)
		if !errors.Is(err, svcerr.ErrDstAccountNotFound) {
			t.Fatalf("want ErrDstAccountNotFound, got %v", err)
		}
	})
}

func TestGetDepositAuthorized_DepositAuthFlag(t *testing.T) {
	svc := newOfferTestService(t)
	srcAddr, srcID := addressFromBytes(t, 0x10)
	dstAddr, dstID := addressFromBytes(t, 0x20)
	insertAccountRoot(t, svc, srcAddr, 1_000_000_000, 0)
	insertAccountRootWithFlags(t, svc, dstAddr, 1_000_000_000, 0, state.LsfDepositAuth)

	t.Run("required but no preauth → not authorized", func(t *testing.T) {
		res, err := svc.GetDepositAuthorized(context.Background(), srcAddr, dstAddr, "current", nil)
		if err != nil {
			t.Fatalf("GetDepositAuthorized: %v", err)
		}
		if res.DepositAuthorized {
			t.Errorf("DepositAuth set without preauth must NOT authorize")
		}
	})

	t.Run("self deposit always authorized", func(t *testing.T) {
		res, err := svc.GetDepositAuthorized(context.Background(), dstAddr, dstAddr, "current", nil)
		if err != nil {
			t.Fatalf("GetDepositAuthorized: %v", err)
		}
		if !res.DepositAuthorized {
			t.Errorf("self-deposit must always authorize")
		}
	})

	t.Run("direct preauth → authorized", func(t *testing.T) {
		preauthKey := keylet.DepositPreauth(dstID, srcID)
		if err := svc.openLedger.Insert(preauthKey, make([]byte, 16)); err != nil {
			t.Fatalf("insert deposit preauth: %v", err)
		}
		res, err := svc.GetDepositAuthorized(context.Background(), srcAddr, dstAddr, "current", nil)
		if err != nil {
			t.Fatalf("GetDepositAuthorized: %v", err)
		}
		if !res.DepositAuthorized {
			t.Errorf("direct DepositPreauth entry must authorize")
		}
	})
}

func TestGetDepositAuthorized_Credentials(t *testing.T) {
	svc := newOfferTestService(t)
	srcAddr, srcID := addressFromBytes(t, 0x10)
	dstAddr, dstID := addressFromBytes(t, 0x20)
	_, issuerID := addressFromBytes(t, 0x40)
	insertAccountRoot(t, svc, srcAddr, 1_000_000_000, 0)
	insertAccountRootWithFlags(t, svc, dstAddr, 1_000_000_000, 0, state.LsfDepositAuth)

	credType := []byte("KYC")

	t.Run("malformed credential hex", func(t *testing.T) {
		_, err := svc.GetDepositAuthorized(context.Background(), srcAddr, dstAddr, "current", []string{"nothex!!"})
		if !errors.Is(err, svcerr.ErrBadCredentials) {
			t.Fatalf("want ErrBadCredentials, got %v", err)
		}
	})

	t.Run("non-existent credential", func(t *testing.T) {
		_, err := svc.GetDepositAuthorized(context.Background(), srcAddr, dstAddr, "current",
			[]string{strings.Repeat("AB", 32)})
		if !errors.Is(err, svcerr.ErrBadCredentials) {
			t.Fatalf("want ErrBadCredentials, got %v", err)
		}
	})

	t.Run("duplicate non-existent hashes report don't exist", func(t *testing.T) {
		// rippled detects duplicates by (issuer, credentialType) read from the
		// ledger, so identical unknown hashes fail the existence check first.
		id := strings.Repeat("CD", 32)
		_, err := svc.GetDepositAuthorized(context.Background(), srcAddr, dstAddr, "current",
			[]string{id, id})
		if !errors.Is(err, svcerr.ErrBadCredentials) {
			t.Fatalf("want ErrBadCredentials, got %v", err)
		}
		if !strings.Contains(err.Error(), "credentials don't exist") {
			t.Fatalf("want \"credentials don't exist\" detail, got %v", err)
		}
	})

	t.Run("unaccepted credential", func(t *testing.T) {
		key := insertCredentialEntry(t, svc, srcID, issuerID, []byte("PENDING"), false, nil)
		_, err := svc.GetDepositAuthorized(context.Background(), srcAddr, dstAddr, "current",
			[]string{formatHashHex(key)})
		if !errors.Is(err, svcerr.ErrBadCredentials) {
			t.Fatalf("want ErrBadCredentials (not accepted), got %v", err)
		}
	})

	t.Run("wrong subject", func(t *testing.T) {
		// Credential whose subject is NOT the source account.
		_, otherID := addressFromBytes(t, 0x50)
		key := insertCredentialEntry(t, svc, otherID, issuerID, []byte("OTHER"), true, nil)
		_, err := svc.GetDepositAuthorized(context.Background(), srcAddr, dstAddr, "current",
			[]string{formatHashHex(key)})
		if !errors.Is(err, svcerr.ErrBadCredentials) {
			t.Fatalf("want ErrBadCredentials (wrong subject), got %v", err)
		}
	})

	t.Run("duplicate credentials", func(t *testing.T) {
		key := insertCredentialEntry(t, svc, srcID, issuerID, []byte("DUP"), true, nil)
		hexID := formatHashHex(key)
		_, err := svc.GetDepositAuthorized(context.Background(), srcAddr, dstAddr, "current",
			[]string{hexID, hexID})
		if !errors.Is(err, svcerr.ErrBadCredentials) {
			t.Fatalf("want ErrBadCredentials (duplicate), got %v", err)
		}
		if !strings.Contains(err.Error(), "duplicates in credentials") {
			t.Fatalf("want \"duplicates in credentials\" detail, got %v", err)
		}
	})

	t.Run("expired credential", func(t *testing.T) {
		// Expiration in the distant past (Ripple epoch): parentCloseTime > exp.
		exp := uint32(1)
		key := insertCredentialEntry(t, svc, srcID, issuerID, []byte("EXPIRED"), true, &exp)
		_, err := svc.GetDepositAuthorized(context.Background(), srcAddr, dstAddr, "current",
			[]string{formatHashHex(key)})
		if !errors.Is(err, svcerr.ErrBadCredentials) {
			t.Fatalf("want ErrBadCredentials (expired), got %v", err)
		}
		if !strings.Contains(err.Error(), "credentials are expired") {
			t.Fatalf("want \"credentials are expired\" detail, got %v", err)
		}
	})

	t.Run("future expiration is not expired", func(t *testing.T) {
		// Expiration one hour ahead in Ripple-epoch seconds. Guards against
		// comparing a Ripple-epoch expiration with Unix-epoch close time,
		// which would falsely expire every credential.
		exp := uint32(toRippleTime(time.Now().Add(time.Hour)))
		key := insertCredentialEntry(t, svc, srcID, issuerID, []byte("FUTURE"), true, &exp)
		res, err := svc.GetDepositAuthorized(context.Background(), srcAddr, dstAddr, "current",
			[]string{formatHashHex(key)})
		if err != nil {
			t.Fatalf("GetDepositAuthorized: %v", err)
		}
		if res.DepositAuthorized {
			t.Errorf("no credential preauth entry: must not authorize")
		}
	})

	t.Run("valid credential with credential preauth → authorized", func(t *testing.T) {
		key := insertCredentialEntry(t, svc, srcID, issuerID, credType, true, nil)
		pairs := []keylet.CredentialPair{{Issuer: issuerID, CredentialType: credType}}
		credPreauthKey := keylet.DepositPreauthCredentials(dstID, pairs)
		if err := svc.openLedger.Insert(credPreauthKey, make([]byte, 16)); err != nil {
			t.Fatalf("insert credential preauth: %v", err)
		}
		res, err := svc.GetDepositAuthorized(context.Background(), srcAddr, dstAddr, "current",
			[]string{formatHashHex(key)})
		if err != nil {
			t.Fatalf("GetDepositAuthorized: %v", err)
		}
		if !res.DepositAuthorized {
			t.Errorf("valid credential + credential preauth must authorize")
		}
	})
}
