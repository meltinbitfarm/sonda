-- 0001: apps and events (D10). No IP column (D9).
-- Timestamps are INTEGER milliseconds since the Unix epoch, UTC.

CREATE TABLE apps (
    id              INTEGER PRIMARY KEY,
    name            TEXT    NOT NULL UNIQUE,
    ingest_key_hash TEXT    NOT NULL UNIQUE, -- hex SHA-256 of the ingest key; the key itself is never stored
    created_at      INTEGER NOT NULL
) STRICT;

CREATE TABLE events (
    id           INTEGER PRIMARY KEY,
    app_id       INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    name         TEXT    NOT NULL,
    ts           INTEGER NOT NULL, -- device time, ms
    received_at  INTEGER NOT NULL, -- server time, ms
    user_id      TEXT    NOT NULL,
    session_id   TEXT    NOT NULL,
    app_version  TEXT,
    build        TEXT,
    os_version   TEXT,
    locale       TEXT,
    region       TEXT,
    storefront   TEXT,
    device_model TEXT,
    device_type  TEXT,
    props        TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(props))
) STRICT;

CREATE INDEX events_app_name_ts    ON events (app_id, name, ts);
CREATE INDEX events_app_user_ts    ON events (app_id, user_id, ts);
CREATE INDEX events_app_session_ts ON events (app_id, session_id, ts);
