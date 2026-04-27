/* shim.h - thin OpenSSL shim for XRPL peer TLS.
 *
 * Connection model: the SSL object is bound to a memory BIO_pair (no fd
 * is passed to OpenSSL). Go pumps bytes between the network BIO and the
 * underlying net.Conn; OpenSSL only ever sees memory buffers. This
 * keeps deadlines, context cancellation, and goroutine teardown on the
 * Go side.
 *
 * Error convention: shim functions returning int return:
 *   >  0   bytes processed (read/write/bio_read/bio_write)
 *   == 0   clean shutdown / EOF
 *   <  0   negative SSL_get_error code (one of PEERTLS_ERR_*)
 */

#ifndef PEERTLS_SHIM_H
#define PEERTLS_SHIM_H

#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct peertls_ctx peertls_ctx;
typedef struct peertls_ssl peertls_ssl;

/* Error codes (negative). Matches SSL_ERROR_* but stable across OpenSSL
 * versions and non-overlapping with positive byte counts. */
#define PEERTLS_ERR_WANT_READ  -1
#define PEERTLS_ERR_WANT_WRITE -2
#define PEERTLS_ERR_SYSCALL    -3
#define PEERTLS_ERR_SSL        -4
#define PEERTLS_ERR_ZERO_RET   -5
#define PEERTLS_ERR_OTHER      -99

/* Context lifecycle. is_server=1 for a server-role context (uses
 * TLS_server_method), 0 for client. The context owns the cert/key. */
peertls_ctx* peertls_ctx_new(int is_server);
void         peertls_ctx_free(peertls_ctx* ctx);

/* Load an X509 certificate + private key from PEM buffers. Returns 0 on
 * success, negative error code on failure. */
int peertls_ctx_use_cert_pem(peertls_ctx* ctx,
                              const char* cert, int cert_len,
                              const char* key,  int key_len);

/* SSL lifecycle. peertls_new returns a new SSL bound to a fresh
 * BIO_pair. Once created, all I/O goes through peertls_{read,write} and
 * peertls_bio_{read,write}. */
peertls_ssl* peertls_new(peertls_ctx* ctx);
void         peertls_free(peertls_ssl* s);

/* Drive the handshake. Returns 0 on success, PEERTLS_ERR_WANT_READ or
 * PEERTLS_ERR_WANT_WRITE if more network I/O is needed, or another
 * negative error. The Go pump loops on this function. */
int peertls_handshake(peertls_ssl* s);

/* Encrypted application I/O. Same return convention as handshake. */
int peertls_read (peertls_ssl* s, void* buf, int len);
int peertls_write(peertls_ssl* s, const void* buf, int len);

/* Drain/fill the network BIO. peertls_bio_read pulls outgoing TLS
 * record bytes that OpenSSL has produced; peertls_bio_write feeds
 * incoming TLS record bytes from the wire into OpenSSL. Returns the
 * number of bytes processed; 0 means no bytes available / no space. */
int peertls_bio_read (peertls_ssl* s, void* buf, int len);
int peertls_bio_write(peertls_ssl* s, const void* buf, int len);

/* TLS 1.2 Finished bytes. Returns the number of bytes copied (typically
 * 12 for TLS 1.2), or 0 if the handshake hasn't completed. */
int peertls_get_finished     (peertls_ssl* s, void* buf, int len);
int peertls_get_peer_finished(peertls_ssl* s, void* buf, int len);

/* Send TLS close_notify. Idempotent. */
int peertls_shutdown(peertls_ssl* s);

/* Returns a static pointer to the most recent SSL error string in this
 * thread. Only valid until the next OpenSSL call on this thread. */
const char* peertls_last_error(void);

#ifdef __cplusplus
}
#endif

#endif /* PEERTLS_SHIM_H */
