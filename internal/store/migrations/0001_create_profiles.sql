-- user_id is the ZITADEL subject (token `sub`), forwarded by Envoy as X-User-ID.
CREATE TABLE profiles (
    user_id      text        PRIMARY KEY,
    display_name text        NOT NULL DEFAULT '',
    email        text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
