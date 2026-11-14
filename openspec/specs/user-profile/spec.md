# user-profile Specification

## Purpose

The caller's own profile at `/v1/users/me`: the user creates it lazily after login and replaces its
client-writable fields later. The user is always identified by the gateway, never by the request.

## Requirements

### Requirement: Caller identity comes only from the gateway header
`PUT /v1/users/me` SHALL act on the profile of the user named in `X-User-ID` (the token `sub`,
set by Envoy), after trimming whitespace. It MUST NOT take a user id from the path, query or body.
A missing or blank header SHALL produce 401 with code `unauthenticated` and `WWW-Authenticate: Bearer`,
unless the request was already rejected for its body size.

#### Scenario: Missing header
- **WHEN** a PUT arrives without `X-User-ID`, or with a blank value
- **THEN** the response is 401 `application/problem+json` with code `unauthenticated` and header `WWW-Authenticate: Bearer`
- **AND** no profile is created or changed

#### Scenario: Header value is trimmed
- **WHEN** a PUT arrives with `X-User-ID: "  42  "`
- **THEN** the profile of user `42` is created or replaced, and the response has `user_id` `42`

#### Scenario: User id in the query is ignored
- **WHEN** a PUT with `X-User-ID: 42` is sent to `/v1/users/me?user_id=other`
- **THEN** the profile of user `42` is created or replaced and no profile of `other` is created or changed

#### Scenario: User id in the body is not accepted
- **WHEN** the body is `{"display_name":"Ada","user_id":"other"}`
- **THEN** the response is 400 with code `invalid_body` and no profile is created or changed

### Requirement: The body size limit is checked before authentication
A request whose declared `Content-Length` exceeds `MAX_BODY_BYTES` SHALL get 413 with code
`request_too_large` before the caller is checked, so it gets 413 even without `X-User-ID`. Through
Envoy an unauthenticated request never reaches the service, so this order is only visible to direct
callers.

#### Scenario: Oversized request without the header
- **WHEN** a PUT without `X-User-ID` declares a `Content-Length` over `MAX_BODY_BYTES`
- **THEN** the response is 413 with code `request_too_large`, not 401

### Requirement: PUT creates the profile when none exists
A PUT for a user without a profile SHALL create it with the given display name and an empty email,
and SHALL answer 201 Created with the profile as body, in the same shape as `GET /v1/users/me`
(`user_id`, `display_name`, `email`, `created_at`, `updated_at`).

#### Scenario: First PUT
- **GIVEN** user `312345678901234567` has no profile
- **WHEN** they send `PUT /v1/users/me` with `{"display_name":"Ada"}`
- **THEN** the response is 201 with `Content-Type: application/json` and body `user_id` `312345678901234567`, `display_name` `Ada`, `email` `""`, and `created_at` equal to `updated_at`
- **AND** a following `GET /v1/users/me` returns 200 with the same profile

### Requirement: PUT replaces the client-writable fields of an existing profile
A PUT for a user with a profile SHALL replace its client-writable fields (today only `display_name`)
and SHALL answer 200 OK with the profile as body. It MUST NOT change `user_id`, `email` or
`created_at`. When the stored display name changes, `updated_at` SHALL be set to the time of the change.

#### Scenario: Changed display name
- **GIVEN** the caller's profile has `display_name` `Ada`
- **WHEN** they send `{"display_name":"Ada Lovelace"}`
- **THEN** the response is 200 with `display_name` `Ada Lovelace`, the same `created_at` and `email`, and an `updated_at` later than before

#### Scenario: Clearing the display name
- **GIVEN** the caller's profile has `display_name` `Ada`
- **WHEN** they send `{"display_name":""}`
- **THEN** the response is 200 with `display_name` `""` and a new `updated_at`

### Requirement: A PUT that changes nothing is a no-op
A PUT whose display name, after trimming, equals the stored one SHALL answer 200 OK with the stored
profile and SHALL leave `updated_at` and `created_at` unchanged, so that `updated_at` always reflects
the last real change.

#### Scenario: Repeated identical PUT
- **GIVEN** the caller's profile was created with `{"display_name":"Ada"}`
- **WHEN** they send `{"display_name":"Ada"}` again
- **THEN** the response is 200, not 201, and `created_at` and `updated_at` equal those of the first response

#### Scenario: Only surrounding whitespace differs
- **GIVEN** the caller's profile has `display_name` `Ada`
- **WHEN** they send `{"display_name":"  Ada "}`
- **THEN** the response is 200 with `display_name` `Ada` and an unchanged `updated_at`

### Requirement: Concurrent PUTs for the same user are safe
Concurrent PUTs for the same user SHALL leave exactly one profile; exactly one of them SHALL answer
201 and every other one 200, and none SHALL fail because of the race.

#### Scenario: Two first-login calls race
- **GIVEN** the user has no profile
- **WHEN** several PUTs for that user run at the same time
- **THEN** one profile row exists, exactly one response is 201 and the others are 200

