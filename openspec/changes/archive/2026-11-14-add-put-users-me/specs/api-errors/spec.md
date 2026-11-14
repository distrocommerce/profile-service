# Spec Delta

## Purpose

The error contract of the service's HTTP API: every error is an RFC 9457 problem response with a
stable `code`, so clients can switch on codes instead of parsing messages.

## ADDED Requirements

### Requirement: Validation errors use the validation_failed code
A request whose body is well-formed but has invalid field values SHALL produce 400
`application/problem+json` with `type` `about:blank`, `title` `Bad Request`, `status` 400, the request
path as `instance`, and the stable code `validation_failed`. Malformed or unknown input keeps the
code `invalid_body`.

#### Scenario: Invalid field value
- **WHEN** a client sends `PUT /v1/users/me` with a `display_name` of 65 code points
- **THEN** the response is 400 `application/problem+json` with `code` `validation_failed` and `instance` `/v1/users/me`

#### Scenario: Malformed body is not a validation error
- **WHEN** a client sends a body that is not valid JSON
- **THEN** the response code is `invalid_body`, not `validation_failed`

### Requirement: Validation errors list every invalid field
A `validation_failed` response SHALL carry the extension member `errors`: a non-empty array with one
object per invalid field, each with `field` (the JSON member name), a stable `code` (`required`,
`too_long` or `invalid_character`) and a human-readable `detail`. Other error responses SHALL NOT
carry `errors`.

#### Scenario: Field error shape
- **WHEN** a client sends `PUT /v1/users/me` with `{}`
- **THEN** the body contains `"errors":[{"field":"display_name","code":"required","detail":"..."}]` with a non-empty `detail`

#### Scenario: Other errors have no field list
- **WHEN** a request fails with any code other than `validation_failed`
- **THEN** the problem response has no `errors` member
