package lending

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/types"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/ledgerfields"
	"github.com/LeJamon/go-xrpl/internal/tx/lending/lmath"
)

// The lending ledger objects carry their NUMBER fields (DebtTotal, CoverAvailable,
// PrincipalOutstanding, ...) as the canonical decimal/scientific string the binary
// codec round-trips; "" means the field is absent (soeDEFAULT zero). This mirrors
// the vault package's local Number seam.

// lendNum parses a NUMBER field string into a large-scale XRPLNumber (the scale a
// lending transaction context installs). "" / "0" decode to zero.
func lendNum(s string) lmath.N {
	if s == "" || s == "0" {
		return lmath.Zero()
	}
	num := &types.Number{}
	b, err := num.FromJSON(s)
	if err != nil {
		return lmath.Zero()
	}
	mantissa := int64(binary.BigEndian.Uint64(b[:8]))
	exp := int32(binary.BigEndian.Uint32(b[8:12]))
	return lmath.Num(mantissa, int(exp))
}

// numStr renders a large-scale XRPLNumber into the NUMBER-field convention: "" for
// zero, else a scientific string the codec re-normalizes to the identical value.
func numStr(n lmath.N) string {
	if n.IsZero() {
		return ""
	}
	return fmt.Sprintf("%de%d", n.Mantissa(), n.Exponent())
}

// loanBrokerData is the parsed form of an ltLOAN_BROKER ledger entry.
type loanBrokerData struct {
	Sequence             uint32
	OwnerNode            uint64
	VaultNode            uint64
	VaultID              [32]byte
	Account              [20]byte // pseudo-account
	Owner                [20]byte
	LoanSequence         uint32
	Data                 string // hex Blob
	ManagementFeeRate    uint16
	OwnerCount           uint32
	DebtTotal            string // NUMBER
	DebtMaximum          string // NUMBER
	CoverAvailable       string // NUMBER
	CoverRateMinimum     uint32
	CoverRateLiquidation uint32
	Flags                uint32
	PreviousTxnID        [32]byte
	PreviousTxnLgrSeq    uint32
}

// serializeLoanBroker encodes a LoanBroker entry to canonical binary. soeDEFAULT
// fields are omitted when at their default to match rippled's STObject encoding.
func serializeLoanBroker(b *loanBrokerData) ([]byte, error) {
	ownerAddr, err := state.EncodeAccountID(b.Owner)
	if err != nil {
		return nil, fmt.Errorf("encode owner: %w", err)
	}
	pseudoAddr, err := state.EncodeAccountID(b.Account)
	if err != nil {
		return nil, fmt.Errorf("encode account: %w", err)
	}
	obj := map[string]any{
		"LedgerEntryType": "LoanBroker",
		"Flags":           b.Flags,
		"Sequence":        b.Sequence,
		"OwnerNode":       fmt.Sprintf("%X", b.OwnerNode),
		"VaultNode":       fmt.Sprintf("%X", b.VaultNode),
		"VaultID":         strings.ToUpper(hex.EncodeToString(b.VaultID[:])),
		"Account":         pseudoAddr,
		"Owner":           ownerAddr,
		"LoanSequence":    b.LoanSequence,
	}
	if b.Data != "" {
		obj["Data"] = strings.ToUpper(b.Data)
	}
	if b.ManagementFeeRate != 0 {
		obj["ManagementFeeRate"] = uint32(b.ManagementFeeRate)
	}
	if b.OwnerCount != 0 {
		obj["OwnerCount"] = b.OwnerCount
	}
	if b.DebtTotal != "" {
		obj["DebtTotal"] = b.DebtTotal
	}
	if b.DebtMaximum != "" {
		obj["DebtMaximum"] = b.DebtMaximum
	}
	if b.CoverAvailable != "" {
		obj["CoverAvailable"] = b.CoverAvailable
	}
	if b.CoverRateMinimum != 0 {
		obj["CoverRateMinimum"] = b.CoverRateMinimum
	}
	if b.CoverRateLiquidation != 0 {
		obj["CoverRateLiquidation"] = b.CoverRateLiquidation
	}
	var zeroHash [32]byte
	if b.PreviousTxnID != zeroHash {
		obj["PreviousTxnID"] = strings.ToUpper(hex.EncodeToString(b.PreviousTxnID[:]))
		obj["PreviousTxnLgrSeq"] = b.PreviousTxnLgrSeq
	}
	hexStr, err := binarycodec.Encode(obj)
	if err != nil {
		return nil, fmt.Errorf("encode loan broker: %w", err)
	}
	return hex.DecodeString(hexStr)
}