### Requirement: The request body has exactly the client-writable fields
The body SHALL be a single JSON object with the member `display_name`. Malformed JSON, an empty body, a
non-object body, a wrongly typed value or any other member SHALL produce 400 with code `invalid_body`.
`email` is not client-writable and SHALL be rejected like any unknown member. A body over the
configured limit (`MAX_BODY_BYTES`) SHALL produce 413 with code `request_too_large`.

#### Scenario: Email in the body
- **WHEN** the body is `{"display_name":"Ada","email":"ada@example.com"}`
- **THEN** the response is 400 with code `invalid_body` and no profile is created or changed

#### Scenario: Malformed JSON
- **WHEN** the body is `{"display_name":`
- **THEN** the response is 400 with code `invalid_body`

#### Scenario: Wrong type
- **WHEN** the body is `{"display_name":42}`
- **THEN** the response is 400 with code `invalid_body`

#### Scenario: Body over the limit
- **WHEN** the body is larger than `MAX_BODY_BYTES`, with or without a `Content-Length`
- **THEN** the response is 413 with code `request_too_large`

### Requirement: Nothing may follow the JSON object
After the JSON object the body SHALL contain only whitespace. Any other data, including a second JSON
value, SHALL produce 400 with code `invalid_body`, and no profile is created or changed.

#### Scenario: Trailing garbage
- **WHEN** the body is `{"display_name":"Ada"} garbage`
- **THEN** the response is 400 with code `invalid_body` and no profile is created or changed

#### Scenario: Second JSON value
- **WHEN** the body is `{"display_name":"Ada"}{"email":"ada@example.com"}`
- **THEN** the response is 400 with code `invalid_body` and no profile is created or changed

#### Scenario: Trailing whitespace
- **WHEN** the body is `{"display_name":"Ada"}` followed by spaces and a newline
- **THEN** the PUT succeeds

### Requirement: Member names are matched case-insensitively
Member names SHALL be matched against `display_name` without regard to case, and when a member
appears more than once the last value SHALL be used. This is the accepted current behavior; a name
that matches no field in any case is still an unknown member.

#### Scenario: Different case
- **WHEN** the body is `{"DISPLAY_NAME":"Ada"}`
- **THEN** the PUT succeeds with `display_name` `Ada`

#### Scenario: Repeated member
- **WHEN** the body is `{"display_name":"Ada","Display_Name":"Eve"}`
- **THEN** the PUT succeeds with `display_name` `Eve`

#### Scenario: Unknown member in another case
- **WHEN** the body is `{"display_name":"Ada","Email":"ada@example.com"}`
- **THEN** the response is 400 with code `invalid_body`

### Requirement: The display name is required
`display_name` SHALL be present in every PUT. A body without it, including `{}` and
`{"display_name":null}`, SHALL produce 400 `validation_failed` with a `required` error for the field,
so that a repeated bootstrap call can never erase a name.

#### Scenario: Empty object
- **WHEN** the body is `{}`
- **THEN** the response is 400 with code `validation_failed` and a field error for `display_name` with code `required`
- **AND** an existing profile is not changed

#### Scenario: Explicit null
- **WHEN** the body is `{"display_name":null}`
- **THEN** the response is 400 with code `validation_failed` and a field error for `display_name` with code `required`

### Requirement: Display name rules
The service SHALL trim leading and trailing Unicode whitespace from `display_name` and validate the
result: it MAY be empty (no display name); it SHALL have at most 64 Unicode code points (not bytes);
it MUST NOT contain control characters (Unicode category Cc). Any other character is allowed and
stored as sent; there is no other normalization.

#### Scenario: Longest allowed name in a multi-byte script
- **WHEN** `display_name` is 64 code points of `é` or of an emoji (more than 64 bytes)
- **THEN** the PUT succeeds and the name is stored unchanged

#### Scenario: Too long
- **WHEN** `display_name` has 65 code points after trimming
- **THEN** the response is 400 with code `validation_failed` and a field error for `display_name` with code `too_long`

#### Scenario: Surrounding whitespace does not count
- **WHEN** `display_name` is 64 code points surrounded by spaces
- **THEN** the PUT succeeds and the stored name has no surrounding spaces

#### Scenario: Control character inside the name
- **WHEN** `display_name` is `"Ada\nLovelace"`, `"Ada\tL"` or contains NUL
- **THEN** the response is 400 with code `validation_failed` and a field error for `display_name` with code `invalid_character`

#### Scenario: Whitespace-only name
- **WHEN** `display_name` is `"   "`
- **THEN** the PUT succeeds and the stored name is `""`

### Requirement: Storage failures are internal errors
A failure of the database during a PUT SHALL produce 500 with code `internal_error` and no internal
detail in the response; the error SHALL be logged.

#### Scenario: Database error
- **WHEN** the database fails while the profile is written
- **THEN** the response is 500 with code `internal_error`, its `detail` does not contain the database error, and the error appears in the log
