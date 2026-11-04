-- 0002: client-generated event_id (UUID, lowercase) for idempotent retries.
-- Nullable only because SQLite cannot ADD COLUMN NOT NULL without a default;
-- the ingest path always sets it. NULLs are distinct in a UNIQUE index, so
-- rows written before this migration do not conflict.
ALTER TABLE events ADD COLUMN event_id TEXT;
CREATE UNIQUE INDEX events_app_event_id ON events (app_id, event_id);
