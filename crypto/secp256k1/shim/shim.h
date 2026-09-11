#ifndef GOXRPL_SECP256K1_SHIM_H
#define GOXRPL_SECP256K1_SHIM_H

#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Called by the Go package initializer before concurrent use. Repeated calls
 * after successful initialization return 1. seed32 is used only during this
 * call to randomize the process-lifetime context. */
int goxrpl_secp256k1_init(const unsigned char* seed32);

/* Returns 1 when secret is a 32-byte scalar in [1, curve_order - 1]. */
int goxrpl_secp256k1_secret_key_valid(const unsigned char* secret,
                                      size_t secret_len);

/* Creates and serializes a compressed public key from a 32-byte secret. */
int goxrpl_secp256k1_public_key_create(const unsigned char* secret,
                                       size_t secret_len,
                                       unsigned char* output,
                                       size_t output_len);

/* Parses a compressed, uncompressed, or hybrid public key and serializes it
 * in the requested format. output_len is an in/out capacity and length. */
int goxrpl_secp256k1_public_key_parse(const unsigned char* pub, size_t pub_len,
                                      unsigned char* output, size_t* output_len,
                                      int compressed);

/* Signs a 32-byte digest with a 32-byte secret using RFC6979 and serializes
 * the low-S signature as DER. output_len is an in/out capacity and length. */
int goxrpl_secp256k1_sign_digest(const unsigned char* hash32,
                                 const unsigned char* secret,
                                 size_t secret_len,
                                 unsigned char* output,
                                 size_t* output_len);

/* Return a newly computed secret-key tweak-add result without mutating the
 * input. Tweak add accepts a valid scalar or all-zero bytes. */
int goxrpl_secp256k1_secret_key_tweak_add(const unsigned char* secret,
                                          size_t secret_len,
                                          const unsigned char* tweak,
                                          size_t tweak_len,
                                          unsigned char* output,
                                          size_t output_len);

/* Return a compressed public-key tweak result without mutating the input.
 * The input may be compressed, uncompressed, or hybrid. */
int goxrpl_secp256k1_public_key_tweak_add(const unsigned char* pub,
                                          size_t pub_len,
                                          const unsigned char* tweak,
                                          size_t tweak_len,
                                          unsigned char* output,
                                          size_t output_len);

/* Returns 1 if the DER signature verifies against (hash32, pub),
 * 0 otherwise. The signature is normalized to low-S before verify, so
 * both low-S and high-S sigs can pass. pub must be an XRPL-canonical
 * 33-byte compressed secp256k1 key. */
int goxrpl_secp256k1_verify_digest(const unsigned char* pub, size_t pub_len,
                                   const unsigned char* sig_der, size_t sig_len,
                                   const unsigned char* hash32);

#ifdef __cplusplus
}
#endif

#endif
