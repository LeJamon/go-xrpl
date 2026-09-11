#include "shim.h"

#include <secp256k1.h>
#include <string.h>

/* The context is initialized once and intentionally retained for the process
 * lifetime. libsecp256k1 permits concurrent use after the one-time
 * randomization below. */
static secp256k1_context* g_ctx = NULL;
static int g_ctx_ready = 0;

static void
secure_erase(void* ptr, size_t len)
{
    volatile unsigned char* bytes = (volatile unsigned char*)ptr;
    while (len-- != 0)
        *bytes++ = 0;
}

int
goxrpl_secp256k1_init(const unsigned char* seed32)
{
    secp256k1_context* context;

    if (g_ctx_ready == 1)
        return 1;
    if (seed32 == NULL)
        return 0;

    context = secp256k1_context_create(SECP256K1_CONTEXT_NONE);
    if (context == NULL)
        return 0;
    if (secp256k1_context_randomize(context, seed32) != 1)
    {
        secp256k1_context_destroy(context);
        return 0;
    }
    g_ctx = context;
    g_ctx_ready = 1;
    return 1;
}

int
goxrpl_secp256k1_secret_key_valid(const unsigned char* secret,
                                  size_t secret_len)
{
    if (g_ctx_ready != 1 || secret == NULL || secret_len != 32)
        return 0;
    return secp256k1_ec_seckey_verify(g_ctx, secret);
}

int
goxrpl_secp256k1_public_key_create(const unsigned char* secret,
                                   size_t secret_len,
                                   unsigned char* output,
                                   size_t output_len)
{
    secp256k1_pubkey pubkey = {{0}};
    size_t serialized_len = 33;
    int result = 0;

    if (g_ctx_ready != 1 || secret == NULL || secret_len != 32 ||
        output == NULL || output_len < 33)
        return 0;

    if (secp256k1_ec_pubkey_create(g_ctx, &pubkey, secret) != 1)
        goto cleanup;
    result = secp256k1_ec_pubkey_serialize(
        g_ctx, output, &serialized_len, &pubkey, SECP256K1_EC_COMPRESSED);
    if (result != 1 || serialized_len != 33)
    {
        result = 0;
    }

cleanup:
    secure_erase(&pubkey, sizeof(pubkey));
    return result;
}

int
goxrpl_secp256k1_public_key_parse(const unsigned char* pub,
                                  size_t pub_len,
                                  unsigned char* output,
                                  size_t* output_len,
                                  int compressed)
{
    secp256k1_pubkey pubkey = {{0}};
    size_t expected_len;
    size_t capacity = output_len != NULL ? *output_len : 0;
    int result = 0;

    if (compressed != 0 && compressed != 1)
        return 0;
    expected_len = compressed ? 33 : 65;
    if (g_ctx_ready != 1 || pub == NULL ||
        (pub_len != 33 && pub_len != 65) || output == NULL || output_len == NULL ||
        capacity < expected_len)
        return 0;
    *output_len = 0;

    if (secp256k1_ec_pubkey_parse(g_ctx, &pubkey, pub, pub_len) != 1)
        goto cleanup;
    *output_len = expected_len;
    result = secp256k1_ec_pubkey_serialize(
        g_ctx, output, output_len, &pubkey,
        compressed ? SECP256K1_EC_COMPRESSED : SECP256K1_EC_UNCOMPRESSED);
    if (result != 1 || *output_len != expected_len)
    {
        *output_len = 0;
        result = 0;
    }

cleanup:
    secure_erase(&pubkey, sizeof(pubkey));
    return result;
}