// parseLoanBroker decodes a LoanBroker entry via the ledgerfields decoder.
func parseLoanBroker(data []byte) (*loanBrokerData, error) {
	lb := &ledgerfields.LoanBroker{}
	if err := lb.Decode(data); err != nil {
		return nil, err
	}
	b := &loanBrokerData{
		Sequence:             lb.Sequence,
		LoanSequence:         lb.LoanSequence,
		Data:                 lb.Data,
		ManagementFeeRate:    uint16(lb.ManagementFeeRate),
		OwnerCount:           lb.OwnerCount,
		DebtTotal:            normNum(lb.DebtTotal),
		DebtMaximum:          normNum(lb.DebtMaximum),
		CoverAvailable:       normNum(lb.CoverAvailable),
		CoverRateMinimum:     lb.CoverRateMinimum,
		CoverRateLiquidation: lb.CoverRateLiquidation,
		Flags:                lb.Flags,
		PreviousTxnLgrSeq:    lb.PreviousTxnLgrSeq,
	}
	if id, err := state.DecodeAccountID(lb.Account); err == nil {
		b.Account = id
	}
	if id, err := state.DecodeAccountID(lb.Owner); err == nil {
		b.Owner = id
	}
	b.VaultID = hash256(lb.VaultID)
	b.PreviousTxnID = hash256(lb.PreviousTxnID)
	b.OwnerNode = hexU64(lb.OwnerNode)
	b.VaultNode = hexU64(lb.VaultNode)
	return b, nil
}

// loanData is the parsed form of an ltLOAN ledger entry.
type loanData struct {
	OwnerNode                uint64
	LoanBrokerNode           uint64
	LoanBrokerID             [32]byte
	LoanSequence             uint32
	Borrower                 [20]byte
	LoanOriginationFee       string // NUMBER
	LoanServiceFee           string // NUMBER
	LatePaymentFee           string // NUMBER
	ClosePaymentFee          string // NUMBER
	OverpaymentFee           uint32
	InterestRate             uint32
	LateInterestRate         uint32
	CloseInterestRate        uint32
	OverpaymentInterestRate  uint32
	StartDate                uint32
	PaymentInterval          uint32
	GracePeriod              uint32
	PreviousPaymentDueDate   uint32
	NextPaymentDueDate       uint32
	PaymentRemaining         uint32
	PeriodicPayment          string // NUMBER (soeREQUIRED)
	PrincipalOutstanding     string // NUMBER
	TotalValueOutstanding    string // NUMBER
	ManagementFeeOutstanding string // NUMBER
	LoanScale                int32
	Flags                    uint32
	PreviousTxnID            [32]byte
	PreviousTxnLgrSeq        uint32
}

// serializeLoan encodes a Loan entry to canonical binary.
func serializeLoan(l *loanData) ([]byte, error) {
	borrowerAddr, err := state.EncodeAccountID(l.Borrower)
	if err != nil {
		return nil, fmt.Errorf("encode borrower: %w", err)
	}
	obj := map[string]any{
		"LedgerEntryType": "Loan",
		"Flags":           l.Flags,
		"OwnerNode":       fmt.Sprintf("%X", l.OwnerNode),
		"LoanBrokerNode":  fmt.Sprintf("%X", l.LoanBrokerNode),
		"LoanBrokerID":    strings.ToUpper(hex.EncodeToString(l.LoanBrokerID[:])),
		"LoanSequence":    l.LoanSequence,
		"Borrower":        borrowerAddr,
		"StartDate":       l.StartDate,
		"PaymentInterval": l.PaymentInterval,
		"PeriodicPayment": defaultNum(l.PeriodicPayment),
	}
	putNum(obj, "LoanOriginationFee", l.LoanOriginationFee)
	putNum(obj, "LoanServiceFee", l.LoanServiceFee)
	putNum(obj, "LatePaymentFee", l.LatePaymentFee)
	putNum(obj, "ClosePaymentFee", l.ClosePaymentFee)
	putU32(obj, "OverpaymentFee", l.OverpaymentFee)
	putU32(obj, "InterestRate", l.InterestRate)
	putU32(obj, "LateInterestRate", l.LateInterestRate)
	putU32(obj, "CloseInterestRate", l.CloseInterestRate)
	putU32(obj, "OverpaymentInterestRate", l.OverpaymentInterestRate)
	putU32(obj, "GracePeriod", l.GracePeriod)
	putU32(obj, "PreviousPaymentDueDate", l.PreviousPaymentDueDate)
	putU32(obj, "NextPaymentDueDate", l.NextPaymentDueDate)
	putU32(obj, "PaymentRemaining", l.PaymentRemaining)
	putNum(obj, "PrincipalOutstanding", l.PrincipalOutstanding)
	putNum(obj, "TotalValueOutstanding", l.TotalValueOutstanding)
	putNum(obj, "ManagementFeeOutstanding", l.ManagementFeeOutstanding)
	if l.LoanScale != 0 {
		obj["LoanScale"] = l.LoanScale
	}
	var zeroHash [32]byte
	if l.PreviousTxnID != zeroHash {
		obj["PreviousTxnID"] = strings.ToUpper(hex.EncodeToString(l.PreviousTxnID[:]))
		obj["PreviousTxnLgrSeq"] = l.PreviousTxnLgrSeq
	}
	hexStr, err := binarycodec.Encode(obj)
	if err != nil {
		return nil, fmt.Errorf("encode loan: %w", err)
	}
	return hex.DecodeString(hexStr)
}

