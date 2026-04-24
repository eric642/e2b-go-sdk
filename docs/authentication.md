# E2B Authentication Methods

This document walks through every authentication method accepted by the E2B
control plane (`https://api.e2b.app`) and the in-sandbox envd endpoints. It
cross-references the `securitySchemes` declared in `spec/openapi.yml` with the
actual implementation in `internal/transport/transport.go`, so you can pick the
right credential for each use case.

References:
- OpenAPI spec: `spec/openapi.yml` (`components.securitySchemes`)
- Official docs: <https://e2b.dev/docs/api-key>, <https://e2b.dev/docs/cli/auth>

## Credential overview

| Credential               | HTTP header                   | Issued by / how to obtain                           | Principal            | Typical use                              |
| ------------------------ | ----------------------------- | --------------------------------------------------- | -------------------- | ---------------------------------------- |
| Team API Key             | `X-API-Key: e2b_...`          | Dashboard → Keys; one per team                      | Team (organization)  | SDK calls that create/manage sandboxes   |
| Personal Access Token    | `Authorization: Bearer ...`   | Dashboard → Account; tied to an E2B user            | User (individual)    | CLI, CI/CD tasks that need user context  |
| Supabase Session Token   | `X-Supabase-Token: ...`       | Supabase session from the Dashboard web frontend    | User + selected team | Dashboard-internal only                  |
| Supabase Team Header     | `X-Supabase-Team: <teamID>`   | Paired with the Supabase token above                | Team selection       | Completes the Supabase auth pair         |
| Admin Token              | `X-Admin-Token: ...`          | Held only by E2B platform operators                 | Platform admin       | Node management, force-kill sandboxes    |
| Envd Access Token        | `X-Access-Token: ...` + Basic | Returned by the control plane on sandbox creation (`envdAccessToken`) | A single sandbox     | Direct envd traffic (process, fs, PTY)   |
| Volume Token (JWT)       | `Authorization: Bearer ...`   | Returned by `POST /volumes` or `GET /volumes/{id}`  | A single volume      | Read/write a volume's content            |

> The `1` and `2` in `Supabase1TokenAuth` / `Supabase2TeamAuth` only exist so
> the code generator checks the token before the team (alphabetical order);
> they carry no other semantics.

## 1. `ApiKeyAuth` — Team API Key (`X-API-Key`)

- **What it is**: a long-lived team-scoped API key, usually prefixed with
  `e2b_`.
- **How to get one**: Dashboard → *Keys*. You can also create/rotate them via
  `POST /api-keys`, `PATCH /api-keys/{apiKeyID}`, `DELETE /api-keys/{apiKeyID}`.
- **Who it represents**: the entire team. Every call made with it shares the
  same quota and permissions.
- **How the SDK uses it**:
  - Via the `E2B_API_KEY` environment variable, or explicitly through
    `Config{APIKey: ...}`.
  - Carried on `Auth.APIKey` and injected as the `X-API-Key` header by
    `AsRequestEditor` (see `internal/transport/transport.go:36`).
- **Endpoints that accept it**: almost every business endpoint —
  `/sandboxes`, `/v2/sandboxes`, `/templates`, `/volumes`, `/snapshots`,
  `/teams/{teamID}/metrics`, etc.
- **When to use it**: **this is the default for the SDK and the right choice
  for production services.**

## 2. `AccessTokenAuth` — Personal Access Token (`Authorization: Bearer`)

- **What it is**: a user-bound access token,
  `bearerFormat: access_token` in the spec.
- **How to get one**: Dashboard → *Account*. Or via `POST /access-tokens` to
  create and `DELETE /access-tokens/{accessTokenID}` to revoke.
- **Who it represents**: **the user** (across teams), which is why it can do
  things a team key cannot — e.g. list "which teams do I belong to".
- **How the SDK uses it**:
  - Via `E2B_ACCESS_TOKEN`, or `Config{AccessToken: ...}`.
  - Injected as `Authorization: Bearer <token>` (`transport.go:40`).
  - If both `APIKey` and `AccessToken` are set, the current implementation
    sends **both** headers, but the server treats the API key's team context
    as authoritative. The spec (`config.go:39` and the comment
    `Exactly one of APIKey or AccessToken should be set`) recommends picking
    exactly one.
- **Endpoints that accept it** (those declaring `AccessTokenAuth` in OpenAPI):
  - `GET /teams` — list teams the caller belongs to
  - `GET|POST /templates`, `POST|PATCH|DELETE /templates/{templateID}` — user-
    scoped template management
  - `POST /templates/{templateID}/builds/{buildID}` and related build
    endpoints
  - `GET /templates/{templateID}/files/{hash}` — upload build artifacts
