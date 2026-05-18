#ifndef GOXRPL_SECP256K1_SHIM_H
#define GOXRPL_SECP256K1_SHIM_H

#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Idempotent; safe to call repeatedly. */
void goxrpl_secp256k1_init(void);

/* Returns 1 if the DER signature verifies against (hash32, pub),
 * 0 otherwise. The signature is normalized to low-S before verify, so
 * both low-S and high-S sigs can pass. pub is 33-byte compressed or
 * 65-byte uncompressed SEC1. */
int goxrpl_secp256k1_verify_digest(const unsigned char* pub, size_t pub_len,
                                    const unsigned char* sig_der, size_t sig_len,
                                    const unsigned char* hash32);

#ifdef __cplusplus
}
#endif

#endif
