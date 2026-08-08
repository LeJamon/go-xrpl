#include "shim.h"

#include <limits.h>
#include <openssl/bio.h>
#include <openssl/dh.h>
#include <openssl/err.h>
#include <openssl/pem.h>
#include <openssl/sha.h>
#include <openssl/ssl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#ifndef SSL_OP_NO_RENEGOTIATION
#error "OpenSSL with SSL_OP_NO_RENEGOTIATION is required"
#endif

struct peertls_ctx {
    SSL_CTX* ctx;
    int is_server;
};

struct peertls_ssl {
    SSL* ssl;
    BIO* network_bio;
    unsigned char* pending_write;
    size_t pending_write_len;
    unsigned char pending_write_digest[SHA256_DIGEST_LENGTH];
    size_t pending_write_input_len;
    size_t pending_write_result;
    int pending_write_complete;
    int write_failed;
};

static const char default_dh_parameters[] =
    "-----BEGIN DH PARAMETERS-----\n"
    "MIIBCAKCAQEApKSWfR7LKy0VoZ/SDCObCvJ5HKX2J93RJ+QN8kJwHh+uuA8G+t8Q\n"
    "MDRjL5HanlV/sKN9HXqBc7eqHmmbqYwIXKUt9MUZTLNheguddxVlc2IjdP5i9Ps8\n"
    "l7su8tnP0l1JvC6Rfv3epRsEAw/ZW/lC2IwkQPpOmvnENQhQ6TgrUzcGkv4Bn0X6\n"
    "pxrDSBpZ+45oehGCUAtcbY8b02vu8zPFoxqo6V/+MIszGzldlik5bVqrJpVF6E8C\n"
    "tRqHjj6KuDbPbjc+pRGvwx/BSO3SULxmYu9J1NOk090MU1CMt6IJY7TpEc9Xrac9\n"
    "9yqY3xXZID240RRcaJ25+U4lszFPqP+CEwIBAg==\n"
    "-----END DH PARAMETERS-----";

static void begin_operation(char* error, size_t error_len) {
    if (error != NULL && error_len > 0) error[0] = '\0';
    ERR_clear_error();
}

static void set_error(char* error, size_t error_len, const char* message) {
    if (error == NULL || error_len == 0) return;
    if (message == NULL) message = "OpenSSL operation failed";
    (void)snprintf(error, error_len, "%s", message);
}

static void capture_error(char* error, size_t error_len, const char* fallback) {
    unsigned long current;
    unsigned long last = 0;
    while ((current = ERR_get_error()) != 0) last = current;
    if (last != 0 && error != NULL && error_len > 0) {
        ERR_error_string_n(last, error, error_len);
        return;
    }
    set_error(error, error_len, fallback);
}

static int checked_int_length(size_t len, int* out,
                              char* error, size_t error_len) {
    if (len > INT_MAX) {
        set_error(error, error_len, "buffer length exceeds OpenSSL int range");
        return 0;
    }
    *out = (int)len;
    return 1;
}

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

static int ssl_failure(SSL* ssl, int rc, char* error, size_t error_len) {
    int code = map_ssl_error(ssl, rc);
    capture_error(error, error_len, "OpenSSL TLS operation failed");
    return code;
}

static unsigned long required_options(void) {
    return SSL_OP_ALL |
           SSL_OP_NO_SSLv2 |
           SSL_OP_NO_SSLv3 |
           SSL_OP_NO_TLSv1 |
           SSL_OP_NO_TLSv1_1 |
           SSL_OP_SINGLE_DH_USE |
           SSL_OP_NO_COMPRESSION |
           SSL_OP_NO_RENEGOTIATION;
}

