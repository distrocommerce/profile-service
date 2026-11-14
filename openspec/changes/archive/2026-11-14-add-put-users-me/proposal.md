# Proposal

## Why

A profile row can only be inserted by hand today, so `GET /v1/users/me` answers 404 for every real
user. Clients need one call that creates their profile on first login and updates it later; other
services never assume a profile exists, so the profile is created lazily by its owner.

## What Changes

- New endpoint `PUT /v1/users/me`: idempotent create-or-update of the caller's profile.
  - Identity only from `X-User-ID` (set by Envoy from the token `sub`); never from the path or body.
    Missing or blank header → 401 `unauthenticated`, as for GET.
  - Full replacement of the client-writable fields. Body: `{"display_name": string}`, read through
    the shared JSON decoder (body limit → 413 `request_too_large`, malformed JSON or unknown
    fields, including `email`, → 400 `invalid_body`).
  - `display_name` is required (a missing field, `null` or `{}` → 400 `validation_failed`), trimmed,
    may be empty, at most 64 Unicode code points, no control characters.
  - 201 Created when the profile is created, 200 OK when it is updated or unchanged; the body is the
    profile in the same shape as GET.
  - A PUT that changes nothing leaves `updated_at` unchanged: `updated_at` is the time of the last
    real change.
- New stable error code `validation_failed` (400) whose problem response lists per-field errors.
- `email` stays empty for new profiles; it is never client-writable (it will come from a verified
  ZITADEL claim later).
- `405` for `/v1/users/me` now advertises `Allow: GET, HEAD, PUT`.
- README: endpoint, request body, bootstrap flow (GET, then PUT only on 404), error codes.

Out of scope: PATCH and DELETE, create-only semantics (`If-None-Match: *`), addresses, email from a
token claim, Kafka events (`profile.updated`), OTLP logs, CI.

## Capabilities

### New Capabilities
- `user-profile`: the caller's own profile resource at `/v1/users/me`: identity from the gateway
  header, create-or-update, field rules and response shape.
- `api-errors`: the service-wide error contract, starting with the `validation_failed` code and its
  per-field details (the existing RFC 9457 format is not yet specified; only the parts this change
  adds are).

### Modified Capabilities
None (`openspec/specs/` is empty).

## Impact

- Code: `internal/httpapi` (route, handler, validation, `errors.go` gains the code and the field
  error list), `internal/store` (upsert method and its integration test). No migration: the existing
  `profiles` table is enough.
- API: new method on an existing path; GET is unchanged. The HTTPRoute matches `/v1/users` by
  prefix without a method filter, so the chart and `service-lib` need no change.
- Dependencies: no new modules. The upsert uses `RETURNING old` and so needs PostgreSQL ≥ 18; every
  environment is pinned to exactly 18.6.
