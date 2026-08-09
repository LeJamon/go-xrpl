# Confidential MPT native backend

The optional `mptcrypto` build tag links the official XRPLF `mpt-crypto` 1.0.2
package through a fixed C ABI. The dependency graph is the graph locked by
rippled 3.3.0:

- `mpt-crypto/1.0.2#b313cef0c1a493eb970ad185b2e9bab7`
- source tag object `bec300394509a0d1bc82fee63b5365dc9e5db20e`
- peeled source commit `376691c5fca7898bd1161b0ac6646109d069ceb9`
- source archive SHA-256 `171f27d8acf88a030bb2480f0aedbfdc523445e429729b824fdd7384cdd730b1`
- `secp256k1/0.7.1#b1f450b7f78a36fff75bb6934a356f3a`
- `openssl/3.6.3#f806de8933e3bf6f01016c6a888cee2e`

Run `just setup-mpt-crypto`, then `just test-mpt-crypto`. The test recipe runs
the complete upstream 1.0.2 C/C++ suite before the Go bridge tests. Setup uses the
repository-local `.conan-home` as `CONAN_HOME`, so it neither reads
nor changes the user's global Conan remotes or cache.

The upstream source archive does not contain a license or notice. This
repository therefore does not vendor or redistribute its sources, headers,
libraries, or linked binaries. Obtain legal clearance before distributing an
`mptcrypto`-linked artifact.

Without both the build tag and cgo, the backend is unavailable and the
ConfidentialTransfer amendment remains unsupported. Builds with both
`mptcrypto` and cgo advertise the complete transaction family as supported only
when the native secp256k1 context initializes successfully; the amendment
remains vote-default-no in every build.