static int configure_dh_parameters(SSL_CTX* ctx,
                                   char* error, size_t error_len) {
    BIO* bio = BIO_new_mem_buf(default_dh_parameters,
                               (int)(sizeof(default_dh_parameters) - 1));
    if (bio == NULL) {
        capture_error(error, error_len, "failed to create DH parameter BIO");
        return 0;
    }
#if OPENSSL_VERSION_NUMBER >= 0x30000000L
    EVP_PKEY* parameters = PEM_read_bio_Parameters(bio, NULL);
    if (parameters == NULL) {
        capture_error(error, error_len, "failed to parse DH parameters");
        BIO_free(bio);
        return 0;
    }
    BIO_free(bio);
    if (SSL_CTX_set0_tmp_dh_pkey(ctx, parameters) != 1) {
        capture_error(error, error_len, "failed to install DH parameters");
        EVP_PKEY_free(parameters);
        return 0;
    }
#else
    DH* parameters = PEM_read_bio_DHparams(bio, NULL, NULL, NULL);
    if (parameters == NULL) {
        capture_error(error, error_len, "failed to parse DH parameters");
        BIO_free(bio);
        return 0;
    }
    BIO_free(bio);
    int installed = SSL_CTX_set_tmp_dh(ctx, parameters);
    DH_free(parameters);
    if (installed != 1) {
        capture_error(error, error_len, "failed to install DH parameters");
        return 0;
    }
#endif
    return 1;
}

peertls_ctx* peertls_ctx_new(int is_server, const char* cipher_list,
                             char* error, size_t error_len) {
    begin_operation(error, error_len);
    if (cipher_list == NULL) cipher_list = PEERTLS_DEFAULT_CIPHER_LIST;
    if (cipher_list[0] == '\0') {
        set_error(error, error_len, "cipher list must not be empty");
        return NULL;
    }

    const SSL_METHOD* method = is_server ? TLS_server_method() : TLS_client_method();
    SSL_CTX* ctx = SSL_CTX_new(method);
    if (ctx == NULL) {
        capture_error(error, error_len, "SSL_CTX_new failed");
        return NULL;
    }
    if (SSL_CTX_set_min_proto_version(ctx, TLS1_2_VERSION) != 1) {
        capture_error(error, error_len, "failed to require TLS 1.2 or newer");
        SSL_CTX_free(ctx);
        return NULL;
    }
    if (SSL_CTX_set_cipher_list(ctx, cipher_list) != 1) {
        capture_error(error, error_len, "cipher list selects no supported ciphers");
        SSL_CTX_free(ctx);
        return NULL;
    }

    unsigned long options = required_options();
    unsigned long applied = SSL_CTX_set_options(ctx, options);
    if ((applied & options) != options) {
        set_error(error, error_len, "failed to apply required TLS options");
        SSL_CTX_free(ctx);
        return NULL;
    }

    SSL_CTX_set_verify(ctx, SSL_VERIFY_NONE, NULL);
    if (!configure_dh_parameters(ctx, error, error_len)) {
        SSL_CTX_free(ctx);
        return NULL;
    }

    peertls_ctx* out = calloc(1, sizeof(*out));
    if (out == NULL) {
        set_error(error, error_len, "failed to allocate TLS context wrapper");
        SSL_CTX_free(ctx);
        return NULL;
    }
    out->ctx = ctx;
    out->is_server = is_server;
    return out;
}

void peertls_ctx_free(peertls_ctx* ctx) {
    if (ctx == NULL) return;
    SSL_CTX_free(ctx->ctx);
    free(ctx);
}

