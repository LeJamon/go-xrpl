package tx

import "fmt"

// Result represents a transaction result code
type Result int

// Transaction result codes matching rippled exactly
// These are organized by category: tes, tec, tef, tel, tem, ter
const (
	// tesSUCCESS and related (0-99)
	TesSUCCESS Result = 0

	// tecCLAIM and other "claimed cost" codes (100-199)
	// Transaction succeeded but with a caveat
	TecCLAIM                     Result = 100
	TecPATH_PARTIAL              Result = 101
	TecUNFUNDED_ADD              Result = 102
	TecUNFUNDED_OFFER            Result = 103
	TecUNFUNDED_PAYMENT          Result = 104
	TecFAILED_PROCESSING         Result = 105
	TecDIR_FULL                  Result = 121
	TecINSUF_RESERVE_LINE        Result = 122
	TecINSUF_RESERVE_OFFER       Result = 123
	TecNO_DST                    Result = 124
	TecNO_DST_INSUF_XRP          Result = 125
	TecNO_LINE_INSUF_RESERVE     Result = 126
	TecNO_LINE_REDUNDANT         Result = 127
	TecPATH_DRY                  Result = 128
	TecUNFUNDED                  Result = 129
	TecNO_ALTERNATIVE_KEY        Result = 130
	TecNO_REGULAR_KEY            Result = 131
	TecOWNERS                    Result = 132
	TecNO_ISSUER                 Result = 133
	TecNO_AUTH                   Result = 134
	TecNO_LINE                   Result = 135
	TecINSUFF_FEE                Result = 136
	TecFROZEN                    Result = 137
	TecNO_TARGET                 Result = 138
	TecNO_PERMISSION             Result = 139
	TecNO_ENTRY                  Result = 140
	TecINSUFFICIENT_RESERVE      Result = 141
	TecNEED_MASTER_KEY           Result = 142
	TecDST_TAG_NEEDED            Result = 143
	TecINTERNAL                  Result = 144
	TecOVERSIZE                  Result = 145
	TecCRYPTOCONDITION_ERROR         Result = 146
	TecINVARIANT_FAILED              Result = 147
	TecEXPIRED                       Result = 148 // Offer/escrow has expired
	TecDUPLICATE                     Result = 149
	TecKILLED                        Result = 150
	TecHAS_OBLIGATIONS               Result = 151
	TecTOO_SOON                      Result = 152
	TecHOOK_REJECTED                 Result = 153 // Reserved for hooks
	TecMAX_SEQUENCE_REACHED          Result = 154
	TecNO_SUITABLE_NFTOKEN_PAGE      Result = 155
	TecNFTOKEN_BUY_SELL_MISMATCH     Result = 156
	TecNFTOKEN_OFFER_TYPE_MISMATCH   Result = 157
	TecCANT_ACCEPT_OWN_NFTOKEN_OFFER Result = 158
	TecINSUFFICIENT_FUNDS            Result = 159
	TecOBJECT_NOT_FOUND              Result = 160
	TecINSUFFICIENT_PAYMENT          Result = 161
	TecUNFUNDED_AMM                  Result = 162
	TecAMM_BALANCE                   Result = 163
	TecAMM_FAILED                    Result = 164
	TecAMM_INVALID_TOKENS            Result = 165
	TecAMM_EMPTY                     Result = 166
	TecAMM_NOT_EMPTY                 Result = 167
	TecAMM_ACCOUNT                   Result = 168
	TecINCOMPLETE                    Result = 169
	TecXCHAIN_BAD_TRANSFER_ISSUE     Result = 170
	TecXCHAIN_NO_CLAIM_ID            Result = 171
	TecXCHAIN_BAD_CLAIM_ID           Result = 172
	TecXCHAIN_CLAIM_NO_QUORUM        Result = 173
	TecXCHAIN_PROOF_UNKNOWN_KEY      Result = 174
	TecXCHAIN_CREATE_ACCOUNT_NONXRP_ISSUE Result = 175
	TecXCHAIN_WRONG_CHAIN            Result = 176
	TecXCHAIN_REWARD_MISMATCH        Result = 177
	TecXCHAIN_NO_SIGNERS_LIST        Result = 178
	TecXCHAIN_SENDING_ACCOUNT_MISMATCH Result = 179
	TecXCHAIN_INSUFF_CREATE_AMOUNT   Result = 180
	TecXCHAIN_ACCOUNT_CREATE_PAST    Result = 181
	TecXCHAIN_ACCOUNT_CREATE_TOO_MANY Result = 182
	TecXCHAIN_PAYMENT_FAILED         Result = 183
	TecXCHAIN_SELF_COMMIT            Result = 184
	TecXCHAIN_BAD_PUBLIC_KEY_ACCOUNT_PAIR Result = 185
	TecXCHAIN_CREATE_ACCOUNT_DISABLED Result = 186
	TecEMPTY_DID                     Result = 187
	TecINVALID_UPDATE_TIME           Result = 188
	TecTOKEN_PAIR_NOT_FOUND          Result = 189
	TecARRAY_EMPTY                   Result = 190
	TecARRAY_TOO_LARGE               Result = 191
	TecLOCKED                        Result = 192
	TecBAD_CREDENTIALS               Result = 193
	TecWRONG_ASSET                   Result = 194
	TecPSEUDO_ACCOUNT                Result = 196

	// tefFAILURE and related codes (-199 to -100)
	// Transaction failed, fee claimed but tx not applied
	TefFAILURE          Result = -199
	TefALREADY          Result = -198
	TefBAD_ADD_AUTH     Result = -197
	TefBAD_AUTH         Result = -196
	TefBAD_LEDGER       Result = -195
	TefCREATED          Result = -194
	TefEXCEPTION        Result = -193
	TefINTERNAL         Result = -192
	TefNO_AUTH_REQUIRED Result = -191
	TefPAST_SEQ         Result = -190
	TefWRONG_PRIOR      Result = -189
	TefMASTER_DISABLED  Result = -188
	TefMAX_LEDGER       Result = -187
	TefBAD_SIGNATURE    Result = -186
	TefBAD_QUORUM       Result = -185
	TefNOT_MULTI_SIGNING Result = -184
	TefBAD_AUTH_MASTER  Result = -183
	TefINVARIANT_FAILED Result = -182
	TefTOO_BIG          Result = -181
	TefNO_TICKET        Result = -180
	TefNFTOKEN_IS_NOT_TRANSFERABLE Result = -179
	TefINVALID_LEDGER_FIX_TYPE     Result = -178

	// telLOCAL_ERROR and related codes (-399 to -300)
	// Local error, transaction not sent to network
	TelLOCAL_ERROR     Result = -399
	TelBAD_DOMAIN      Result = -398
	TelBAD_PATH_COUNT  Result = -397
	TelBAD_PUBLIC_KEY  Result = -396
	TelFAILED_PROCESSING Result = -395
	TelINSUF_FEE_P     Result = -394
	TelNO_DST_PARTIAL  Result = -393
	TelCAN_NOT_QUEUE   Result = -392
	TelCAN_NOT_QUEUE_BALANCE Result = -391
	TelCAN_NOT_QUEUE_BLOCKS Result = -390
	TelCAN_NOT_QUEUE_BLOCKED Result = -389
	TelCAN_NOT_QUEUE_FEE Result = -388
	TelCAN_NOT_QUEUE_FULL Result = -387
	TelWRONG_NETWORK   Result = -386
	TelREQUIRES_NETWORK_ID Result = -385
	TelNETWORK_ID_MAKES_TX_NON_CANONICAL Result = -384

	// temMALFORMED and related codes (-299 to -200)
	// Malformed transaction
	TemMALFORMED           Result = -299
	TemBAD_AMOUNT          Result = -298
	TemBAD_CURRENCY        Result = -297
	TemBAD_EXPIRATION      Result = -296
	TemBAD_FEE             Result = -295
	TemBAD_ISSUER          Result = -294
	TemBAD_LIMIT           Result = -293
	TemBAD_OFFER           Result = -292
	TemBAD_PATH            Result = -291
	TemBAD_PATH_LOOP       Result = -290
	TemBAD_REGKEY          Result = -289
	TemBAD_SEND_XRP_LIMIT  Result = -288
	TemBAD_SEND_XRP_MAX    Result = -287
	TemBAD_SEND_XRP_NO_DIRECT Result = -286
	TemBAD_SEND_XRP_PARTIAL Result = -285
	TemBAD_SEND_XRP_PATHS  Result = -284
	TemBAD_SEQUENCE        Result = -283
	TemBAD_SIGNATURE       Result = -282
	TemBAD_SRC_ACCOUNT     Result = -281
	TemBAD_TRANSFER_RATE   Result = -280
	TemDST_IS_SRC          Result = -279
	TemDST_NEEDED          Result = -278
	TemINVALID             Result = -277
	TemINVALID_FLAG        Result = -276
	TemREDUNDANT           Result = -275
	TemRIPPLE_EMPTY        Result = -274
	TemDISABLED            Result = -273
	TemBAD_SIGNER          Result = -272
	TemBAD_QUORUM          Result = -271
	TemBAD_WEIGHT          Result = -270
	TemBAD_TICK_SIZE       Result = -269
	TemINVALID_ACCOUNT_ID  Result = -268
	TemCAN_NOT_PREAUTH_SELF Result = -267
	TemINVALID_COUNT       Result = -266
	TemUNCERTAIN           Result = -265
	TemUNKNOWN             Result = -264
	TemSEQ_AND_TICKET      Result = -263
	TemBAD_NFTOKEN_TRANSFER_FEE Result = -262
	TemBAD_AMM_TOKENS      Result = -261
	TemXCHAIN_EQUAL_DOOR_ACCOUNTS Result = -260
	TemXCHAIN_BAD_PROOF    Result = -259
	TemXCHAIN_BRIDGE_BAD_ISSUES Result = -258
	TemXCHAIN_BRIDGE_NONDOOR_OWNER Result = -257
	TemXCHAIN_BRIDGE_BAD_MIN_ACCOUNT_CREATE_AMOUNT Result = -256
	TemXCHAIN_BRIDGE_BAD_REWARD_AMOUNT Result = -255
	TemEMPTY_DID           Result = -254
	TemARRAY_EMPTY         Result = -253
	TemARRAY_TOO_LARGE     Result = -252
	TemBAD_TRANSFER_FEE    Result = -251

	// terRETRY and related codes (-99 to -1)
	// Retry later
	TerRETRY        Result = -99
	TerFUNDS_SPENT  Result = -98
	TerINSUF_FEE_B  Result = -97
	TerNO_ACCOUNT   Result = -96
	TerNO_AUTH      Result = -95
	TerNO_LINE      Result = -94
	TerOWNERS       Result = -93
	TerPRE_SEQ      Result = -92
	TerLAST         Result = -91
	TerNO_RIPPLE    Result = -90
	TerQUEUED       Result = -89
	TerPRE_TICKET   Result = -88
	TerNO_AMM       Result = -87
	TerADDRESS_COLLISION    Result = -86
)

