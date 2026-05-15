package tx

import (
	"errors"
	"fmt"
)

// ResultError is a structured validation error that carries a typed Result code.
// It eliminates the need for string-prefix matching in parseValidationError.
type ResultError struct {
	Code   Result
	Detail string
}

func (e *ResultError) Error() string {
	if e.Detail == "" {
		return e.Code.String()
	}
	return e.Code.String() + ": " + e.Detail
}

// Errorf creates a ResultError with the given code and formatted detail message.
func Errorf(code Result, format string, args ...any) error {
	return &ResultError{
		Code:   code,
		Detail: fmt.Sprintf(format, args...),
	}
}

// AsResultError extracts a ResultError from err, if present.
func AsResultError(err error) (*ResultError, bool) {
	var re *ResultError
	if errors.As(err, &re) {
		return re, true
	}
	return nil, false
}

// Result represents a transaction result code
type Result int

// Transaction result codes matching rippled exactly
// These are organized by category: tes, tec, tef, tel, tem, ter
const (
	// tesSUCCESS and related (0-99)
	TesSUCCESS Result = 0

	// tecCLAIM and other "claimed cost" codes (100-199)
	// Transaction succeeded but with a caveat
	TecCLAIM                              Result = 100
	TecPATH_PARTIAL                       Result = 101
	TecUNFUNDED_ADD                       Result = 102
	TecUNFUNDED_OFFER                     Result = 103
	TecUNFUNDED_PAYMENT                   Result = 104
	TecFAILED_PROCESSING                  Result = 105
	TecDIR_FULL                           Result = 121
	TecINSUF_RESERVE_LINE                 Result = 122
	TecINSUF_RESERVE_OFFER                Result = 123
	TecNO_DST                             Result = 124
	TecNO_DST_INSUF_XRP                   Result = 125
	TecNO_LINE_INSUF_RESERVE              Result = 126
	TecNO_LINE_REDUNDANT                  Result = 127
	TecPATH_DRY                           Result = 128
	TecUNFUNDED                           Result = 129
	TecNO_ALTERNATIVE_KEY                 Result = 130
	TecNO_REGULAR_KEY                     Result = 131
	TecOWNERS                             Result = 132
	TecNO_ISSUER                          Result = 133
	TecNO_AUTH                            Result = 134
	TecNO_LINE                            Result = 135
	TecINSUFF_FEE                         Result = 136
	TecFROZEN                             Result = 137
	TecNO_TARGET                          Result = 138
	TecNO_PERMISSION                      Result = 139
	TecNO_ENTRY                           Result = 140
	TecINSUFFICIENT_RESERVE               Result = 141
	TecNEED_MASTER_KEY                    Result = 142
	TecDST_TAG_NEEDED                     Result = 143
	TecINTERNAL                           Result = 144
	TecOVERSIZE                           Result = 145
	TecCRYPTOCONDITION_ERROR              Result = 146
	TecINVARIANT_FAILED                   Result = 147
	TecEXPIRED                            Result = 148 // Offer/escrow has expired
	TecDUPLICATE                          Result = 149
	TecKILLED                             Result = 150
	TecHAS_OBLIGATIONS                    Result = 151
	TecTOO_SOON                           Result = 152
	TecHOOK_REJECTED                      Result = 153 // Reserved for hooks
	TecMAX_SEQUENCE_REACHED               Result = 154
	TecNO_SUITABLE_NFTOKEN_PAGE           Result = 155
	TecNFTOKEN_BUY_SELL_MISMATCH          Result = 156
	TecNFTOKEN_OFFER_TYPE_MISMATCH        Result = 157
	TecCANT_ACCEPT_OWN_NFTOKEN_OFFER      Result = 158
	TecINSUFFICIENT_FUNDS                 Result = 159
	TecOBJECT_NOT_FOUND                   Result = 160
	TecINSUFFICIENT_PAYMENT               Result = 161
	TecUNFUNDED_AMM                       Result = 162
	TecAMM_BALANCE                        Result = 163
	TecAMM_FAILED                         Result = 164
	TecAMM_INVALID_TOKENS                 Result = 165
	TecAMM_EMPTY                          Result = 166
	TecAMM_NOT_EMPTY                      Result = 167
	TecAMM_ACCOUNT                        Result = 168
	TecINCOMPLETE                         Result = 169
	TecXCHAIN_BAD_TRANSFER_ISSUE          Result = 170
	TecXCHAIN_NO_CLAIM_ID                 Result = 171
	TecXCHAIN_BAD_CLAIM_ID                Result = 172
	TecXCHAIN_CLAIM_NO_QUORUM             Result = 173
	TecXCHAIN_PROOF_UNKNOWN_KEY           Result = 174
	TecXCHAIN_CREATE_ACCOUNT_NONXRP_ISSUE Result = 175
	TecXCHAIN_WRONG_CHAIN                 Result = 176
	TecXCHAIN_REWARD_MISMATCH             Result = 177
	TecXCHAIN_NO_SIGNERS_LIST             Result = 178
	TecXCHAIN_SENDING_ACCOUNT_MISMATCH    Result = 179
	TecXCHAIN_INSUFF_CREATE_AMOUNT        Result = 180
	TecXCHAIN_ACCOUNT_CREATE_PAST         Result = 181
	TecXCHAIN_ACCOUNT_CREATE_TOO_MANY     Result = 182
	TecXCHAIN_PAYMENT_FAILED              Result = 183
	TecXCHAIN_SELF_COMMIT                 Result = 184
	TecXCHAIN_BAD_PUBLIC_KEY_ACCOUNT_PAIR Result = 185
	TecXCHAIN_CREATE_ACCOUNT_DISABLED     Result = 186
	TecEMPTY_DID                          Result = 187
	TecINVALID_UPDATE_TIME                Result = 188
	TecTOKEN_PAIR_NOT_FOUND               Result = 189
	TecARRAY_EMPTY                        Result = 190
	TecARRAY_TOO_LARGE                    Result = 191
	TecLOCKED                             Result = 192
	TecBAD_CREDENTIALS                    Result = 193
	TecWRONG_ASSET                        Result = 194
	TecLIMIT_EXCEEDED                     Result = 195
	TecPSEUDO_ACCOUNT                     Result = 196
	TecPRECISION_LOSS                     Result = 197
	TecNO_DELEGATE_PERMISSION             Result = 198

	// tefFAILURE and related codes (-199 to -100)
	// Transaction failed, fee claimed but tx not applied
	TefFAILURE                     Result = -199
	TefALREADY                     Result = -198
	TefBAD_ADD_AUTH                Result = -197
	TefBAD_AUTH                    Result = -196
	TefBAD_LEDGER                  Result = -195
	TefCREATED                     Result = -194
	TefEXCEPTION                   Result = -193
	TefINTERNAL                    Result = -192
	TefNO_AUTH_REQUIRED            Result = -191
	TefPAST_SEQ                    Result = -190
	TefWRONG_PRIOR                 Result = -189
	TefMASTER_DISABLED             Result = -188
	TefMAX_LEDGER                  Result = -187
	TefBAD_SIGNATURE               Result = -186
	TefBAD_QUORUM                  Result = -185
	TefNOT_MULTI_SIGNING           Result = -184
	TefBAD_AUTH_MASTER             Result = -183
	TefINVARIANT_FAILED            Result = -182
	TefTOO_BIG                     Result = -181
	TefNO_TICKET                   Result = -180
	TefNFTOKEN_IS_NOT_TRANSFERABLE Result = -179
	TefINVALID_LEDGER_FIX_TYPE     Result = -178

	// telLOCAL_ERROR and related codes (-399 to -300)
	// Local error, transaction not sent to network
	TelLOCAL_ERROR                       Result = -399
	TelBAD_DOMAIN                        Result = -398
	TelBAD_PATH_COUNT                    Result = -397
	TelBAD_PUBLIC_KEY                    Result = -396
	TelFAILED_PROCESSING                 Result = -395
	TelINSUF_FEE_P                       Result = -394
	TelNO_DST_PARTIAL                    Result = -393
	TelCAN_NOT_QUEUE                     Result = -392
	TelCAN_NOT_QUEUE_BALANCE             Result = -391
	TelCAN_NOT_QUEUE_BLOCKS              Result = -390
	TelCAN_NOT_QUEUE_BLOCKED             Result = -389
	TelCAN_NOT_QUEUE_FEE                 Result = -388
	TelCAN_NOT_QUEUE_FULL                Result = -387
	TelWRONG_NETWORK                     Result = -386
	TelREQUIRES_NETWORK_ID               Result = -385
	TelNETWORK_ID_MAKES_TX_NON_CANONICAL Result = -384

	// temMALFORMED and related codes (-299 to -200)
	// Malformed transaction
	TemMALFORMED                                   Result = -299
	TemBAD_AMOUNT                                  Result = -298
	TemBAD_CURRENCY                                Result = -297
	TemBAD_EXPIRATION                              Result = -296
	TemBAD_FEE                                     Result = -295
	TemBAD_ISSUER                                  Result = -294
	TemBAD_LIMIT                                   Result = -293
	TemBAD_OFFER                                   Result = -292
	TemBAD_PATH                                    Result = -291
	TemBAD_PATH_LOOP                               Result = -290
	TemBAD_REGKEY                                  Result = -289
	TemBAD_SEND_XRP_LIMIT                          Result = -288
	TemBAD_SEND_XRP_MAX                            Result = -287
	TemBAD_SEND_XRP_NO_DIRECT                      Result = -286
	TemBAD_SEND_XRP_PARTIAL                        Result = -285
	TemBAD_SEND_XRP_PATHS                          Result = -284
	TemBAD_SEQUENCE                                Result = -283
	TemBAD_SIGNATURE                               Result = -282
	TemBAD_SRC_ACCOUNT                             Result = -281
	TemBAD_TRANSFER_RATE                           Result = -280
	TemDST_IS_SRC                                  Result = -279
	TemDST_NEEDED                                  Result = -278
	TemINVALID                                     Result = -277
	TemINVALID_FLAG                                Result = -276
	TemREDUNDANT                                   Result = -275
	TemRIPPLE_EMPTY                                Result = -274
	TemDISABLED                                    Result = -273
	TemBAD_SIGNER                                  Result = -272
	TemBAD_QUORUM                                  Result = -271
	TemBAD_WEIGHT                                  Result = -270
	TemBAD_TICK_SIZE                               Result = -269
	TemINVALID_ACCOUNT_ID                          Result = -268
	TemCAN_NOT_PREAUTH_SELF                        Result = -267
	TemINVALID_COUNT                               Result = -266
	TemUNCERTAIN                                   Result = -265
	TemUNKNOWN                                     Result = -264
	TemSEQ_AND_TICKET                              Result = -263
	TemBAD_NFTOKEN_TRANSFER_FEE                    Result = -262
	TemBAD_AMM_TOKENS                              Result = -261
	TemXCHAIN_EQUAL_DOOR_ACCOUNTS                  Result = -260
	TemXCHAIN_BAD_PROOF                            Result = -259
	TemXCHAIN_BRIDGE_BAD_ISSUES                    Result = -258
	TemXCHAIN_BRIDGE_NONDOOR_OWNER                 Result = -257
	TemXCHAIN_BRIDGE_BAD_MIN_ACCOUNT_CREATE_AMOUNT Result = -256
	TemXCHAIN_BRIDGE_BAD_REWARD_AMOUNT             Result = -255
	TemEMPTY_DID                                   Result = -254
	TemARRAY_EMPTY                                 Result = -253
	TemARRAY_TOO_LARGE                             Result = -252
	TemBAD_TRANSFER_FEE                            Result = -251

	// terRETRY and related codes (-99 to -1)
	// Retry later
	TerRETRY             Result = -99
	TerFUNDS_SPENT       Result = -98
	TerINSUF_FEE_B       Result = -97
	TerNO_ACCOUNT        Result = -96
	TerNO_AUTH           Result = -95
	TerNO_LINE           Result = -94
	TerOWNERS            Result = -93
	TerPRE_SEQ           Result = -92
	TerLAST              Result = -91
	TerNO_RIPPLE         Result = -90
	TerQUEUED            Result = -89
	TerPRE_TICKET        Result = -88
	TerNO_AMM            Result = -87
	TerADDRESS_COLLISION Result = -86
)