int peertls_ctx_use_cert_pem(peertls_ctx* ctx,
                             const char* cert, size_t cert_len,
                             const char* key, size_t key_len,
                             char* error, size_t error_len) {
    begin_operation(error, error_len);
    if (ctx == NULL || ctx->ctx == NULL || cert == NULL || key == NULL) {
        set_error(error, error_len, "invalid TLS context or PEM buffer");
        return PEERTLS_ERR_OTHER;
    }
    int cert_int;
    int key_int;
    if (!checked_int_length(cert_len, &cert_int, error, error_len) ||
        !checked_int_length(key_len, &key_int, error, error_len)) {
        return PEERTLS_ERR_OTHER;
    }

    BIO* cert_bio = BIO_new_mem_buf(cert, cert_int);
    if (cert_bio == NULL) {
        capture_error(error, error_len, "failed to create certificate BIO");
        return PEERTLS_ERR_SSL;
    }
    X509* certificate = PEM_read_bio_X509(cert_bio, NULL, NULL, NULL);
    if (certificate == NULL) {
        capture_error(error, error_len, "failed to parse certificate PEM");
        BIO_free(cert_bio);
        return PEERTLS_ERR_SSL;
    }
    BIO_free(cert_bio);

    if (SSL_CTX_use_certificate(ctx->ctx, certificate) != 1) {
        capture_error(error, error_len, "failed to install certificate");
        X509_free(certificate);
        return PEERTLS_ERR_SSL;
    }
    X509_free(certificate);

    BIO* key_bio = BIO_new_mem_buf(key, key_int);
    if (key_bio == NULL) {
        capture_error(error, error_len, "failed to create private-key BIO");
        return PEERTLS_ERR_SSL;
    }
    EVP_PKEY* private_key = PEM_read_bio_PrivateKey(key_bio, NULL, NULL, NULL);
    if (private_key == NULL) {
        capture_error(error, error_len, "failed to parse private-key PEM");
        BIO_free(key_bio);
        return PEERTLS_ERR_SSL;
    }
    BIO_free(key_bio);

    if (SSL_CTX_use_PrivateKey(ctx->ctx, private_key) != 1) {
        capture_error(error, error_len, "failed to install private key");
        EVP_PKEY_free(private_key);
        return PEERTLS_ERR_SSL;
    }
    EVP_PKEY_free(private_key);

    if (SSL_CTX_check_private_key(ctx->ctx) != 1) {
        capture_error(error, error_len, "certificate and private key do not match");
        return PEERTLS_ERR_SSL;
    }
    return 0;
}

