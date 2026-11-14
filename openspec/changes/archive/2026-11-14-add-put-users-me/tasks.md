# Tasks

## 1. Validation error format

- [x] 1.1 In `internal/httpapi/errors.go` add `codeValidationFailed = "validation_failed"`, the field codes `required`, `too_long`, `invalid_character`, `fieldError{Field, Code, Detail}` and `Errors []fieldError` (`json:"errors,omitempty"`) on `errorResponse`; add `writeValidationError(w, r, errs)` sharing one writer with `writeError`. Verify with a test that a validation response has status 400, code `validation_failed` and the `errors` list, and that a `writeError` response has no `errors` member.
- [x] 1.2 Document `validation_failed` (400) and the `errors` member with an example in the README "Errors" section; verify the example matches the JSON the test from 1.1 produces.

## 2. Store upsert

- [x] 2.1 Add `(*Store).UpsertProfile(ctx, userID, displayName string) (Profile, bool, error)` in `internal/store/store.go` with the single `INSERT ... ON CONFLICT ... RETURNING ..., (old.user_id IS NULL) AS created` statement from design.md; verify `go build ./...` passes.
- [x] 2.2 Extend `internal/store/store_test.go`: create (`created=true`, empty email, `created_at = updated_at`), changed update (`created=false`, new `updated_at`, same `created_at` and email, including an email inserted by hand), no-op update (`created_at` and `updated_at` unchanged) and concurrent upserts of one user from several goroutines (one row, exactly one `created`). Verify with `PROFILE_TEST_DATABASE_URL='postgres://profile:profile_dev@localhost:5432/profile?sslmode=disable' go test -race ./internal/store/` against the compose PostgreSQL.

## 3. Display name validation

- [x] 3.1 Add `validateDisplayName` (trim with `strings.TrimSpace`, at most 64 runes, no `unicode.IsControl`) and `maxDisplayNameLen` in `internal/httpapi`; verify with a table test: `""`, whitespace-only, 64 × `é`, 64 emoji, 64 runes with surrounding spaces and a trailing `\n` (accepted, trimmed), 65 runes (`too_long`), inner `\n`, `\t`, NUL (`invalid_character`), ZWJ emoji sequence and non-Latin scripts (accepted unchanged).

## 4. PUT /v1/users/me

- [x] 4.1 Add `UpsertProfile` to the `Store` interface, register `PUT /v1/users/me`, implement `putMe` (shared 401 helper with `getMe`, `decodeJSON` into a `*string` field, `required` on nil, validation, upsert, 201/200 with the profile, 500 via `internalError`), and extend `fakeStore` with upsert semantics and an injectable error; verify `go build ./...` and the existing tests pass, with `TestRouterErrors` now expecting `Allow: GET, HEAD, PUT`.
- [x] 4.2 Handler tests in `internal/httpapi/api_test.go` for: 201 (body shape as GET, empty email), 200 changed (new `updated_at`), 200 identical and 200 whitespace-only difference (`updated_at` unchanged), 400 `invalid_body` (malformed JSON, empty body, wrong type, unknown field, `email`, `user_id`), 400 `validation_failed` (`{}`, `null`, too long, control character) with the field error, 401 (missing and blank header, `WWW-Authenticate: Bearer`, store not called), 413 (declared and streamed body), 500 (detail does not leak, error logged). Verify with `make test`.
- [x] 4.3 README: add `PUT /v1/users/me` to the API table with the request body, the 201/200 and no-op `updated_at` rules, the bootstrap flow (GET, then PUT only on 404) and a `curl -X PUT` example in the Envoy check; verify the documented responses match the handler tests.

## 5. Integration checks

- [x] 5.1 Run `make test lint` and the store test with `PROFILE_TEST_DATABASE_URL`; verify all pass.
- [x] 5.2 Deploy with `make image kind-load deploy PROJECT_ID=<zitadel project id>` and verify through Envoy with a real token (`http://api.localtest.me:8080/v1/users/me`): first PUT → 201, same PUT → 200 with unchanged `updated_at`, changed PUT → 200 with a new `updated_at`, GET → 200 with the profile, PUT without a token → 401.

## 6. Review follow-ups

- [x] 6.1 In `decodeJSON` (`internal/httpapi/handlers.go`) read one more token after the value: anything but `io.EOF` gives 400 `invalid_body`, a `*http.MaxBytesError` still gives 413. Verify with tests in `TestBodyLimit` (generic decoder: trailing garbage, second value, stray `}`, trailing whitespace accepted) and in `TestPutMe` (`{"display_name":"Ada"} garbage` and `{"display_name":"Ada"}{"email":"ada@example.com"}` give `invalid_body` with the store untouched; trailing whitespace gives 201).
- [x] 6.2 Handler tests in `TestPutMe` for `X-User-ID: "  42  "` (profile of `42`, `user_id` `42`), `?user_id=other` ignored (profile of the header user, none for `other`), the specified case-insensitive matching (`DISPLAY_NAME` accepted, last duplicate wins, `Email` rejected) and an oversized declared body without the header giving 413. Verify with `make test`.
- [x] 6.3 Rerun `make test lint` and the store test with `PROFILE_TEST_DATABASE_URL`; verify all pass.

## Workflow follow-up

- Propose the commit (files and message) and wait for approval; commit only after it.
- Archive the change after review; verify `openspec/specs/user-profile` and `openspec/specs/api-errors` are created.
