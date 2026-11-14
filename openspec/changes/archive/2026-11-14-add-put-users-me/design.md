# Design

## Context

See proposal.md for the motivation; requirements are in `specs/user-profile` and `specs/api-errors`.

Current state that shapes the approach:
- `internal/httpapi`: one ServeMux; `getMe` reads the caller from `userID(r)` (trimmed `X-User-ID`)
  and answers 401 with `WWW-Authenticate: Bearer` when it is blank. `decodeJSON` already uses
  `DisallowUnknownFields` and maps `*http.MaxBytesError` to 413 and everything else to 400
  `invalid_body`; nothing uses it yet. `errors.go` holds the codes, `errorResponse` and `writeError`.
- `internal/store`: `GetProfile` with one query; the `Store` interface in httpapi lists what the
  handlers need, and `api_test.go` has a `fakeStore`.
- Table `profiles` (migration 0001): `display_name` and `email` are `text NOT NULL DEFAULT ''`,
  `created_at`/`updated_at` default `now()`. No change is needed.
- PostgreSQL is pinned to exactly 18.6 in every environment (local compose and RDS). The HTTPRoute
  matches the `/v1/users` prefix for every method.

## Goals / Non-Goals

**Goals:**
- One handler and one SQL statement per PUT; no transaction or read-before-write in Go.
- Validation as a pure function, tested without HTTP.
- The field-error format in `errors.go`, reusable by later write endpoints.

**Non-Goals:**
- No database CHECK constraint on `display_name` (the service is the only writer; a later migration
  can add one).
- No `Content-Type` check on requests (no 415); `decodeJSON` does not check it for any endpoint.
- `decodeJSON` keeps case-insensitive member names and last-wins duplicates (see Risks); its only
  change is the trailing-data check below.
- No `Location` header on 201: for PUT the created resource is the target URI (RFC 9110, 9.3.4 and
  15.3.2).

## Decisions

