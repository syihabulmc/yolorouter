-- migrations/postgres/00018_model_candidate_capability_tristate.sql
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
--
-- The DEFAULT is dropped alongside NOT NULL so an INSERT that omits these
-- columns lands on NULL ("never probed") rather than a false that would read
-- as a decisive "not supported".

-- +goose Up
ALTER TABLE model_candidates ALTER COLUMN supports_streaming        DROP NOT NULL;
ALTER TABLE model_candidates ALTER COLUMN supports_function_calling DROP NOT NULL;

ALTER TABLE model_candidates ALTER COLUMN supports_streaming        DROP DEFAULT;
ALTER TABLE model_candidates ALTER COLUMN supports_function_calling DROP DEFAULT;

-- Only the false values are reset. A stored true was written by a probe that
-- actually succeeded, so it is trustworthy and worth keeping — wiping it would
-- make every already-verified candidate render as an unknown needing a retest.
UPDATE model_candidates SET supports_streaming = NULL WHERE supports_streaming = false;
UPDATE model_candidates SET supports_function_calling = NULL WHERE supports_function_calling = false;

-- +goose Down
-- Rolling back collapses the three states back onto two: NULL ("unknown")
-- becomes false, matching the pre-migration convention where anything not
-- proven supported was stored as false. The backfill must run before NOT NULL
-- is restored, or the constraint would reject the existing NULL rows.
UPDATE model_candidates SET supports_streaming = false WHERE supports_streaming IS NULL;
UPDATE model_candidates SET supports_function_calling = false WHERE supports_function_calling IS NULL;

ALTER TABLE model_candidates ALTER COLUMN supports_streaming        SET DEFAULT false;
ALTER TABLE model_candidates ALTER COLUMN supports_function_calling SET DEFAULT false;

ALTER TABLE model_candidates ALTER COLUMN supports_streaming        SET NOT NULL;
ALTER TABLE model_candidates ALTER COLUMN supports_function_calling SET NOT NULL;