peertls_ssl* peertls_new(peertls_ctx* ctx, char* error, size_t error_len) {
    begin_operation(error, error_len);
    if (ctx == NULL || ctx->ctx == NULL) {
        set_error(error, error_len, "invalid TLS context");
        return NULL;
    }

    SSL* ssl = SSL_new(ctx->ctx);
    if (ssl == NULL) {
        capture_error(error, error_len, "SSL_new failed");
        return NULL;
    }

    BIO* internal = NULL;
    BIO* network = NULL;
    if (BIO_new_bio_pair(&internal, 0, &network, 0) != 1) {
        capture_error(error, error_len, "BIO_new_bio_pair failed");
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
    if (out == NULL) {
        set_error(error, error_len, "failed to allocate TLS connection wrapper");
        SSL_free(ssl);
        BIO_free(network);
        return NULL;
    }
    out->ssl = ssl;
    out->network_bio = network;
    return out;
}

void peertls_free(peertls_ssl* s) {
    if (s == NULL) return;
    SSL_free(s->ssl);
    free(s->pending_write);
    BIO_free(s->network_bio);
    free(s);
}

static void release_pending_write_payload(peertls_ssl* s) {
    free(s->pending_write);
    s->pending_write = NULL;
    s->pending_write_len = 0;
}

static void clear_pending_write(peertls_ssl* s) {
    release_pending_write_payload(s);
    memset(s->pending_write_digest, 0, sizeof(s->pending_write_digest));
    s->pending_write_input_len = 0;
    s->pending_write_result = 0;
    s->pending_write_complete = 0;
}

static int has_pending_write(const peertls_ssl* s) {
    return s->pending_write != NULL || s->pending_write_complete;
}

static int abort_pending_write(peertls_ssl* s, const char* message,
                               char* error, size_t error_len) {
    SSL_free(s->ssl);
    s->ssl = NULL;
    clear_pending_write(s);
    s->write_failed = 1;
    set_error(error, error_len, message);
    return PEERTLS_ERR_WRITE_RETRY;
}

int peertls_handshake(peertls_ssl* s, char* error, size_t error_len) {
    begin_operation(error, error_len);
    if (s == NULL || s->ssl == NULL || s->write_failed) {
        set_error(error, error_len, "invalid TLS connection");
        return PEERTLS_ERR_OTHER;
    }
    if (has_pending_write(s)) {
        return abort_pending_write(s, "handshake interrupted a pending SSL_write",
                                   error, error_len);
    }
    int rc = SSL_do_handshake(s->ssl);
    if (rc == 1) return 0;
    return ssl_failure(s->ssl, rc, error, error_len);
}

int peertls_shutdown(peertls_ssl* s, char* error, size_t error_len) {
    begin_operation(error, error_len);
    if (s == NULL || s->ssl == NULL || s->write_failed) {
        set_error(error, error_len, "invalid TLS connection");
        return PEERTLS_ERR_OTHER;
    }
    if (has_pending_write(s)) {
        return abort_pending_write(s, "shutdown interrupted a pending SSL_write",
                                   error, error_len);
    }
    int rc = SSL_shutdown(s->ssl);
    if (rc == 1) return 0;
    if (rc == 0) return PEERTLS_ERR_WANT_READ;
    return ssl_failure(s->ssl, rc, error, error_len);
}

int peertls_read(peertls_ssl* s, void* buf, size_t len,
                 char* error, size_t error_len) {
    begin_operation(error, error_len);
    if (s == NULL || s->ssl == NULL || s->write_failed ||
        (buf == NULL && len != 0)) {
        set_error(error, error_len, "invalid TLS connection or read buffer");
        return PEERTLS_ERR_OTHER;
    }
    if (s->pending_write != NULL) {
        int write_len = (int)s->pending_write_len;
        int write_rc = SSL_write(s->ssl, s->pending_write, write_len);
        if (write_rc > 0) {
            s->pending_write_complete = 1;
            s->pending_write_result = (size_t)write_rc;
            release_pending_write_payload(s);
        } else {
            int code = ssl_failure(s->ssl, write_rc, error, error_len);
            if (code != PEERTLS_ERR_WANT_READ &&
                code != PEERTLS_ERR_WANT_WRITE) {
                clear_pending_write(s);
                s->write_failed = 1;
            }
            return code;
        }
    }
    int int_len;
    if (!checked_int_length(len, &int_len, error, error_len)) {
        return PEERTLS_ERR_OTHER;
    }
    int rc = SSL_read(s->ssl, buf, int_len);
    if (rc > 0) return rc;
    return ssl_failure(s->ssl, rc, error, error_len);
}

int peertls_write(peertls_ssl* s, const void* buf, size_t len,
                  char* error, size_t error_len) {
    begin_operation(error, error_len);
    if (s == NULL || s->ssl == NULL || s->write_failed ||
        (buf == NULL && len != 0)) {
        set_error(error, error_len, "invalid or failed TLS connection");
        return PEERTLS_ERR_OTHER;
    }
    int int_len;
    if (!checked_int_length(len, &int_len, error, error_len)) {
        return PEERTLS_ERR_OTHER;
    }

    if (s->pending_write_complete) {
        unsigned char digest[SHA256_DIGEST_LENGTH];
        if (len != s->pending_write_input_len ||
            SHA256(buf, len, digest) == NULL ||
            memcmp(s->pending_write_digest, digest, sizeof(digest)) != 0) {
            return abort_pending_write(s, "SSL_write retry data or length changed",
                                       error, error_len);
        }
        int result = (int)s->pending_write_result;
        clear_pending_write(s);
        return result;
    }

    if (len == 0) return 0;

    if (s->pending_write == NULL) {
        s->pending_write = malloc(len);
        if (s->pending_write == NULL) {
            set_error(error, error_len, "failed to allocate pending write buffer");
            return PEERTLS_ERR_OTHER;
        }
        memcpy(s->pending_write, buf, len);
        s->pending_write_len = len;
        s->pending_write_input_len = len;
        if (SHA256(buf, len, s->pending_write_digest) == NULL) {
            clear_pending_write(s);
            set_error(error, error_len, "failed to fingerprint pending write buffer");
            return PEERTLS_ERR_OTHER;
        }
    } else if (len != s->pending_write_len ||
               memcmp(s->pending_write, buf, len) != 0) {
        return abort_pending_write(s, "SSL_write retry data or length changed",
                                   error, error_len);
    }

    int rc = SSL_write(s->ssl, s->pending_write, int_len);
    if (rc > 0) {
        clear_pending_write(s);
        return rc;
    }
    int code = ssl_failure(s->ssl, rc, error, error_len);
    if (code != PEERTLS_ERR_WANT_READ && code != PEERTLS_ERR_WANT_WRITE) {
        clear_pending_write(s);
        s->write_failed = 1;
    }
    return code;
}

int peertls_bio_read(peertls_ssl* s, void* buf, size_t len,
                     char* error, size_t error_len) {
    begin_operation(error, error_len);
    if (s == NULL || s->ssl == NULL || s->write_failed ||
        s->network_bio == NULL || (buf == NULL && len != 0)) {
        set_error(error, error_len, "invalid TLS connection or BIO read buffer");
        return PEERTLS_ERR_OTHER;
    }
    int int_len;
    if (!checked_int_length(len, &int_len, error, error_len)) {
        return PEERTLS_ERR_OTHER;
    }
    if (BIO_ctrl_pending(s->network_bio) == 0) return 0;
    int rc = BIO_read(s->network_bio, buf, int_len);
    if (rc > 0) return rc;
    if (BIO_should_retry(s->network_bio)) return 0;
    capture_error(error, error_len, "BIO_read failed");
    return PEERTLS_ERR_SSL;
}

int peertls_bio_write(peertls_ssl* s, const void* buf, size_t len,
                      char* error, size_t error_len) {
    begin_operation(error, error_len);
    if (s == NULL || s->ssl == NULL || s->write_failed ||
        s->network_bio == NULL || (buf == NULL && len != 0)) {
        set_error(error, error_len, "invalid TLS connection or BIO write buffer");
        return PEERTLS_ERR_OTHER;
    }
    int int_len;
    if (!checked_int_length(len, &int_len, error, error_len)) {
        return PEERTLS_ERR_OTHER;
    }
    int rc = BIO_write(s->network_bio, buf, int_len);
    if (rc > 0) return rc;
    if (BIO_should_retry(s->network_bio)) return 0;
    capture_error(error, error_len, "BIO_write failed");
    return PEERTLS_ERR_SSL;
}

size_t peertls_get_finished(peertls_ssl* s, void* buf, size_t len) {
    if (s == NULL || s->ssl == NULL || s->write_failed ||
        has_pending_write(s) ||
        (buf == NULL && len != 0)) return 0;
    return SSL_get_finished(s->ssl, buf, len);
}

size_t peertls_get_peer_finished(peertls_ssl* s, void* buf, size_t len) {
    if (s == NULL || s->ssl == NULL || s->write_failed ||
        has_pending_write(s) ||
        (buf == NULL && len != 0)) return 0;
    return SSL_get_peer_finished(s->ssl, buf, len);
}

int peertls_ctx_protocol_bounds(peertls_ctx* ctx,
                                int* min_version, int* max_version) {
    if (ctx == NULL || ctx->ctx == NULL ||
        min_version == NULL || max_version == NULL) return 0;
    long min = SSL_CTX_get_min_proto_version(ctx->ctx);
    long max = SSL_CTX_get_max_proto_version(ctx->ctx);
    if (min < INT_MIN || min > INT_MAX || max < INT_MIN || max > INT_MAX) {
        return 0;
    }
    *min_version = (int)min;
    *max_version = (int)max;
    return 1;
}

unsigned long peertls_ctx_options(peertls_ctx* ctx) {
    if (ctx == NULL || ctx->ctx == NULL) return 0;
    return SSL_CTX_get_options(ctx->ctx);
}

unsigned long peertls_required_options(void) {
    return required_options();
}

int peertls_ctx_set_protocol_bounds_for_test(
        peertls_ctx* ctx, int min_version, int max_version,
        char* error, size_t error_len) {
    begin_operation(error, error_len);
    if (ctx == NULL || ctx->ctx == NULL) {
        set_error(error, error_len, "invalid TLS context");
        return PEERTLS_ERR_OTHER;
    }
    if (SSL_CTX_set_min_proto_version(ctx->ctx, min_version) != 1 ||
        SSL_CTX_set_max_proto_version(ctx->ctx, max_version) != 1) {
        capture_error(error, error_len, "failed to set test protocol bounds");
        return PEERTLS_ERR_SSL;
    }
    return 0;
}

int peertls_ssl_version(peertls_ssl* s) {
    if (s == NULL || s->ssl == NULL || s->write_failed) return 0;
    return SSL_version(s->ssl);
}

size_t peertls_pending_write_size(peertls_ssl* s) {
    if (s == NULL) return 0;
    return s->pending_write_len;
}
