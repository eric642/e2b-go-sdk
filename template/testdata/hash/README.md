# Hash fixtures

Fixtures consumed by `TestCalculateFilesHash`. The test validates
determinism and input-sensitivity of `calculateFilesHash`; it does not
pin byte-exact golden hashes.

Cross-SDK byte-exact parity with the Python SDK's
`calculate_files_hash` (needed for shared cache hits) is validated in
an integration context, not here — pinning a byte-exact golden in a
unit test is flaky across machines because the hash incorporates
directory `stat()` output whose permission bits vary with umask,
filesystem, and ACLs.
