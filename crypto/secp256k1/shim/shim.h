/* shim.h - libsecp256k1 verify shim.
 *
 * Verify-only path. The shim owns a single process-wide
 * secp256k1_context (verify flavor), created lazily and never freed.
 * libsecp256k1 documents the verify context as thread-safe, so no
 * locking is required around verify_digest.
 */

#ifndef GOXRPL_SECP256K1_SHIM_H
#define GOXRPL_SECP256K1_SHIM_H

#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Lazily initialize the verify context. Safe to call repeatedly. */
void goxrpl_secp256k1_init(void);

/* Verify a DER-encoded ECDSA signature against a 32-byte message hash
 * and a 33-byte compressed (or 65-byte uncompressed) SEC1 public key.
 *
 * Returns:
 *    1 - signature is valid
 *    0 - signature is invalid, malformed, non-low-S, or pubkey is bad
 *
 * libsecp256k1's secp256k1_ecdsa_verify enforces low-S internally, so
 * any signature with high-S is rejected even if it would otherwise be
 * arithmetically valid.
 */
int goxrpl_secp256k1_verify_digest(const unsigned char* pub, size_t pub_len,
                                    const unsigned char* sig_der, size_t sig_len,
                                    const unsigned char* hash32);

#ifdef __cplusplus
}
#endif

#endif