// resultNames maps every Result code to its canonical rippled string.
//
// Note on temCANNOT_PREAUTH_SELF: the Go constant is TemCAN_NOT_PREAUTH_SELF
// (with the extra underscore) but the canonical rippled identifier is
// "temCANNOT_PREAUTH_SELF" — see rippled include/xrpl/protocol/TER.h:120.
// The constant name is the Go-side oddity; the string is correct.
var resultNames = map[Result]string{
	TesSUCCESS:                                     "tesSUCCESS",
	TecCLAIM:                                       "tecCLAIM",
	TecPATH_PARTIAL:                                "tecPATH_PARTIAL",
	TecUNFUNDED_ADD:                                "tecUNFUNDED_ADD",
	TecUNFUNDED_OFFER:                              "tecUNFUNDED_OFFER",
	TecUNFUNDED_PAYMENT:                            "tecUNFUNDED_PAYMENT",
	TecFAILED_PROCESSING:                           "tecFAILED_PROCESSING",
	TecDIR_FULL:                                    "tecDIR_FULL",
	TecINSUF_RESERVE_LINE:                          "tecINSUF_RESERVE_LINE",
	TecINSUF_RESERVE_OFFER:                         "tecINSUF_RESERVE_OFFER",
	TecNO_DST:                                      "tecNO_DST",
	TecNO_DST_INSUF_XRP:                            "tecNO_DST_INSUF_XRP",
	TecNO_LINE_INSUF_RESERVE:                       "tecNO_LINE_INSUF_RESERVE",
	TecNO_LINE_REDUNDANT:                           "tecNO_LINE_REDUNDANT",
	TecPATH_DRY:                                    "tecPATH_DRY",
	TecUNFUNDED:                                    "tecUNFUNDED",
	TecNO_ALTERNATIVE_KEY:                          "tecNO_ALTERNATIVE_KEY",
	TecNO_REGULAR_KEY:                              "tecNO_REGULAR_KEY",
	TecOWNERS:                                      "tecOWNERS",
	TecNO_ISSUER:                                   "tecNO_ISSUER",
	TecNO_AUTH:                                     "tecNO_AUTH",
	TecNO_LINE:                                     "tecNO_LINE",
	TecINSUFF_FEE:                                  "tecINSUFF_FEE",
	TecFROZEN:                                      "tecFROZEN",
	TecNO_TARGET:                                   "tecNO_TARGET",
	TecNO_PERMISSION:                               "tecNO_PERMISSION",
	TecNO_ENTRY:                                    "tecNO_ENTRY",
	TecINSUFFICIENT_RESERVE:                        "tecINSUFFICIENT_RESERVE",
	TecNEED_MASTER_KEY:                             "tecNEED_MASTER_KEY",
	TecDST_TAG_NEEDED:                              "tecDST_TAG_NEEDED",
	TecINTERNAL:                                    "tecINTERNAL",
	TecOVERSIZE:                                    "tecOVERSIZE",
	TecCRYPTOCONDITION_ERROR:                       "tecCRYPTOCONDITION_ERROR",
	TecINVARIANT_FAILED:                            "tecINVARIANT_FAILED",
	TecEXPIRED:                                     "tecEXPIRED",
	TecDUPLICATE:                                   "tecDUPLICATE",
	TecKILLED:                                      "tecKILLED",
	TecHAS_OBLIGATIONS:                             "tecHAS_OBLIGATIONS",
	TecTOO_SOON:                                    "tecTOO_SOON",
	TecHOOK_REJECTED:                               "tecHOOK_REJECTED",
	TecMAX_SEQUENCE_REACHED:                        "tecMAX_SEQUENCE_REACHED",
	TecNO_SUITABLE_NFTOKEN_PAGE:                    "tecNO_SUITABLE_NFTOKEN_PAGE",
	TecNFTOKEN_BUY_SELL_MISMATCH:                   "tecNFTOKEN_BUY_SELL_MISMATCH",
	TecNFTOKEN_OFFER_TYPE_MISMATCH:                 "tecNFTOKEN_OFFER_TYPE_MISMATCH",
	TecCANT_ACCEPT_OWN_NFTOKEN_OFFER:               "tecCANT_ACCEPT_OWN_NFTOKEN_OFFER",
	TecINSUFFICIENT_FUNDS:                          "tecINSUFFICIENT_FUNDS",
	TecOBJECT_NOT_FOUND:                            "tecOBJECT_NOT_FOUND",
	TecINSUFFICIENT_PAYMENT:                        "tecINSUFFICIENT_PAYMENT",
	TecUNFUNDED_AMM:                                "tecUNFUNDED_AMM",
	TecAMM_BALANCE:                                 "tecAMM_BALANCE",
	TecAMM_FAILED:                                  "tecAMM_FAILED",
	TecAMM_INVALID_TOKENS:                          "tecAMM_INVALID_TOKENS",
	TecAMM_EMPTY:                                   "tecAMM_EMPTY",
	TecAMM_NOT_EMPTY:                               "tecAMM_NOT_EMPTY",
	TecAMM_ACCOUNT:                                 "tecAMM_ACCOUNT",
	TecINCOMPLETE:                                  "tecINCOMPLETE",
	TecXCHAIN_BAD_TRANSFER_ISSUE:                   "tecXCHAIN_BAD_TRANSFER_ISSUE",
	TecXCHAIN_NO_CLAIM_ID:                          "tecXCHAIN_NO_CLAIM_ID",
	TecXCHAIN_BAD_CLAIM_ID:                         "tecXCHAIN_BAD_CLAIM_ID",
	TecXCHAIN_CLAIM_NO_QUORUM:                      "tecXCHAIN_CLAIM_NO_QUORUM",
	TecXCHAIN_PROOF_UNKNOWN_KEY:                    "tecXCHAIN_PROOF_UNKNOWN_KEY",
	TecXCHAIN_CREATE_ACCOUNT_NONXRP_ISSUE:          "tecXCHAIN_CREATE_ACCOUNT_NONXRP_ISSUE",
	TecXCHAIN_WRONG_CHAIN:                          "tecXCHAIN_WRONG_CHAIN",
	TecXCHAIN_REWARD_MISMATCH:                      "tecXCHAIN_REWARD_MISMATCH",
	TecXCHAIN_NO_SIGNERS_LIST:                      "tecXCHAIN_NO_SIGNERS_LIST",
	TecXCHAIN_SENDING_ACCOUNT_MISMATCH:             "tecXCHAIN_SENDING_ACCOUNT_MISMATCH",
	TecXCHAIN_INSUFF_CREATE_AMOUNT:                 "tecXCHAIN_INSUFF_CREATE_AMOUNT",
	TecXCHAIN_ACCOUNT_CREATE_PAST:                  "tecXCHAIN_ACCOUNT_CREATE_PAST",
	TecXCHAIN_ACCOUNT_CREATE_TOO_MANY:              "tecXCHAIN_ACCOUNT_CREATE_TOO_MANY",
	TecXCHAIN_PAYMENT_FAILED:                       "tecXCHAIN_PAYMENT_FAILED",
	TecXCHAIN_SELF_COMMIT:                          "tecXCHAIN_SELF_COMMIT",
	TecXCHAIN_BAD_PUBLIC_KEY_ACCOUNT_PAIR:          "tecXCHAIN_BAD_PUBLIC_KEY_ACCOUNT_PAIR",
	TecXCHAIN_CREATE_ACCOUNT_DISABLED:              "tecXCHAIN_CREATE_ACCOUNT_DISABLED",
	TecEMPTY_DID:                                   "tecEMPTY_DID",
	TecINVALID_UPDATE_TIME:                         "tecINVALID_UPDATE_TIME",
	TecTOKEN_PAIR_NOT_FOUND:                        "tecTOKEN_PAIR_NOT_FOUND",
	TecARRAY_EMPTY:                                 "tecARRAY_EMPTY",
	TecARRAY_TOO_LARGE:                             "tecARRAY_TOO_LARGE",
	TecLOCKED:                                      "tecLOCKED",
	TecBAD_CREDENTIALS:                             "tecBAD_CREDENTIALS",
	TecWRONG_ASSET:                                 "tecWRONG_ASSET",
	TecLIMIT_EXCEEDED:                              "tecLIMIT_EXCEEDED",
	TecPSEUDO_ACCOUNT:                              "tecPSEUDO_ACCOUNT",
	TecPRECISION_LOSS:                              "tecPRECISION_LOSS",
	TecNO_DELEGATE_PERMISSION:                      "tecNO_DELEGATE_PERMISSION",
	TefFAILURE:                                     "tefFAILURE",
	TefALREADY:                                     "tefALREADY",
	TefBAD_ADD_AUTH:                                "tefBAD_ADD_AUTH",
	TefBAD_AUTH:                                    "tefBAD_AUTH",
	TefBAD_LEDGER:                                  "tefBAD_LEDGER",
	TefCREATED:                                     "tefCREATED",
	TefEXCEPTION:                                   "tefEXCEPTION",
	TefINTERNAL:                                    "tefINTERNAL",
	TefNO_AUTH_REQUIRED:                            "tefNO_AUTH_REQUIRED",
	TefPAST_SEQ:                                    "tefPAST_SEQ",
	TefWRONG_PRIOR:                                 "tefWRONG_PRIOR",
	TefMASTER_DISABLED:                             "tefMASTER_DISABLED",
	TefMAX_LEDGER:                                  "tefMAX_LEDGER",
	TefBAD_SIGNATURE:                               "tefBAD_SIGNATURE",
	TefBAD_QUORUM:                                  "tefBAD_QUORUM",
	TefNOT_MULTI_SIGNING:                           "tefNOT_MULTI_SIGNING",
	TefBAD_AUTH_MASTER:                             "tefBAD_AUTH_MASTER",
	TefINVARIANT_FAILED:                            "tefINVARIANT_FAILED",
	TefTOO_BIG:                                     "tefTOO_BIG",
	TefNO_TICKET:                                   "tefNO_TICKET",
	TefNFTOKEN_IS_NOT_TRANSFERABLE:                 "tefNFTOKEN_IS_NOT_TRANSFERABLE",
	TefINVALID_LEDGER_FIX_TYPE:                     "tefINVALID_LEDGER_FIX_TYPE",
	TelLOCAL_ERROR:                                 "telLOCAL_ERROR",
	TelBAD_DOMAIN:                                  "telBAD_DOMAIN",
	TelBAD_PATH_COUNT:                              "telBAD_PATH_COUNT",
	TelBAD_PUBLIC_KEY:                              "telBAD_PUBLIC_KEY",
	TelFAILED_PROCESSING:                           "telFAILED_PROCESSING",
	TelINSUF_FEE_P:                                 "telINSUF_FEE_P",
	TelNO_DST_PARTIAL:                              "telNO_DST_PARTIAL",
	TelCAN_NOT_QUEUE:                               "telCAN_NOT_QUEUE",
	TelCAN_NOT_QUEUE_BALANCE:                       "telCAN_NOT_QUEUE_BALANCE",
	TelCAN_NOT_QUEUE_BLOCKS:                        "telCAN_NOT_QUEUE_BLOCKS",
	TelCAN_NOT_QUEUE_BLOCKED:                       "telCAN_NOT_QUEUE_BLOCKED",
	TelCAN_NOT_QUEUE_FEE:                           "telCAN_NOT_QUEUE_FEE",
	TelCAN_NOT_QUEUE_FULL:                          "telCAN_NOT_QUEUE_FULL",
	TelWRONG_NETWORK:                               "telWRONG_NETWORK",
	TelREQUIRES_NETWORK_ID:                         "telREQUIRES_NETWORK_ID",
	TelNETWORK_ID_MAKES_TX_NON_CANONICAL:           "telNETWORK_ID_MAKES_TX_NON_CANONICAL",
	TemMALFORMED:                                   "temMALFORMED",
	TemBAD_AMOUNT:                                  "temBAD_AMOUNT",
	TemBAD_CURRENCY:                                "temBAD_CURRENCY",
	TemBAD_EXPIRATION:                              "temBAD_EXPIRATION",
	TemBAD_FEE:                                     "temBAD_FEE",
	TemBAD_ISSUER:                                  "temBAD_ISSUER",
	TemBAD_LIMIT:                                   "temBAD_LIMIT",
	TemBAD_OFFER:                                   "temBAD_OFFER",
	TemBAD_PATH:                                    "temBAD_PATH",
	TemBAD_PATH_LOOP:                               "temBAD_PATH_LOOP",
	TemBAD_REGKEY:                                  "temBAD_REGKEY",
	TemBAD_SEND_XRP_LIMIT:                          "temBAD_SEND_XRP_LIMIT",
	TemBAD_SEND_XRP_MAX:                            "temBAD_SEND_XRP_MAX",
	TemBAD_SEND_XRP_NO_DIRECT:                      "temBAD_SEND_XRP_NO_DIRECT",
	TemBAD_SEND_XRP_PARTIAL:                        "temBAD_SEND_XRP_PARTIAL",
	TemBAD_SEND_XRP_PATHS:                          "temBAD_SEND_XRP_PATHS",
	TemBAD_SEQUENCE:                                "temBAD_SEQUENCE",
	TemBAD_SIGNATURE:                               "temBAD_SIGNATURE",
	TemBAD_SRC_ACCOUNT:                             "temBAD_SRC_ACCOUNT",
	TemBAD_TRANSFER_RATE:                           "temBAD_TRANSFER_RATE",
	TemDST_IS_SRC:                                  "temDST_IS_SRC",
	TemDST_NEEDED:                                  "temDST_NEEDED",
	TemINVALID:                                     "temINVALID",
	TemINVALID_FLAG:                                "temINVALID_FLAG",
	TemREDUNDANT:                                   "temREDUNDANT",
	TemRIPPLE_EMPTY:                                "temRIPPLE_EMPTY",
	TemDISABLED:                                    "temDISABLED",
	TemBAD_SIGNER:                                  "temBAD_SIGNER",
	TemBAD_QUORUM:                                  "temBAD_QUORUM",
	TemBAD_WEIGHT:                                  "temBAD_WEIGHT",
	TemBAD_TICK_SIZE:                               "temBAD_TICK_SIZE",
	TemINVALID_ACCOUNT_ID:                          "temINVALID_ACCOUNT_ID",
	TemCAN_NOT_PREAUTH_SELF:                        "temCANNOT_PREAUTH_SELF",
	TemINVALID_COUNT:                               "temINVALID_COUNT",
	TemUNCERTAIN:                                   "temUNCERTAIN",
	TemUNKNOWN:                                     "temUNKNOWN",
	TemSEQ_AND_TICKET:                              "temSEQ_AND_TICKET",
	TemBAD_NFTOKEN_TRANSFER_FEE:                    "temBAD_NFTOKEN_TRANSFER_FEE",
	TemBAD_AMM_TOKENS:                              "temBAD_AMM_TOKENS",
	TemXCHAIN_EQUAL_DOOR_ACCOUNTS:                  "temXCHAIN_EQUAL_DOOR_ACCOUNTS",
	TemXCHAIN_BAD_PROOF:                            "temXCHAIN_BAD_PROOF",
	TemXCHAIN_BRIDGE_BAD_ISSUES:                    "temXCHAIN_BRIDGE_BAD_ISSUES",
	TemXCHAIN_BRIDGE_NONDOOR_OWNER:                 "temXCHAIN_BRIDGE_NONDOOR_OWNER",
	TemXCHAIN_BRIDGE_BAD_MIN_ACCOUNT_CREATE_AMOUNT: "temXCHAIN_BRIDGE_BAD_MIN_ACCOUNT_CREATE_AMOUNT",
	TemXCHAIN_BRIDGE_BAD_REWARD_AMOUNT:             "temXCHAIN_BRIDGE_BAD_REWARD_AMOUNT",
	TemEMPTY_DID:                                   "temEMPTY_DID",
	TemARRAY_EMPTY:                                 "temARRAY_EMPTY",
	TemARRAY_TOO_LARGE:                             "temARRAY_TOO_LARGE",
	TemBAD_TRANSFER_FEE:                            "temBAD_TRANSFER_FEE",
	TerRETRY:                                       "terRETRY",
	TerFUNDS_SPENT:                                 "terFUNDS_SPENT",
	TerINSUF_FEE_B:                                 "terINSUF_FEE_B",
	TerNO_ACCOUNT:                                  "terNO_ACCOUNT",
	TerNO_AUTH:                                     "terNO_AUTH",
	TerNO_LINE:                                     "terNO_LINE",
	TerOWNERS:                                      "terOWNERS",
	TerPRE_SEQ:                                     "terPRE_SEQ",
	TerLAST:                                        "terLAST",
	TerNO_RIPPLE:                                   "terNO_RIPPLE",
	TerQUEUED:                                      "terQUEUED",
	TerPRE_TICKET:                                  "terPRE_TICKET",
	TerNO_AMM:                                      "terNO_AMM",
	TerADDRESS_COLLISION:                           "terADDRESS_COLLISION",
}