// String returns the string representation of the result code
func (r Result) String() string {
	switch r {
	case TesSUCCESS:
		return "tesSUCCESS"
	case TecCLAIM:
		return "tecCLAIM"
	case TecPATH_PARTIAL:
		return "tecPATH_PARTIAL"
	case TecUNFUNDED_ADD:
		return "tecUNFUNDED_ADD"
	case TecUNFUNDED_OFFER:
		return "tecUNFUNDED_OFFER"
	case TecUNFUNDED_PAYMENT:
		return "tecUNFUNDED_PAYMENT"
	case TecFAILED_PROCESSING:
		return "tecFAILED_PROCESSING"
	case TecDIR_FULL:
		return "tecDIR_FULL"
	case TecINSUF_RESERVE_LINE:
		return "tecINSUF_RESERVE_LINE"
	case TecINSUF_RESERVE_OFFER:
		return "tecINSUF_RESERVE_OFFER"
	case TecNO_DST:
		return "tecNO_DST"
	case TecNO_DST_INSUF_XRP:
		return "tecNO_DST_INSUF_XRP"
	case TecNO_LINE_INSUF_RESERVE:
		return "tecNO_LINE_INSUF_RESERVE"
	case TecNO_LINE_REDUNDANT:
		return "tecNO_LINE_REDUNDANT"
	case TecPATH_DRY:
		return "tecPATH_DRY"
	case TecUNFUNDED:
		return "tecUNFUNDED"
	case TecNO_ALTERNATIVE_KEY:
		return "tecNO_ALTERNATIVE_KEY"
	case TecNO_REGULAR_KEY:
		return "tecNO_REGULAR_KEY"
	case TecOWNERS:
		return "tecOWNERS"
	case TecNO_ISSUER:
		return "tecNO_ISSUER"
	case TecNO_AUTH:
		return "tecNO_AUTH"
	case TecNO_LINE:
		return "tecNO_LINE"
	case TecINSUFF_FEE:
		return "tecINSUFF_FEE"
	case TecFROZEN:
		return "tecFROZEN"
	case TecNO_TARGET:
		return "tecNO_TARGET"
	case TecNO_PERMISSION:
		return "tecNO_PERMISSION"
	case TecNO_ENTRY:
		return "tecNO_ENTRY"
	case TecINSUFFICIENT_RESERVE:
		return "tecINSUFFICIENT_RESERVE"
	case TecNEED_MASTER_KEY:
		return "tecNEED_MASTER_KEY"
	case TecDST_TAG_NEEDED:
		return "tecDST_TAG_NEEDED"
	case TecINTERNAL:
		return "tecINTERNAL"
	case TecOVERSIZE:
		return "tecOVERSIZE"
	case TecCRYPTOCONDITION_ERROR:
		return "tecCRYPTOCONDITION_ERROR"
	case TecINVARIANT_FAILED:
		return "tecINVARIANT_FAILED"
	case TecEXPIRED:
		return "tecEXPIRED"
	case TecKILLED:
		return "tecKILLED"
	case TecHAS_OBLIGATIONS:
		return "tecHAS_OBLIGATIONS"
	case TecTOO_SOON:
		return "tecTOO_SOON"
	case TecMAX_SEQUENCE_REACHED:
		return "tecMAX_SEQUENCE_REACHED"
	case TecNO_SUITABLE_NFTOKEN_PAGE:
		return "tecNO_SUITABLE_NFTOKEN_PAGE"
	case TecNFTOKEN_BUY_SELL_MISMATCH:
		return "tecNFTOKEN_BUY_SELL_MISMATCH"
	case TecNFTOKEN_OFFER_TYPE_MISMATCH:
		return "tecNFTOKEN_OFFER_TYPE_MISMATCH"
	case TecCANT_ACCEPT_OWN_NFTOKEN_OFFER:
		return "tecCANT_ACCEPT_OWN_NFTOKEN_OFFER"
	case TecINSUFFICIENT_FUNDS:
		return "tecINSUFFICIENT_FUNDS"
	case TecOBJECT_NOT_FOUND:
		return "tecOBJECT_NOT_FOUND"
	case TecINSUFFICIENT_PAYMENT:
		return "tecINSUFFICIENT_PAYMENT"
	case TecAMM_EMPTY:
		return "tecAMM_EMPTY"
	case TecAMM_ACCOUNT:
		return "tecAMM_ACCOUNT"
	case TecINCOMPLETE:
		return "tecINCOMPLETE"
	case TecEMPTY_DID:
		return "tecEMPTY_DID"
	case TecINVALID_UPDATE_TIME:
		return "tecINVALID_UPDATE_TIME"
	case TecTOKEN_PAIR_NOT_FOUND:
		return "tecTOKEN_PAIR_NOT_FOUND"
	case TecARRAY_EMPTY:
		return "tecARRAY_EMPTY"
	case TecARRAY_TOO_LARGE:
		return "tecARRAY_TOO_LARGE"
	case TecLOCKED:
		return "tecLOCKED"
	case TecBAD_CREDENTIALS:
		return "tecBAD_CREDENTIALS"
	case TecWRONG_ASSET:
		return "tecWRONG_ASSET"
	case TecPSEUDO_ACCOUNT:
		return "tecPSEUDO_ACCOUNT"
	case TecDUPLICATE:
		return "tecDUPLICATE"
	case TecUNFUNDED_AMM:
		return "tecUNFUNDED_AMM"
	case TecAMM_BALANCE:
		return "tecAMM_BALANCE"
	case TecAMM_FAILED:
		return "tecAMM_FAILED"
	case TecAMM_INVALID_TOKENS:
		return "tecAMM_INVALID_TOKENS"
	case TecAMM_NOT_EMPTY:
		return "tecAMM_NOT_EMPTY"
	case TemEMPTY_DID:
		return "temEMPTY_DID"
	case TemARRAY_EMPTY:
		return "temARRAY_EMPTY"
	case TemARRAY_TOO_LARGE:
		return "temARRAY_TOO_LARGE"
	case TemBAD_TRANSFER_FEE:
		return "temBAD_TRANSFER_FEE"
	case TefFAILURE:
		return "tefFAILURE"
	case TefALREADY:
		return "tefALREADY"
	case TefBAD_AUTH:
		return "tefBAD_AUTH"
	case TefBAD_LEDGER:
		return "tefBAD_LEDGER"
	case TefEXCEPTION:
		return "tefEXCEPTION"
	case TefINTERNAL:
		return "tefINTERNAL"
	case TefPAST_SEQ:
		return "tefPAST_SEQ"
	case TefMASTER_DISABLED:
		return "tefMASTER_DISABLED"
	case TefMAX_LEDGER:
		return "tefMAX_LEDGER"
	case TefBAD_SIGNATURE:
		return "tefBAD_SIGNATURE"
	case TefBAD_QUORUM:
		return "tefBAD_QUORUM"
	case TefNOT_MULTI_SIGNING:
		return "tefNOT_MULTI_SIGNING"
	case TefBAD_AUTH_MASTER:
		return "tefBAD_AUTH_MASTER"
	case TefTOO_BIG:
		return "tefTOO_BIG"
	case TefNO_TICKET:
		return "tefNO_TICKET"
	case TefNFTOKEN_IS_NOT_TRANSFERABLE:
		return "tefNFTOKEN_IS_NOT_TRANSFERABLE"
	case TefINVALID_LEDGER_FIX_TYPE:
		return "tefINVALID_LEDGER_FIX_TYPE"
	case TelLOCAL_ERROR:
		return "telLOCAL_ERROR"
	case TelBAD_DOMAIN:
		return "telBAD_DOMAIN"
	case TelBAD_PUBLIC_KEY:
		return "telBAD_PUBLIC_KEY"
	case TelINSUF_FEE_P:
		return "telINSUF_FEE_P"
	case TelCAN_NOT_QUEUE:
		return "telCAN_NOT_QUEUE"
	case TelWRONG_NETWORK:
		return "telWRONG_NETWORK"
	case TelREQUIRES_NETWORK_ID:
		return "telREQUIRES_NETWORK_ID"
	case TelNETWORK_ID_MAKES_TX_NON_CANONICAL:
		return "telNETWORK_ID_MAKES_TX_NON_CANONICAL"
	case TemMALFORMED:
		return "temMALFORMED"
	case TemBAD_AMOUNT:
		return "temBAD_AMOUNT"
	case TemBAD_CURRENCY:
		return "temBAD_CURRENCY"
	case TemBAD_FEE:
		return "temBAD_FEE"
	case TemBAD_ISSUER:
		return "temBAD_ISSUER"
	case TemBAD_LIMIT:
		return "temBAD_LIMIT"
	case TemBAD_SEQUENCE:
		return "temBAD_SEQUENCE"
	case TemBAD_SIGNATURE:
		return "temBAD_SIGNATURE"
	case TemBAD_SRC_ACCOUNT:
		return "temBAD_SRC_ACCOUNT"
	case TemDST_IS_SRC:
		return "temDST_IS_SRC"
	case TemDST_NEEDED:
		return "temDST_NEEDED"
	case TemINVALID:
		return "temINVALID"
	case TemINVALID_FLAG:
		return "temINVALID_FLAG"
	case TemREDUNDANT:
		return "temREDUNDANT"
	case TemDISABLED:
		return "temDISABLED"
	case TemUNCERTAIN:
		return "temUNCERTAIN"
	case TemUNKNOWN:
		return "temUNKNOWN"
	case TemBAD_PATH:
		return "temBAD_PATH"
	case TemBAD_PATH_LOOP:
		return "temBAD_PATH_LOOP"
	case TemBAD_OFFER:
		return "temBAD_OFFER"
	case TemBAD_SEND_XRP_LIMIT:
		return "temBAD_SEND_XRP_LIMIT"
	case TemBAD_SEND_XRP_MAX:
		return "temBAD_SEND_XRP_MAX"
	case TemBAD_SEND_XRP_NO_DIRECT:
		return "temBAD_SEND_XRP_NO_DIRECT"
	case TemBAD_SEND_XRP_PARTIAL:
		return "temBAD_SEND_XRP_PARTIAL"
	case TemBAD_SEND_XRP_PATHS:
		return "temBAD_SEND_XRP_PATHS"
	case TemRIPPLE_EMPTY:
		return "temRIPPLE_EMPTY"
	case TemBAD_TRANSFER_RATE:
		return "temBAD_TRANSFER_RATE"
	case TemBAD_EXPIRATION:
		return "temBAD_EXPIRATION"
	case TemBAD_AMM_TOKENS:
		return "temBAD_AMM_TOKENS"
	case TemBAD_SIGNER:
		return "temBAD_SIGNER"
	case TemBAD_QUORUM:
		return "temBAD_QUORUM"
	case TemBAD_WEIGHT:
		return "temBAD_WEIGHT"
	case TemBAD_TICK_SIZE:
		return "temBAD_TICK_SIZE"
	case TemINVALID_ACCOUNT_ID:
		return "temINVALID_ACCOUNT_ID"
	case TemCAN_NOT_PREAUTH_SELF:
		return "temCAN_NOT_PREAUTH_SELF"
	case TemINVALID_COUNT:
		return "temINVALID_COUNT"
	case TemSEQ_AND_TICKET:
		return "temSEQ_AND_TICKET"
	case TemBAD_NFTOKEN_TRANSFER_FEE:
		return "temBAD_NFTOKEN_TRANSFER_FEE"
	case TemBAD_REGKEY:
		return "temBAD_REGKEY"
	case TerRETRY:
		return "terRETRY"
	case TerFUNDS_SPENT:
		return "terFUNDS_SPENT"
	case TerINSUF_FEE_B:
		return "terINSUF_FEE_B"
	case TerNO_ACCOUNT:
		return "terNO_ACCOUNT"
	case TerNO_AUTH:
		return "terNO_AUTH"
	case TerNO_LINE:
		return "terNO_LINE"
	case TerOWNERS:
		return "terOWNERS"
	case TerPRE_SEQ:
		return "terPRE_SEQ"
	case TerLAST:
		return "terLAST"
	case TerNO_RIPPLE:
		return "terNO_RIPPLE"
	case TerQUEUED:
		return "terQUEUED"
	case TerPRE_TICKET:
		return "terPRE_TICKET"
	case TerNO_AMM:
		return "terNO_AMM"
	case TerADDRESS_COLLISION:
		return "terADDRESS_COLLISION"
	default:
		return fmt.Sprintf("Unknown(%d)", r)
	}
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
