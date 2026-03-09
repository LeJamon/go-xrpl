package testing

import (
	"github.com/LeJamon/goXRPLd/internal/tx"
)

// TxResult represents the result of applying a transaction.
type TxResult struct {
	// Code is the transaction engine result code (e.g., "tesSUCCESS").
	Code string

	// Success indicates whether the transaction was successfully applied.
	Success bool

	// Message provides additional details about the result.
	Message string

	// Metadata contains the transaction metadata (AffectedNodes, etc.).
	Metadata *tx.Metadata
}

// Common transaction result codes.
const (
	// Success codes (applied to ledger)
	tesSUCCESS = "tesSUCCESS"

	// Claim codes (claimed cost only, not applied)
	tecCLAIM                 = "tecCLAIM"
	tecPATH_PARTIAL          = "tecPATH_PARTIAL"
	tecUNFUNDED_ADD          = "tecUNFUNDED_ADD"
	tecUNFUNDED_OFFER        = "tecUNFUNDED_OFFER"
	tecUNFUNDED_PAYMENT      = "tecUNFUNDED_PAYMENT"
	tecFAILED_PROCESSING     = "tecFAILED_PROCESSING"
	tecDIR_FULL              = "tecDIR_FULL"
	tecINSUF_RESERVE_LINE    = "tecINSUF_RESERVE_LINE"
	tecINSUF_RESERVE_OFFER   = "tecINSUF_RESERVE_OFFER"
	tecNO_DST                = "tecNO_DST"
	tecNO_DST_INSUF_XRP      = "tecNO_DST_INSUF_XRP"
	tecNO_LINE_INSUF_RESERVE = "tecNO_LINE_INSUF_RESERVE"
	tecNO_LINE_REDUNDANT     = "tecNO_LINE_REDUNDANT"
	tecPATH_DRY              = "tecPATH_DRY"
	tecUNFUNDED              = "tecUNFUNDED"
	tecNO_ALTERNATIVE_KEY    = "tecNO_ALTERNATIVE_KEY"
	tecNO_REGULAR_KEY        = "tecNO_REGULAR_KEY"
	tecOWNERS                = "tecOWNERS"
	tecNO_ISSUER             = "tecNO_ISSUER"
	tecNO_AUTH               = "tecNO_AUTH"
	tecNO_LINE               = "tecNO_LINE"
	tecINSUFF_FEE            = "tecINSUFF_FEE"
	tecFROZEN                = "tecFROZEN"
	tecNO_TARGET             = "tecNO_TARGET"
	tecNO_PERMISSION         = "tecNO_PERMISSION"
	tecNO_ENTRY              = "tecNO_ENTRY"
	tecINSUFFICIENT_RESERVE  = "tecINSUFFICIENT_RESERVE"
	tecNEED_MASTER_KEY       = "tecNEED_MASTER_KEY"
	tecDST_TAG_NEEDED        = "tecDST_TAG_NEEDED"
	tecINTERNAL              = "tecINTERNAL"
	tecOVERSIZE              = "tecOVERSIZE"
	tecCRYPTOCONDITION_ERROR = "tecCRYPTOCONDITION_ERROR"
	tecINVARIANT_FAILED      = "tecINVARIANT_FAILED"
	tecNO_SUITABLE_NFTOKEN_PAGE = "tecNO_SUITABLE_NFTOKEN_PAGE"
	tecDUPLICATE             = "tecDUPLICATE"
	tecCANT_ACCEPT_OWN_NFTOKEN_OFFER = "tecCANT_ACCEPT_OWN_NFTOKEN_OFFER"
	tecEXPIRED               = "tecEXPIRED"

	// Failure codes (not applied, retry possible)
	tefFAILURE          = "tefFAILURE"
	tefALREADY          = "tefALREADY"
	tefBAD_ADD_AUTH     = "tefBAD_ADD_AUTH"
	tefBAD_AUTH         = "tefBAD_AUTH"
	tefBAD_LEDGER       = "tefBAD_LEDGER"
	tefCREATED          = "tefCREATED"
	tefEXCEPTION        = "tefEXCEPTION"
	tefINTERNAL         = "tefINTERNAL"
	tefNO_AUTH_REQUIRED = "tefNO_AUTH_REQUIRED"
	tefPAST_SEQ         = "tefPAST_SEQ"
	tefWRONG_PRIOR      = "tefWRONG_PRIOR"
	tefMASTER_DISABLED  = "tefMASTER_DISABLED"
	tefMAX_LEDGER       = "tefMAX_LEDGER"
	tefBAD_SIGNATURE    = "tefBAD_SIGNATURE"
	tefBAD_QUORUM       = "tefBAD_QUORUM"
	tefNOT_MULTI_SIGNING = "tefNOT_MULTI_SIGNING"
	tefBAD_AUTH_MASTER  = "tefBAD_AUTH_MASTER"
	tefINVARIANT_FAILED = "tefINVARIANT_FAILED"
	tefTOO_BIG          = "tefTOO_BIG"

	// Retry codes (not applied, retry later)
	terRETRY         = "terRETRY"
	terFUNDS_SPENT   = "terFUNDS_SPENT"
	terINSUF_FEE_B   = "terINSUF_FEE_B"
	terNO_ACCOUNT    = "terNO_ACCOUNT"
	terNO_AUTH       = "terNO_AUTH"
	terNO_LINE       = "terNO_LINE"
	terOWNERS        = "terOWNERS"
	terPRE_SEQ       = "terPRE_SEQ"
	terLAST          = "terLAST"
	terNO_RIPPLE     = "terNO_RIPPLE"
	terQUEUED        = "terQUEUED"

	// Malformed transaction codes (invalid transaction format)
	temMALFORMED             = "temMALFORMED"
	temBAD_AMOUNT            = "temBAD_AMOUNT"
	temBAD_CURRENCY          = "temBAD_CURRENCY"
	temBAD_EXPIRATION        = "temBAD_EXPIRATION"
	temBAD_FEE               = "temBAD_FEE"
	temBAD_ISSUER            = "temBAD_ISSUER"
	temBAD_LIMIT             = "temBAD_LIMIT"
	temBAD_OFFER             = "temBAD_OFFER"
	temBAD_PATH              = "temBAD_PATH"
	temBAD_PATH_LOOP         = "temBAD_PATH_LOOP"
	temBAD_REGKEY            = "temBAD_REGKEY"
	temBAD_SEND_XRP_LIMIT    = "temBAD_SEND_XRP_LIMIT"
	temBAD_SEND_XRP_MAX      = "temBAD_SEND_XRP_MAX"
	temBAD_SEND_XRP_NO_DIRECT = "temBAD_SEND_XRP_NO_DIRECT"
	temBAD_SEND_XRP_PARTIAL  = "temBAD_SEND_XRP_PARTIAL"
	temBAD_SEND_XRP_PATHS    = "temBAD_SEND_XRP_PATHS"
	temBAD_SEQUENCE          = "temBAD_SEQUENCE"
	temBAD_SIGNATURE         = "temBAD_SIGNATURE"
	temBAD_SRC_ACCOUNT       = "temBAD_SRC_ACCOUNT"
	temBAD_TRANSFER_RATE     = "temBAD_TRANSFER_RATE"
	temDST_IS_SRC            = "temDST_IS_SRC"
	temDST_NEEDED            = "temDST_NEEDED"
	temINVALID               = "temINVALID"
	temINVALID_FLAG          = "temINVALID_FLAG"
	temREDUNDANT             = "temREDUNDANT"
	temRIPPLE_EMPTY          = "temRIPPLE_EMPTY"
	temDISABLED              = "temDISABLED"
	temBAD_SIGNER            = "temBAD_SIGNER"
	temBAD_QUORUM            = "temBAD_QUORUM"
	temBAD_WEIGHT            = "temBAD_WEIGHT"
	temBAD_TICK_SIZE         = "temBAD_TICK_SIZE"
	temINVALID_ACCOUNT_ID    = "temINVALID_ACCOUNT_ID"
	temCANNOT_PREAUTH_SELF   = "temCANNOT_PREAUTH_SELF"
	temUNCERTAIN             = "temUNCERTAIN"
	temUNKNOWN               = "temUNKNOWN"
	temSEQ_AND_TICKET        = "temSEQ_AND_TICKET"
	temBAD_NFTOKEN_TRANSFER_FEE = "temBAD_NFTOKEN_TRANSFER_FEE"
)

