package jtx

import (
	"encoding/hex"

	"github.com/LeJamon/go-xrpl/keylet"
)

func CheckID(account *Account, sequence uint32) string {
	check := keylet.Check(account.ID, sequence)
	return hex.EncodeToString(check.Key[:])
}
