//go:build mptcrypto && cgo

#include "shim.h"

#include <cstring>
#include <mpt_protocol.h>
#include <secp256k1_mpt.h>
#include <utility/mpt_utility.h>

static_assert(kMPT_ACCOUNT_ID_SIZE == 20, "account size");
static_assert(kMPT_ISSUANCE_ID_SIZE == 24, "issuance size");
static_assert(kMPT_PUBKEY_SIZE == 33, "public key size");
static_assert(kMPT_BLINDING_FACTOR_SIZE == 32, "blinding factor size");
static_assert(kMPT_ELGAMAL_TOTAL_SIZE == 66, "ciphertext size");
static_assert(kMPT_PEDERSEN_COMMIT_SIZE == 33, "commitment size");
static_assert(kMPT_SCHNORR_PROOF_SIZE == 64, "convert proof size");
static_assert(SECP256K1_COMPACT_CONVERTBACK_PROOF_SIZE + kMPT_SINGLE_BULLETPROOF_SIZE == 816, "convert back proof size");

extern "C" int go_mpt_available(void) { return mpt_secp256k1_context() != nullptr; }

extern "C" int go_mpt_valid_pubkey(const uint8_t pubkey[33]) {
    secp256k1_pubkey parsed;
    return secp256k1_ec_pubkey_parse(mpt_secp256k1_context(), &parsed, pubkey, 33) == 1;
}

extern "C" int go_mpt_valid_ciphertext(const uint8_t ciphertext[66]) {
    secp256k1_pubkey c1, c2;
    return mpt_make_ec_pair(ciphertext, &c1, &c2) ? 1 : 0;
}

static int combine(const uint8_t a[66], const uint8_t b[66], uint8_t out[66], bool subtract) {
    secp256k1_pubkey a1, a2, b1, b2, r1, r2;
    if (!mpt_make_ec_pair(a, &a1, &a2) || !mpt_make_ec_pair(b, &b1, &b2)) return 0;
    int ok = subtract
        ? secp256k1_elgamal_subtract(mpt_secp256k1_context(), &r1, &r2, &a1, &a2, &b1, &b2)
        : secp256k1_elgamal_add(mpt_secp256k1_context(), &r1, &r2, &a1, &a2, &b1, &b2);
    return ok == 1 && mpt_serialize_ec_pair(&r1, &r2, out) ? 1 : 0;
}

extern "C" int go_mpt_add(const uint8_t a[66], const uint8_t b[66], uint8_t out[66]) { return combine(a, b, out, false); }
extern "C" int go_mpt_subtract(const uint8_t a[66], const uint8_t b[66], uint8_t out[66]) { return combine(a, b, out, true); }

extern "C" int go_mpt_canonical_zero(const uint8_t pubkey[33], const uint8_t account[20], const uint8_t issuance[24], uint8_t out[66]) {
    secp256k1_pubkey key, c1, c2;
    if (secp256k1_ec_pubkey_parse(mpt_secp256k1_context(), &key, pubkey, 33) != 1) return 0;
    if (generate_canonical_encrypted_zero(mpt_secp256k1_context(), &c1, &c2, &key, account, issuance) != 1) return 0;
    return mpt_serialize_ec_pair(&c1, &c2, out) ? 1 : 0;
}

static mpt_issuance_id issuance_id(const uint8_t in[24]) {
    mpt_issuance_id value;
    std::memcpy(value.bytes, in, 24);
    return value;
}

static account_id account(const uint8_t in[20]) {
    account_id value;
    std::memcpy(value.bytes, in, 20);
    return value;
}

extern "C" int go_mpt_convert_context(const uint8_t acc[20], const uint8_t issuance[24], uint32_t sequence, uint8_t out[32]) {
    return mpt_get_convert_context_hash(account(acc), issuance_id(issuance), sequence, out) == 0;
}

extern "C" int go_mpt_convert_back_context(const uint8_t acc[20], const uint8_t issuance[24], uint32_t sequence, uint32_t version, uint8_t out[32]) {
    return mpt_get_convert_back_context_hash(account(acc), issuance_id(issuance), sequence, version, out) == 0;
}

static mpt_confidential_participant participant(const uint8_t pub[33], const uint8_t ct[66]) {
    mpt_confidential_participant value;
    std::memcpy(value.pubkey, pub, 33);
    std::memcpy(value.ciphertext, ct, 66);
    return value;
}

extern "C" int go_mpt_verify_revealed(uint64_t amount, const uint8_t blind[32], const uint8_t holder_pub[33], const uint8_t holder_ct[66], const uint8_t issuer_pub[33], const uint8_t issuer_ct[66], int has_auditor, const uint8_t auditor_pub[33], const uint8_t auditor_ct[66]) {
    auto holder = participant(holder_pub, holder_ct);
    auto issuer = participant(issuer_pub, issuer_ct);
    auto auditor = participant(auditor_pub, auditor_ct);
    return mpt_verify_revealed_amount(amount, blind, &holder, &issuer, has_auditor ? &auditor : nullptr) == 0;
}

extern "C" int go_mpt_verify_convert(const uint8_t proof[64], const uint8_t pubkey[33], const uint8_t context[32]) {
    return mpt_verify_convert_proof(proof, pubkey, context) == 0;
}

extern "C" int go_mpt_verify_convert_back(const uint8_t proof[816], const uint8_t pubkey[33], const uint8_t spending[66], const uint8_t commitment[33], uint64_t amount, const uint8_t context[32]) {
    return mpt_verify_convert_back_proof(proof, pubkey, spending, commitment, amount, context) == 0;
}

extern "C" int go_mpt_generate_keypair(uint8_t private_key[32], uint8_t public_key[33]) {
    return mpt_generate_keypair(private_key, public_key) == 0;
}

extern "C" int go_mpt_generate_blinding(uint8_t blind[32]) {
    return mpt_generate_blinding_factor(blind) == 0;
}

extern "C" int go_mpt_encrypt(uint64_t amount, const uint8_t public_key[33], const uint8_t blind[32], uint8_t ciphertext[66]) {
    return mpt_encrypt_amount(amount, public_key, blind, ciphertext) == 0;
}

extern "C" int go_mpt_generate_convert_proof(const uint8_t public_key[33], const uint8_t private_key[32], const uint8_t context[32], uint8_t proof[64]) {
    return mpt_get_convert_proof(public_key, private_key, context, proof) == 0;
}

extern "C" int go_mpt_commitment(uint64_t amount, const uint8_t blind[32], uint8_t commitment[33]) {
    return mpt_get_pedersen_commitment(amount, blind, commitment) == 0;
}

extern "C" int go_mpt_generate_convert_back_proof(const uint8_t private_key[32], const uint8_t public_key[33], const uint8_t context[32], uint64_t amount, const uint8_t balance_commitment[33], uint64_t balance, const uint8_t spending[66], const uint8_t balance_blind[32], uint8_t proof[816]) {
    mpt_pedersen_proof_params params;
    std::memcpy(params.pedersen_commitment, balance_commitment, 33);
    params.amount = balance;
    std::memcpy(params.ciphertext, spending, 66);
    std::memcpy(params.blinding_factor, balance_blind, 32);
    return mpt_get_convert_back_proof(private_key, public_key, context, amount, &params, proof) == 0;
}