// ResultSuccess returns a successful transaction result.
func ResultSuccess() TxResult {
	return TxResult{
		Code:    tesSUCCESS,
		Success: true,
		Message: "The transaction was applied.",
	}
}

// ResultWithCode creates a TxResult with the specified code.
func ResultWithCode(code string, success bool, message string) TxResult {
	return TxResult{
		Code:    code,
		Success: success,
		Message: message,
	}
}

// IsSuccess returns true if the result code indicates success.
func (r TxResult) IsSuccess() bool {
	return r.Code == tesSUCCESS
}

// IsClaimed returns true if the result code indicates the fee was claimed but
// the transaction was not applied (tec codes).
func (r TxResult) IsClaimed() bool {
	if len(r.Code) < 3 {
		return false
	}
	return r.Code[:3] == "tec"
}

// IsRetry returns true if the result code indicates a retry is possible.
func (r TxResult) IsRetry() bool {
	if len(r.Code) < 3 {
		return false
	}
	return r.Code[:3] == "ter"
}

// IsMalformed returns true if the result code indicates the transaction is malformed.
func (r TxResult) IsMalformed() bool {
	if len(r.Code) < 3 {
		return false
	}
	return r.Code[:3] == "tem"
}

// IsFailed returns true if the result code indicates a failure.
func (r TxResult) IsFailed() bool {
	if len(r.Code) < 3 {
		return false
	}
	return r.Code[:3] == "tef"
}
