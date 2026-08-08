/* shim.h - thin OpenSSL shim for XRPL peer TLS. */

#ifndef PEERTLS_SHIM_H
#define PEERTLS_SHIM_H

#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct peertls_ctx peertls_ctx;
typedef struct peertls_ssl peertls_ssl;

#define PEERTLS_ERROR_SIZE 256
#define PEERTLS_DEFAULT_CIPHER_LIST "TLSv1.2:!CBC:!DSS:!PSK:!eNULL:!aNULL"

#define PEERTLS_ERR_WANT_READ     -1
#define PEERTLS_ERR_WANT_WRITE    -2
#define PEERTLS_ERR_SYSCALL       -3
#define PEERTLS_ERR_SSL           -4
#define PEERTLS_ERR_ZERO_RET      -5
#define PEERTLS_ERR_WRITE_RETRY   -6
#define PEERTLS_ERR_OTHER        -99

peertls_ctx* peertls_ctx_new(int is_server, const char* cipher_list,
                             char* error, size_t error_len);
void         peertls_ctx_free(peertls_ctx* ctx);

int peertls_ctx_use_cert_pem(peertls_ctx* ctx,
                             const char* cert, size_t cert_len,
                             const char* key, size_t key_len,
                             char* error, size_t error_len);

peertls_ssl* peertls_new(peertls_ctx* ctx, char* error, size_t error_len);
void         peertls_free(peertls_ssl* s);

int peertls_handshake(peertls_ssl* s, char* error, size_t error_len);
int peertls_shutdown(peertls_ssl* s, char* error, size_t error_len);

int peertls_read(peertls_ssl* s, void* buf, size_t len,
                 char* error, size_t error_len);
int peertls_write(peertls_ssl* s, const void* buf, size_t len,
                  char* error, size_t error_len);

int peertls_bio_read(peertls_ssl* s, void* buf, size_t len,
                     char* error, size_t error_len);
int peertls_bio_write(peertls_ssl* s, const void* buf, size_t len,
                      char* error, size_t error_len);

/* These return the full Finished length even when len is smaller. */
size_t peertls_get_finished(peertls_ssl* s, void* buf, size_t len);
size_t peertls_get_peer_finished(peertls_ssl* s, void* buf, size_t len);

/* Narrow state inspection used by direct shim tests. */
int           peertls_ctx_protocol_bounds(peertls_ctx* ctx,
                                          int* min_version,
                                          int* max_version);
unsigned long peertls_ctx_options(peertls_ctx* ctx);
unsigned long peertls_required_options(void);
int           peertls_ctx_set_protocol_bounds_for_test(
                  peertls_ctx* ctx, int min_version, int max_version,
                  char* error, size_t error_len);
int           peertls_ssl_version(peertls_ssl* s);
size_t        peertls_pending_write_size(peertls_ssl* s);

#ifdef __cplusplus
}
#endif

#endif /* PEERTLS_SHIM_H */
