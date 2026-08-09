#ifndef GOXRPL_MPTCRYPTO_SHIM_H
#define GOXRPL_MPTCRYPTO_SHIM_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

int go_mpt_available(void);
void go_mpt_test_force_unavailable(int unavailable);
int go_mpt_valid_pubkey(const uint8_t pubkey[33]);
int go_mpt_valid_ciphertext(const uint8_t ciphertext[66]);
int go_mpt_add(const uint8_t a[66], const uint8_t b[66], uint8_t out[66]);
int go_mpt_subtract(const uint8_t a[66], const uint8_t b[66], uint8_t out[66]);
int go_mpt_canonical_zero(const uint8_t pubkey[33], const uint8_t account[20], const uint8_t issuance[24], uint8_t out[66]);
int go_mpt_convert_context(const uint8_t account[20], const uint8_t issuance[24], uint32_t sequence, uint8_t out[32]);
int go_mpt_convert_back_context(const uint8_t account[20], const uint8_t issuance[24], uint32_t sequence, uint32_t version, uint8_t out[32]);
int go_mpt_send_context(const uint8_t account[20], const uint8_t issuance[24], uint32_t sequence, const uint8_t destination[20], uint32_t version, uint8_t out[32]);
int go_mpt_clawback_context(const uint8_t account[20], const uint8_t issuance[24], uint32_t sequence, const uint8_t holder[20], uint8_t out[32]);
int go_mpt_verify_revealed(uint64_t amount, const uint8_t blind[32], const uint8_t holder_pub[33], const uint8_t holder_ct[66], const uint8_t issuer_pub[33], const uint8_t issuer_ct[66], int has_auditor, const uint8_t auditor_pub[33], const uint8_t auditor_ct[66]);
int go_mpt_verify_convert(const uint8_t proof[64], const uint8_t pubkey[33], const uint8_t context[32]);
int go_mpt_verify_convert_back(const uint8_t proof[816], const uint8_t pubkey[33], const uint8_t spending[66], const uint8_t commitment[33], uint64_t amount, const uint8_t context[32]);
int go_mpt_verify_send(const uint8_t proof[946], const uint8_t pubs[132], const uint8_t ciphertexts[264], uint8_t participant_count, const uint8_t spending[66], const uint8_t amount_commitment[33], const uint8_t balance_commitment[33], const uint8_t context[32]);
int go_mpt_verify_clawback(const uint8_t proof[64], uint64_t amount, const uint8_t pubkey[33], const uint8_t ciphertext[66], const uint8_t context[32]);
int go_mpt_rerandomize(const uint8_t ciphertext[66], const uint8_t pubkey[33], const uint8_t randomness[32], uint8_t out[66]);
int go_mpt_generate_keypair(uint8_t private_key[32], uint8_t public_key[33]);
int go_mpt_generate_blinding(uint8_t blind[32]);
int go_mpt_encrypt(uint64_t amount, const uint8_t public_key[33], const uint8_t blind[32], uint8_t ciphertext[66]);
int go_mpt_generate_convert_proof(const uint8_t public_key[33], const uint8_t private_key[32], const uint8_t context[32], uint8_t proof[64]);
int go_mpt_commitment(uint64_t amount, const uint8_t blind[32], uint8_t commitment[33]);
int go_mpt_generate_convert_back_proof(const uint8_t private_key[32], const uint8_t public_key[33], const uint8_t context[32], uint64_t amount, const uint8_t balance_commitment[33], uint64_t balance, const uint8_t spending[66], const uint8_t balance_blind[32], uint8_t proof[816]);
int go_mpt_generate_send_proof(const uint8_t private_key[32], uint64_t amount, const uint8_t pubs[132], const uint8_t ciphertexts[264], uint8_t participant_count, const uint8_t transaction_blind[32], const uint8_t context[32], const uint8_t amount_commitment[33], const uint8_t balance_commitment[33], uint64_t balance, const uint8_t spending[66], const uint8_t balance_blind[32], uint8_t proof[946]);
int go_mpt_generate_clawback_proof(const uint8_t private_key[32], const uint8_t public_key[33], const uint8_t context[32], uint64_t amount, const uint8_t ciphertext[66], uint8_t proof[64]);

#ifdef __cplusplus
}
#endif

#endif
