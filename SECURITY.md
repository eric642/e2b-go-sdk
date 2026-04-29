# Security Policy

## Reporting a vulnerability

**Please do not open a public GitHub issue for security vulnerabilities.**

Instead, report them privately by:

- Opening a [private security advisory](https://github.com/eric642/e2b-go-sdk/security/advisories/new) on this repository, **or**
- Emailing the maintainer listed in the repository's commit history.

Please include:

- A description of the issue and its potential impact.
- Steps to reproduce (or a proof-of-concept).
- Affected versions / commits, if you know them.

You can expect an initial acknowledgement within **72 hours** and a
substantive reply within **7 days**. If the issue is confirmed, we'll
coordinate a fix and disclosure timeline with you.

## Scope

This repository is a **client library** for the E2B sandbox platform.
Vulnerabilities in scope include:

- Credential leakage or mishandling in the SDK itself.
- Code paths that could let a malicious sandbox escape intended bounds
  via SDK APIs.
- Denial-of-service issues reachable through the public Go API surface.

Vulnerabilities in upstream [`e2b-dev/E2B`](https://github.com/e2b-dev/E2B)
or in the E2B control plane itself are **out of scope here** — please
report those directly to the E2B team.

## Supported versions

Active development happens on `main`. Security fixes are cut as a new
release on the tracked upstream tag (see [CONTRIBUTING.md](./CONTRIBUTING.md)
for the version policy). Older releases receive fixes on a best-effort
basis.
