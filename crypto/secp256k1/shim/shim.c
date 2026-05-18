#include "shim.h"

#include <secp256k1.h>

/* Process-wide verify context. libsecp256k1 documents the verify
 * context as thread-safe; we create it once and never free it. The
 * lifetime is the process. */
static secp256k1_context* g_ctx = NULL;

/* SECP256K1_CONTEXT_VERIFY is the historically correct flag and is
 * still accepted by every released version of libsecp256k1. Newer
 * versions (>=0.3.0) unified the context flags and emit a deprecation
 * notice, but the runtime behavior is unchanged. Using it preserves
 * compatibility with older system installs. */
void goxrpl_secp256k1_init(void) {
    if (g_ctx == NULL) {
        g_ctx = secp256k1_context_create(SECP256K1_CONTEXT_VERIFY);
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

    /* Normalize S to low-S before verifying. The pure-Go decred verify
     * does not enforce low-S, so callers that pass mustBeFullyCanonical=
     * false (e.g. manifest verification) currently accept high-S
     * signatures. Normalizing here preserves that behavior — the C and
     * Go paths must agree on accept/reject. Mathematically (r,S) and
     * (r,N-S) verify the same (msg, pubkey), so normalizing is lossless. */
    secp256k1_ecdsa_signature_normalize(g_ctx, &sig, &sig);

    return secp256k1_ecdsa_verify(g_ctx, &sig, hash32, &pubkey);
}
