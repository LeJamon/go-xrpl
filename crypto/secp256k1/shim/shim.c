#include "shim.h"

#include <secp256k1.h>

/* Process-lifetime, never freed. Verify ops on this context are
 * thread-safe per libsecp256k1. */
static secp256k1_context* g_ctx = NULL;

/* Requires libsecp256k1 >= 0.3.0 (Ubuntu 24.04, Debian 12+, Alpine
 * 3.18+, Homebrew). SECP256K1_CONTEXT_NONE is the unified flag; the
 * older SECP256K1_CONTEXT_VERIFY is deprecated and emits
 * -Wdeprecated-declarations on >= 0.3.0. */
void goxrpl_secp256k1_init(void) {
    if (g_ctx == NULL) {
        g_ctx = secp256k1_context_create(SECP256K1_CONTEXT_NONE);
    }
}

int goxrpl_secp256k1_verify_digest(const unsigned char* pub, size_t pub_len,
                                    const unsigned char* sig_der, size_t sig_len,
                                    const unsigned char* hash32) {
    if (g_ctx == NULL || pub == NULL || sig_der == NULL || hash32 == NULL) {
        return 0;
    }
    if (pub_len == 0 || sig_len == 0) {
        return 0;
    }

    secp256k1_pubkey pubkey;
    if (!secp256k1_ec_pubkey_parse(g_ctx, &pubkey, pub, pub_len)) {
        return 0;
    }

    secp256k1_ecdsa_signature sig;
    if (!secp256k1_ecdsa_signature_parse_der(g_ctx, &sig, sig_der, sig_len)) {
        return 0;
    }

    /* Always normalize to low-S so this path matches the pure-Go decred
     * verify, which accepts high-S (manifest verification relies on it).
     * (r,S) and (r,N-S) verify the same (msg, pubkey), so lossless. */
    secp256k1_ecdsa_signature_normalize(g_ctx, &sig, &sig);

    return secp256k1_ecdsa_verify(g_ctx, &sig, hash32, &pubkey);
}