// String returns the canonical rippled name for this result code.
func (r Result) String() string {
	if s, ok := resultNames[r]; ok {
		return s
	}
	return fmt.Sprintf("Unknown(%d)", r)
}

// IsSuccess returns true if the result indicates success
func (r Result) IsSuccess() bool {
	return r == TesSUCCESS
}

// IsClaimed returns true if the result indicates the fee was claimed
// This includes tec codes where the transaction "succeeded" with a caveat
func (r Result) IsClaimed() bool {
	return r >= TecCLAIM && r < 200
}

// IsTec returns true if this is a tec (claimed cost) code
func (r Result) IsTec() bool {
	return r >= 100 && r < 200
}

// IsTef returns true if this is a tef (failure) code
func (r Result) IsTef() bool {
	return r >= -199 && r <= -100
}

// IsTel returns true if this is a tel (local error) code
func (r Result) IsTel() bool {
	return r >= -399 && r <= -300
}

// IsTem returns true if this is a tem (malformed) code
func (r Result) IsTem() bool {
	return r >= -299 && r <= -200
}

// IsTer returns true if this is a ter (retry) code
func (r Result) IsTer() bool {
	return r >= -99 && r <= -1
}

// ShouldRetry returns true if the transaction should be retried later
func (r Result) ShouldRetry() bool {
	return r.IsTer()
}

