-- migrations/sqlite/00018_model_candidate_capability_tristate.sql
--
-- SQLite mirror of
-- migrations/postgres/00018_model_candidate_capability_tristate.sql.
--
-- supports_streaming / supports_function_calling become nullable so they can
-- express three states instead of two: true (probe passed), false (probe
-- returned a decisive "not supported"), NULL (never probed, or the probe was
-- inconclusive — rate limited, unreachable, upstream error). Previously any
-- non-success outcome was written as false, and because the gateway filters
-- routable candidates on these flags, a single upstream 429 during a
-- capability probe could permanently stop a perfectly healthy candidate from
-- serving streaming or tool-calling traffic until someone retested it by hand.
--
-- Every existing false is reset to NULL: a stored false cannot be told apart
-- from a probe that was misclassified under the old write rule, so none of
-- them are trustworthy. Combined with the gateway treating NULL as "allow",
-- this restores routing for candidates that were silently demoted, at the
-- cost of letting a genuinely unsupported candidate be tried once more.
-- A stored true is kept: it was written by a probe that actually succeeded, and
-- discarding it would make every already-verified candidate demand a retest.
--
-- SQLite cannot drop a NOT NULL constraint in place, and DROP COLUMN discards
-- the values, so each column's contents are parked in a temporary nullable
-- column first and renamed back afterwards. This avoids a full table rebuild,
-- which would mean reproducing the table's foreign keys and both UNIQUE
-- constraints by hand. Column ordering changes (the renamed columns move to the
-- end), which is harmless — every query addresses these columns by name.

-- +goose Up
ALTER TABLE model_candidates ADD COLUMN supports_streaming_next        BOOLEAN NULL;
ALTER TABLE model_candidates ADD COLUMN supports_function_calling_next BOOLEAN NULL;

UPDATE model_candidates SET
    supports_streaming_next        = CASE WHEN supports_streaming = 1 THEN 1 ELSE NULL END,
    supports_function_calling_next = CASE WHEN supports_function_calling = 1 THEN 1 ELSE NULL END;

ALTER TABLE model_candidates DROP COLUMN supports_streaming;
ALTER TABLE model_candidates DROP COLUMN supports_function_calling;

ALTER TABLE model_candidates RENAME COLUMN supports_streaming_next        TO supports_streaming;
ALTER TABLE model_candidates RENAME COLUMN supports_function_calling_next TO supports_function_calling;

-- +goose Down
-- Rolling back collapses the three states back onto two: NULL ("unknown")
-- becomes false, matching the pre-migration convention where anything not
-- proven supported was stored as false. Every proven true is carried over.
--
-- The values have to be parked in a temporary column first. Dropping and
-- re-adding in place, as the Up does, would discard them — and because the
-- restored pre-migration gateway excludes false from streaming and tool-calling
-- rotation, that would drop streaming and tool traffic for EVERY candidate,
-- including the ones whose support was actually proven, until each was retested
-- by hand. This mirrors the Postgres Down, which preserves them via UPDATE.
ALTER TABLE model_candidates ADD COLUMN supports_streaming_prev        BOOLEAN NOT NULL DEFAULT 0;
ALTER TABLE model_candidates ADD COLUMN supports_function_calling_prev BOOLEAN NOT NULL DEFAULT 0;

UPDATE model_candidates SET
    supports_streaming_prev        = COALESCE(supports_streaming, 0),
    supports_function_calling_prev = COALESCE(supports_function_calling, 0);

ALTER TABLE model_candidates DROP COLUMN supports_streaming;
ALTER TABLE model_candidates DROP COLUMN supports_function_calling;

ALTER TABLE model_candidates RENAME COLUMN supports_streaming_prev        TO supports_streaming;
ALTER TABLE model_candidates RENAME COLUMN supports_function_calling_prev TO supports_function_calling;
