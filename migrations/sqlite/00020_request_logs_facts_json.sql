-- migrations/sqlite/00020_request_logs_facts_json.sql
--
-- SQLite mirror of migrations/postgres/00020_request_logs_facts_json.sql.
--
-- Adds the overflow column for reported observations that have no column of
-- their own.
--
-- Capabilities report structured observations during a request; the ones this
-- build has columns for are written to those columns. Anything else — an
-- observation from a capability this build does not have a column for, or one
-- added after this schema was written — lands here verbatim, keyed by its
-- stable name.
--
-- The alternative is dropping it, which fails silently: the row still writes,
-- just without the number, and nothing distinguishes "this did not happen" from
-- "nobody wrote a column for it". Storing it costs one text column and makes a
-- missing column a reporting gap someone can find rather than data that is
-- simply gone.
--
-- +goose Up
ALTER TABLE request_logs
    ADD COLUMN facts_json TEXT NOT NULL DEFAULT '';  -- JSON array of observations with no dedicated column; '' when every one was recognised

-- +goose Down
ALTER TABLE request_logs DROP COLUMN facts_json;