int
goxrpl_secp256k1_sign_digest(const unsigned char* hash32,
                             const unsigned char* secret,
                             size_t secret_len,
                             unsigned char* output,
                             size_t* output_len)
{
    secp256k1_ecdsa_signature signature = {{0}};
    size_t serialized_len;
    int result = 0;

    if (output_len != NULL)
    {
        serialized_len = *output_len;
        *output_len = 0;
    }
    else
    {
        serialized_len = 0;
    }
    if (g_ctx_ready != 1 || hash32 == NULL || secret == NULL ||
        secret_len != 32 || output == NULL || output_len == NULL ||
        serialized_len < 72)
        goto cleanup;
    if (secp256k1_ec_seckey_verify(g_ctx, secret) != 1)
        goto cleanup;
    if (secp256k1_ecdsa_sign(
            g_ctx, &signature, hash32, secret,
            secp256k1_nonce_function_rfc6979, NULL) != 1)
        goto cleanup;

    *output_len = serialized_len;
    result = secp256k1_ecdsa_signature_serialize_der(
        g_ctx, output, output_len, &signature);
    if (result != 1 || *output_len == 0 || *output_len > 72)
    {
        *output_len = 0;
        result = 0;
    }

cleanup:
    secure_erase(&signature, sizeof(signature));
    return result;
}

int
goxrpl_secp256k1_secret_key_tweak_add(const unsigned char* secret,
                                      size_t secret_len,
                                      const unsigned char* tweak,
                                      size_t tweak_len,
                                      unsigned char* output,
                                      size_t output_len)
{
    unsigned char tweaked[32];
    int result;

    if (output != NULL && output_len >= 32)
        secure_erase(output, 32);
    if (g_ctx_ready != 1 || secret == NULL || secret_len != 32 ||
        tweak == NULL || tweak_len != 32 || output == NULL || output_len < 32)
        return 0;

    memcpy(tweaked, secret, sizeof(tweaked));
    result = secp256k1_ec_seckey_tweak_add(g_ctx, tweaked, tweak);
    if (result == 1)
        memcpy(output, tweaked, sizeof(tweaked));
    secure_erase(tweaked, sizeof(tweaked));
    return result;
}

int
goxrpl_secp256k1_public_key_tweak_add(const unsigned char* pub,
                                      size_t pub_len,
                                      const unsigned char* tweak,
                                      size_t tweak_len,
                                      unsigned char* output,
                                      size_t output_len)
{
    secp256k1_pubkey pubkey = {{0}};
    size_t serialized_len = 33;
    int result = 0;

    if (g_ctx_ready != 1 || pub == NULL ||
        (pub_len != 33 && pub_len != 65) || tweak == NULL || tweak_len != 32 ||
        output == NULL || output_len < 33)
        goto cleanup;
    if (secp256k1_ec_pubkey_parse(g_ctx, &pubkey, pub, pub_len) != 1)
        goto cleanup;
    result = secp256k1_ec_pubkey_tweak_add(g_ctx, &pubkey, tweak);
    if (result != 1)
        goto cleanup;

    result = secp256k1_ec_pubkey_serialize(
        g_ctx, output, &serialized_len, &pubkey, SECP256K1_EC_COMPRESSED);
    if (result != 1 || serialized_len != 33)
    {
        result = 0;
    }

cleanup:
    secure_erase(&pubkey, sizeof(pubkey));
    return result;
}

int
goxrpl_secp256k1_verify_digest(const unsigned char* pub,
                               size_t pub_len,
                               const unsigned char* sig_der,
                               size_t sig_len,
                               const unsigned char* hash32)
{
    secp256k1_pubkey pubkey = {{0}};
    secp256k1_ecdsa_signature signature = {{0}};
    int result = 0;

    if (g_ctx_ready != 1 || pub == NULL || sig_der == NULL ||
        hash32 == NULL || pub_len != 33 ||
        (pub[0] != 0x02 && pub[0] != 0x03) || sig_len == 0)
        goto cleanup;
    if (secp256k1_ec_pubkey_parse(g_ctx, &pubkey, pub, pub_len) != 1)
        goto cleanup;
    if (secp256k1_ecdsa_signature_parse_der(
            g_ctx, &signature, sig_der, sig_len) != 1)
        goto cleanup;

    /* Relaxed verification accepts either ECDSA S representative. The Go
     * caller performs the strict low-S check where required. */
    secp256k1_ecdsa_signature_normalize(g_ctx, &signature, &signature);
    result = secp256k1_ecdsa_verify(g_ctx, &signature, hash32, &pubkey);

cleanup:
    secure_erase(&signature, sizeof(signature));
    secure_erase(&pubkey, sizeof(pubkey));
    return result;
}