// IsApplied returns true if the transaction was applied to the ledger
// This is true for tesSUCCESS and all tec codes
func (r Result) IsApplied() bool {
	return r.IsSuccess() || r.IsTec()
}

// Message returns a human-readable message for the result
func (r Result) Message() string {
	switch r {
	case TesSUCCESS:
		return "The transaction was applied. Only final in a validated ledger."
	case TecCLAIM:
		return "Fee claimed. No action taken."
	case TecUNFUNDED_PAYMENT:
		return "Insufficient XRP balance to send."
	case TecNO_DST:
		return "Destination account does not exist."
	case TecNO_DST_INSUF_XRP:
		return "Destination account does not exist. Too little XRP sent to create it."
	case TecINSUFFICIENT_RESERVE:
		return "Insufficient reserve to complete requested operation."
	case TecDST_TAG_NEEDED:
		return "A destination tag is required."
	case TemBAD_AMOUNT:
		return "Can only send positive amounts."
	case TemBAD_FEE:
		return "Invalid fee, negative or not XRP."
	case TemBAD_SEQUENCE:
		return "Sequence number must be non-zero."
	case TemDST_IS_SRC:
		return "Destination may not be source."
	case TemDST_NEEDED:
		return "Destination is required."
	case TemINVALID:
		return "The transaction is ill-formed."
	case TemINVALID_FLAG:
		return "Invalid flags."
	case TemDISABLED:
		return "The transaction requires an amendment that is not enabled."
	case TerNO_ACCOUNT:
		return "The source account does not exist."
	case TerPRE_SEQ:
		return "Missing/inapplicable prior transaction."
	case TerINSUF_FEE_B:
		return "Account balance can't pay fee."
	case TefBAD_SIGNATURE:
		return "Invalid signature."
	case TefPAST_SEQ:
		return "Sequence number has already passed."
	default:
		return r.String()
	}
}