- **When to use it**:
  - **CLI**: the token written to `~/.e2b` after `e2b auth login` is this one.
  - **CI/CD**: use `E2B_ACCESS_TOKEN` in pipelines that build or publish
    templates.
  - Creating sandboxes at runtime does **not** need it — use the team API key.

## 3. `Supabase1TokenAuth` + `Supabase2TeamAuth` — Dashboard session

- **What it is**: the E2B Dashboard frontend authenticates users through
  Supabase, then sends the Supabase session token in `X-Supabase-Token` and
  the currently selected team ID in `X-Supabase-Team`. Both headers together
  form one identity.
- **Who it represents**: the signed-in user plus the currently selected team.
- **How the SDK uses it**: **it doesn't**. This is a browser-only protocol
  used by the Dashboard; the SDK and CLI never send these headers.
- **Why it still appears in the OpenAPI spec**: the same spec is used to
  generate both the backend and the Dashboard's frontend client.

## 4. `AdminTokenAuth` — Platform admin (`X-Admin-Token`)

- **What it is**: an E2B platform administrator credential.
- **Who it represents**: operators / SRE, not regular teams or users.
- **Endpoints that accept it** (those declaring `AdminTokenAuth` in OpenAPI):
  - `GET /nodes`, `GET|POST /nodes/{nodeID}` — cluster node management
  - `POST /admin/teams/{teamID}/sandboxes/kill` — force-kill every sandbox in
    a team
  - `POST /admin/teams/{teamID}/builds/cancel` — cancel every build for a team
- **When to use it**: regular users and teams **cannot obtain** this token
  and should never reference it from the SDK. It is listed here for
  completeness only.

## 5. Envd Access Token — short-lived, sandbox-scoped token

> This has no dedicated `securityScheme` in the OpenAPI spec, but it is the
> most frequently overlooked — and most commonly mis-set — credential in
> day-to-day use.

- **What it is**: the `envdAccessToken` returned by the control plane when a
  sandbox is created (see the `Sandbox` and `SandboxDetail` schemas).
- **Who it represents**: **one specific sandbox**. The token is invalidated
  the moment the sandbox is destroyed.
- **How to send it** (see `internal/transport/transport.go:94`):
  - Connect-RPC traffic (process, filesystem, PTY): `X-Access-Token: <token>`
    plus `Authorization: Basic base64("<user>:")` — HTTP Basic with the user
    name in the *username* slot and an empty password. The default user is
    `"user"`; switch to `"root"` when root is required.
  - Plain HTTP endpoints (`/files`, `/metrics`, `/envs`): same combination,
    `X-Access-Token` + Basic.
- **When to use it**: the SDK adds this automatically for all envd traffic,
  so application code never has to touch it. You only need to know about it
  when you build your own HTTP client or debug a specific envd endpoint.

## 6. Volume Token — JWT for volume content

- **What it is**: the JWT returned by `POST /volumes` or
  `GET /volumes/{volumeID}`.
- **Who it represents**: **read/write access to a single volume**.
- **How to send it**: `Authorization: Bearer <token>`, targeted at the volume
  content plane (not `api.e2b.app`). See `NewVolumeAPIClient`
  (`internal/transport/transport.go:158`).
- **When to use it**: uploading or downloading files inside a volume. Because
  it is decoupled from the team API key, you can safely hand a volume token
  to a downstream service that only needs to read or write that one volume.

## Which one should I use?

Sorted by everyday frequency:

1. **Run workloads, create sandboxes** → Team **API Key** (`E2B_API_KEY`).
2. **CI/CD that builds templates, CLI login** → Personal **Access Token**
   (`E2B_ACCESS_TOKEN`).
3. **Execute commands or manage files inside a sandbox** → the SDK uses the
   **Envd Access Token** automatically; no action required.
4. **Read/write volume files** → **Volume Token** returned by the volumes
   API.
5. **Supabase / Admin Token** → not for regular users; ignore.

## Where each credential lives in this SDK

- `Config.APIKey`, `Config.AccessToken` — `config.go:39` (public entry point)
- `Auth.AsRequestEditor` — `internal/transport/transport.go:34`
  (injects `X-API-Key` / `Authorization: Bearer` for the control plane)
- `EnvdAuth.applyHeader` — `internal/transport/transport.go:94`
  (injects `X-Access-Token` + Basic for envd)
- `NewVolumeAPIClient` — `internal/transport/transport.go:158`
  (injects `Authorization: Bearer <jwt>` for the volume content plane)
