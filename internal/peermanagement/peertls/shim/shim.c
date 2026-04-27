/* shim.c - implementation of shim.h. */

#include "shim.h"

#include <openssl/bio.h>
#include <openssl/err.h>
#include <openssl/pem.h>
#include <openssl/ssl.h>
#include <stdlib.h>
#include <string.h>

struct peertls_ctx {
    SSL_CTX* ctx;
    int is_server;
};

struct peertls_ssl {
    SSL* ssl;
    BIO* internal_bio;  /* bound to ssl */
    BIO* network_bio;   /* the side Go pumps */
};

static int map_ssl_error(SSL* ssl, int rc) {
    int err = SSL_get_error(ssl, rc);
    switch (err) {
        case SSL_ERROR_NONE:        return 0;
        case SSL_ERROR_ZERO_RETURN: return PEERTLS_ERR_ZERO_RET;
        case SSL_ERROR_WANT_READ:   return PEERTLS_ERR_WANT_READ;
        case SSL_ERROR_WANT_WRITE:  return PEERTLS_ERR_WANT_WRITE;
        case SSL_ERROR_SYSCALL:     return PEERTLS_ERR_SYSCALL;
        case SSL_ERROR_SSL:         return PEERTLS_ERR_SSL;
        default:                    return PEERTLS_ERR_OTHER;
    }
}

peertls_ctx* peertls_ctx_new(int is_server) {
    const SSL_METHOD* m = is_server ? TLS_server_method() : TLS_client_method();
    SSL_CTX* ctx = SSL_CTX_new(m);
    if (!ctx) return NULL;

    /* Force TLS 1.2 only — matches rippled. */
    SSL_CTX_set_min_proto_version(ctx, TLS1_2_VERSION);
    SSL_CTX_set_max_proto_version(ctx, TLS1_2_VERSION);

    /* Rippled peers don't validate certs (Public-Key header is the trust
     * anchor). Match the existing InsecureSkipVerify behavior. */
    SSL_CTX_set_verify(ctx, SSL_VERIFY_NONE, NULL);

    peertls_ctx* out = calloc(1, sizeof(*out));
    if (!out) {
        SSL_CTX_free(ctx);
        return NULL;
    }
    out->ctx = ctx;
    out->is_server = is_server;
    return out;
}

void peertls_ctx_free(peertls_ctx* ctx) {
    if (!ctx) return;
    if (ctx->ctx) SSL_CTX_free(ctx->ctx);
    free(ctx);
}

int peertls_ctx_use_cert_pem(peertls_ctx* ctx,
                              const char* cert, int cert_len,
                              const char* key,  int key_len) {
    if (!ctx || !ctx->ctx) return PEERTLS_ERR_OTHER;

    BIO* cb = BIO_new_mem_buf(cert, cert_len);
    if (!cb) return PEERTLS_ERR_OTHER;
    X509* x = PEM_read_bio_X509(cb, NULL, NULL, NULL);
    BIO_free(cb);
    if (!x) return PEERTLS_ERR_SSL;

    if (SSL_CTX_use_certificate(ctx->ctx, x) != 1) {
        X509_free(x);
        return PEERTLS_ERR_SSL;
    }
    X509_free(x);

    BIO* kb = BIO_new_mem_buf(key, key_len);
    if (!kb) return PEERTLS_ERR_OTHER;
    EVP_PKEY* pk = PEM_read_bio_PrivateKey(kb, NULL, NULL, NULL);
    BIO_free(kb);
    if (!pk) return PEERTLS_ERR_SSL;

    if (SSL_CTX_use_PrivateKey(ctx->ctx, pk) != 1) {
        EVP_PKEY_free(pk);
        return PEERTLS_ERR_SSL;
    }
    EVP_PKEY_free(pk);

    if (SSL_CTX_check_private_key(ctx->ctx) != 1) {
        return PEERTLS_ERR_SSL;
    }
    return 0;
}

peertls_ssl* peertls_new(peertls_ctx* ctx) {
    if (!ctx || !ctx->ctx) return NULL;

    SSL* ssl = SSL_new(ctx->ctx);
    if (!ssl) return NULL;

    BIO* internal = NULL;
    BIO* network  = NULL;
    /* 0 size == default 17 KiB, large enough for any TLS record. */
    if (BIO_new_bio_pair(&internal, 0, &network, 0) != 1) {
        SSL_free(ssl);
        return NULL;
    }

    SSL_set_bio(ssl, internal, internal);

    if (ctx->is_server) {
        SSL_set_accept_state(ssl);
    } else {
        SSL_set_connect_state(ssl);
    }

    peertls_ssl* out = calloc(1, sizeof(*out));
    if (!out) {
        SSL_free(ssl);
        BIO_free(network);
        return NULL;
    }
    out->ssl = ssl;
    out->internal_bio = internal; /* freed by SSL_free */
    out->network_bio = network;
    return out;
}

void peertls_free(peertls_ssl* s) {
    if (!s) return;
    if (s->ssl) SSL_free(s->ssl);
    if (s->network_bio) BIO_free(s->network_bio);
    free(s);
}

int peertls_handshake(peertls_ssl* s) {
    if (!s || !s->ssl) return PEERTLS_ERR_OTHER;
    int rc = SSL_do_handshake(s->ssl);
    if (rc == 1) return 0;
    return map_ssl_error(s->ssl, rc);
}

int peertls_read(peertls_ssl* s, void* buf, int len) {
    if (!s || !s->ssl) return PEERTLS_ERR_OTHER;
    int rc = SSL_read(s->ssl, buf, len);
    if (rc > 0) return rc;
    return map_ssl_error(s->ssl, rc);
}

int peertls_write(peertls_ssl* s, const void* buf, int len) {
    if (!s || !s->ssl) return PEERTLS_ERR_OTHER;
    int rc = SSL_write(s->ssl, buf, len);
    if (rc > 0) return rc;
    return map_ssl_error(s->ssl, rc);
}

int peertls_bio_read(peertls_ssl* s, void* buf, int len) {
    if (!s || !s->network_bio) return PEERTLS_ERR_OTHER;
    int pending = BIO_ctrl_pending(s->network_bio);
    if (pending == 0) return 0;
    int rc = BIO_read(s->network_bio, buf, len);
    if (rc <= 0) return 0;
    return rc;
}

int peertls_bio_write(peertls_ssl* s, const void* buf, int len) {
    if (!s || !s->network_bio) return PEERTLS_ERR_OTHER;
    int rc = BIO_write(s->network_bio, buf, len);
    if (rc <= 0) return 0;
    return rc;
}

int peertls_get_finished(peertls_ssl* s, void* buf, int len) {
    if (!s || !s->ssl) return 0;
    return (int)SSL_get_finished(s->ssl, buf, (size_t)len);
}

int peertls_get_peer_finished(peertls_ssl* s, void* buf, int len) {
    if (!s || !s->ssl) return 0;
    return (int)SSL_get_peer_finished(s->ssl, buf, (size_t)len);
}

int peertls_shutdown(peertls_ssl* s) {
    if (!s || !s->ssl) return PEERTLS_ERR_OTHER;
    int rc = SSL_shutdown(s->ssl);
    if (rc >= 0) return 0;
    return map_ssl_error(s->ssl, rc);
}

const char* peertls_last_error(void) {
    static __thread char buf[256];
    unsigned long e = ERR_peek_last_error();
    if (e == 0) return "";
    ERR_error_string_n(e, buf, sizeof(buf));
    return buf;
}