**Handler order: identity → decode → validate → store → respond.** `putMe` checks the header first,
using the same 401 response as `getMe` (factored into a small helper both handlers call), so an
unauthenticated request never reads the body (a declared oversized body is still rejected earlier
by `limitBody`, as the spec states). The request type is
`putMeRequest{ DisplayName *string \`json:"display_name"\` }`, and `decodeJSON` decodes into a
`*putMeRequest`: a body of JSON `null` leaves that pointer nil and gives 400 `invalid_body` ("the
request body must be a JSON object"); a nil `DisplayName` (member missing or `null`) is the
`required` error. Decoding into pointers is the simplest way to tell "missing" from `""` with
`encoding/json`; a `map[string]any` or `json.RawMessage` would need its own type checks.

**Trailing data: one more token must be EOF.** After decoding the value, `decodeJSON` reads one more
token with `dec.Token()`; anything but `io.EOF` (a second value, garbage, a stray `}`) is 400
`invalid_body`, and a `*http.MaxBytesError` there still gives 413. Whitespace after the value is
skipped by the decoder. `dec.More()` was not used: it reports false for a stray `}` or `]`, so
`{...}}` would pass. This applies to every endpoint that uses `decodeJSON`.
The route is `PUT /v1/users/me`, so 405 for that path advertises `GET, HEAD, PUT` (the mux sorts the
methods: `slices.Sorted` in `(*ServeMux).matchingMethods`, checked in the Go 1.27.3 source).

**Validation: trim, then check.** `validateDisplayName(string) (string, *fieldError)` (a single
field has at most one error; the handler wraps it in the list):
`strings.TrimSpace` (Unicode whitespace, `unicode.IsSpace`), then `utf8.RuneCountInString` ≤ 64
(`maxDisplayNameLen`), then reject any rune with `unicode.IsControl`. Trimming first means a stray
leading/trailing `\n`, `\t` or U+0085 is removed silently rather than rejected; only control
characters inside the name fail (decided; the spec scenarios cover it). `unicode.IsControl` covers
only category Cc, so format characters (Cf: ZWJ in emoji sequences, bidi marks) are allowed, as the
rule "everything else is allowed" requires (see Risks). Rune count is used rather than grapheme
clusters, as decided (a flag emoji counts 2).

**Field errors in the problem response.** `errorResponse` gains
`Errors []fieldError \`json:"errors,omitempty"\`` with `fieldError{Field, Code, Detail}`;
`writeError` and a new `writeValidationError(w, r, errs []fieldError)` share one writer, so the
headers and the base members stay in one place. New constants: `codeValidationFailed`, and field
codes `required`, `too_long`, `invalid_character`. Alternative: the `invalid-params` /
`name` / `reason` shape from the RFC 9457 example. It is only an example in the RFC, and a per-field
stable `code` matches how the service already lets clients switch on codes, so it was not used.

**Store: one upsert, `created` from PostgreSQL 18 `RETURNING old`.**
`UpsertProfile(ctx, userID, displayName string) (Profile, bool, error)` runs
`INSERT ... ON CONFLICT (user_id) DO UPDATE SET display_name = EXCLUDED.display_name, updated_at = CASE
WHEN profiles.display_name IS DISTINCT FROM EXCLUDED.display_name THEN now() ELSE profiles.updated_at
END RETURNING user_id, display_name, email, created_at, updated_at, (old.user_id IS NULL) AS created`.
PostgreSQL 18 allows `old`/`new` in RETURNING; for a row that was inserted, `old` is NULL, and
`user_id` (the primary key) is never NULL in an existing row, so `old.user_id IS NULL` means
"created". This ties the service to PostgreSQL ≥ 18, which is acceptable because every environment
(local compose and RDS) is pinned to exactly 18.6.
It always returns the row, is atomic under concurrency (the conflicting row is locked, so concurrent
PUTs for one user serialize and exactly one inserts), and leaves `email` and `created_at` untouched
on update. Alternatives:
- `DO UPDATE ... WHERE <changed>`: rejected, RETURNING yields no row for a no-op.
- `SELECT` then `INSERT`/`UPDATE` in a transaction: two round trips and a race on first insert.
- `(xmax = 0) AS created`: works on any version, but relies on undocumented tuple internals;
  rejected in favour of the documented `old` form. Both were run against the compose PostgreSQL 18.6
  (temporary table, rolled back) and agree: `true` on insert, `false` on a no-op update.

**Tests.** `fakeStore` gains an upsert that mimics the statement (create → `created=true`; same
name → unchanged timestamps; new name → new `updated_at`) and an injectable error. Handler tests
cover every scenario in `specs/user-profile` (including header trimming, an ignored `?user_id=`,
trailing data and case-insensitive member names); `validateDisplayName` gets a table test. The store
test adds create, update, no-op (timestamps unchanged) and N goroutines upserting one user
(`sync.WaitGroup`, no new dependency), asserting one row and exactly one `created`.

## Risks / Trade-offs

- [`encoding/json` (v1) in `decodeJSON` matches member names case-insensitively
  (`{"DISPLAY_NAME":"x"}` is accepted) and allows duplicate members (last wins), so
  `{"display_name":"Ada","Display_Name":"Eve"}` stores `Eve`] → Accepted and specified as current
  behavior. Future hardening, as a separate change for all endpoints: move `decodeJSON` to
  `encoding/json/v2` for exact-case matching and duplicate rejection. Checked on Go 1.27.3:
  `encoding/json/v2` builds and runs without setting `GOEXPERIMENT`, because the `jsonv2` experiment
  is on by default (`go list -f '{{context.ToolTags}}' runtime` includes `goexperiment.jsonv2`, and
  `encoding/json` v1 itself is built on v2). A probe program confirmed that
  `RejectUnknownMembers(true)` rejects `DISPLAY_NAME` and that duplicates are rejected by default.
  The package files still carry `//go:build goexperiment.jsonv2`, so whether its API is covered by
  the Go 1 compatibility promise is unverified; check the Go 1.27 release notes before switching.
- [Invalid UTF-8 in the body is replaced with U+FFFD by `encoding/json`, not rejected] → Stored
  strings are always valid UTF-8; acceptable.
- [A no-op PUT still writes a new row version (dead tuple, WAL) and locks the row] → Negligible at
  the expected rate; it is what makes the single statement always return the row.
- [`RETURNING old` needs PostgreSQL ≥ 18; on an older server the statement fails to parse] → Every
  environment is pinned to exactly 18.6, and the store test runs against the real server, so a
  mismatch shows up in tests rather than in production. Any future downgrade or new environment must
  keep PostgreSQL ≥ 18.
- [Invisible format characters (Unicode Cf: bidi overrides such as U+202E, zero-width spaces) are
  allowed in `display_name`, so a name can look different from what it is or render as empty]
  → Accepted for now. Future hardening: reject a specific list of bidi controls and zero-width
  characters. Do not reject all of Cf, because ZWJ (U+200D) is needed for emoji sequences.
- [A declared `Content-Length` over the limit gets 413 from the middleware before the handler checks
  the header, so an unauthenticated oversized request sees 413, not 401] → Accepted and specified
  (`specs/user-profile`, "The body size limit is checked before authentication"). Through Envoy an
  unauthenticated request never reaches the service; the order is unchanged from today.
- [`now()` is the transaction start time] → The statement runs in its own implicit transaction, so it
  is the time of the write.

## Migration Plan

No schema migration. Deploy with `make image kind-load deploy PROJECT_ID=<id>`. Rollback: redeploy
the previous image tag; rows created by PUT are ordinary rows that the old version's GET reads.