// parseLoan decodes a Loan entry via the ledgerfields decoder.
func parseLoan(data []byte) (*loanData, error) {
	ll := &ledgerfields.Loan{}
	if err := ll.Decode(data); err != nil {
		return nil, err
	}
	l := &loanData{
		LoanSequence:             ll.LoanSequence,
		LoanOriginationFee:       normNum(ll.LoanOriginationFee),
		LoanServiceFee:           normNum(ll.LoanServiceFee),
		LatePaymentFee:           normNum(ll.LatePaymentFee),
		ClosePaymentFee:          normNum(ll.ClosePaymentFee),
		OverpaymentFee:           ll.OverpaymentFee,
		InterestRate:             ll.InterestRate,
		LateInterestRate:         ll.LateInterestRate,
		CloseInterestRate:        ll.CloseInterestRate,
		OverpaymentInterestRate:  ll.OverpaymentInterestRate,
		StartDate:                ll.StartDate,
		PaymentInterval:          ll.PaymentInterval,
		GracePeriod:              ll.GracePeriod,
		PreviousPaymentDueDate:   ll.PreviousPaymentDueDate,
		NextPaymentDueDate:       ll.NextPaymentDueDate,
		PaymentRemaining:         ll.PaymentRemaining,
		PeriodicPayment:          normNum(ll.PeriodicPayment),
		PrincipalOutstanding:     normNum(ll.PrincipalOutstanding),
		TotalValueOutstanding:    normNum(ll.TotalValueOutstanding),
		ManagementFeeOutstanding: normNum(ll.ManagementFeeOutstanding),
		LoanScale:                int32(ll.LoanScale),
		Flags:                    ll.Flags,
		PreviousTxnLgrSeq:        ll.PreviousTxnLgrSeq,
	}
	if id, err := state.DecodeAccountID(ll.Borrower); err == nil {
		l.Borrower = id
	}
	l.LoanBrokerID = hash256(ll.LoanBrokerID)
	l.PreviousTxnID = hash256(ll.PreviousTxnID)
	l.OwnerNode = hexU64(ll.OwnerNode)
	l.LoanBrokerNode = hexU64(ll.LoanBrokerNode)
	return l, nil
}

// --- small encoding helpers ---

func normNum(v any) string {
	s, ok := v.(string)
	if !ok || s == "" || s == "0" {
		return ""
	}
	return s
}

// defaultNum renders a soeREQUIRED NUMBER: it is always present, so an empty
// (zero) value serializes as "0".
func defaultNum(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

func putNum(obj map[string]any, key, s string) {
	if s != "" {
		obj[key] = s
	}
}

func putU32(obj map[string]any, key string, v uint32) {
	if v != 0 {
		obj[key] = v
	}
}

func hash256(s string) [32]byte {
	var h [32]byte
	if b, err := hex.DecodeString(s); err == nil && len(b) == 32 {
		copy(h[:], b)
	}
	return h
}

func hexU64(s string) uint64 {
	n, _ := strconv.ParseUint(s, 16, 64)
	return n
}
