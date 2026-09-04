-- Initial schema for the social API described in HANDOFF.md.
--
-- IDs are opaque TEXT rather than UUID or BIGSERIAL: the contract forbids
-- clients parsing or sorting them, and TEXT keeps the door open to changing
-- the generator without a migration.

CREATE TABLE users (
    id            TEXT        PRIMARY KEY,
    email         TEXT        NOT NULL,
    username      TEXT        NOT NULL,
    display_name  TEXT        NOT NULL,
    bio           TEXT        NOT NULL DEFAULT '',
    avatar_key    TEXT,
    password_hash TEXT        NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- The contract stores emails lowercase; enforce it here so a stray insert
    -- path cannot create a duplicate that differs only in case.
    CONSTRAINT users_email_lowercase CHECK (email = lower(email)),
    CONSTRAINT users_email_length    CHECK (char_length(email) BETWEEN 3 AND 254),
    CONSTRAINT users_username_format CHECK (username ~ '^[a-z0-9_]{3,30}$'),
    CONSTRAINT users_display_name    CHECK (char_length(display_name) BETWEEN 1 AND 50),
    CONSTRAINT users_bio_length      CHECK (char_length(bio) <= 160)
);

CREATE UNIQUE INDEX users_email_key    ON users (email);
CREATE UNIQUE INDEX users_username_key ON users (username);

-- Refresh tokens rotate. Each exchange marks the presented token used and
-- issues a successor sharing family_id, so replaying a consumed token is
-- detectable and lets us revoke the whole family at once.
CREATE TABLE refresh_tokens (
    id         TEXT        PRIMARY KEY,
    user_id    TEXT        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    family_id  TEXT        NOT NULL,
    -- SHA-256 of the token. The plaintext is never stored, so a database leak
    -- does not hand over live sessions.
    token_hash TEXT        NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    used_at    TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX refresh_tokens_hash_key   ON refresh_tokens (token_hash);
CREATE        INDEX refresh_tokens_user_idx   ON refresh_tokens (user_id);
CREATE        INDEX refresh_tokens_family_idx ON refresh_tokens (family_id);
CREATE        INDEX refresh_tokens_expiry_idx ON refresh_tokens (expires_at);

CREATE TABLE posts (
    id         TEXT        PRIMARY KEY,
    author_id  TEXT        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    image_key  TEXT        NOT NULL,
    caption    TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT posts_caption_length CHECK (char_length(caption) <= 2200)
);

-- The feed is global and chronological, newest first, and pages by keyset on
-- exactly this ordering. The composite descending index is what keeps that a
-- single index scan rather than a sort of the whole table.
CREATE INDEX posts_feed_idx   ON posts (created_at DESC, id DESC);
CREATE INDEX posts_author_idx ON posts (author_id, created_at DESC, id DESC);

-- Every presigned key is recorded so one that is never attached to a resource
-- can be reclaimed. The contract promises that happens after 24 hours.
CREATE TABLE uploads (
    key            TEXT        PRIMARY KEY,
    user_id        TEXT        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    purpose        TEXT        NOT NULL,
    content_type   TEXT        NOT NULL,
    content_length BIGINT      NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    attached_at    TIMESTAMPTZ,

    CONSTRAINT uploads_purpose CHECK (purpose IN ('avatar', 'post'))
);

-- Partial index: the janitor only ever scans unattached rows.
CREATE INDEX uploads_abandoned_idx ON uploads (created_at) WHERE attached_at IS NULL;
