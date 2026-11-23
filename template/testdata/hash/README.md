# Hash fixtures

Minimal fixture trees (`single/`, `nested/`, `ignored/`) consumed by
`TestCalculateFilesHash` and `TestCalculateFilesHash_UpstreamGoldenParity`
in `template/hash_test.go`.

Because `calculateFilesHash` excludes filesystem-dependent directory sizes
(matching upstream Python `utils.py` and JS `utils.ts`), the expected digests
are deterministic across macOS APFS and Linux ext4 once file/directory modes
are normalized to `0644`/`0755`.

The expected SHA-256 digests (verified directly against upstream
`E2B/packages/dockerfile-utils-js/src/dockerignore.ts` `PatternMatcher` and
Python `utils.py::calculate_files_hash` semantics) and the `dockerignore`
fixture tree (covering `node_modules`, `dist/`, `/secret.txt`, and
`!dist/keep.txt`) live directly in `template/hash_test.go`.
