-- migrations/sqlite/00019_model_candidates_price_history.sql
--
-- SQLite mirror of
-- migrations/postgres/00019_model_candidates_price_history.sql.
--
-- Three changes serving one query: the price auto-suggest look-up, which asks
-- for the most recently PRICED candidate of one provider + upstream model name
-- and prefills it into a new candidate.
--
-- 1. price_updated_at. updated_at cannot answer "when was this rate last
--    stated": enabling or disabling a candidate, a retest, and a capability
--    probe all bump it without touching a price column, so ordering by it lets
--    an untouched stale rate overtake a newer one. The new column moves only
--    when the price this row states actually changes — a new value, or the same
--    value newly attached to a different upstream model (see
--    repository.UpdateModelCandidate). Existing rows are backfilled from
--    updated_at, an upper bound on when their price was last stated and the
--    closest thing to the truth still recoverable.
--
-- 2. provider_model_name_folded. The look-up matches the model name
--    case-insensitively, because upstream names are quoted inconsistently
--    ("DeepSeek-V4-Pro" vs "deepseek-v4-pro") and a byte-exact match would miss
--    a provider's own negotiated price. That fold cannot live in the predicate:
--    SQL LOWER() is not the same function on both supported backends (SQLite's
--    folds ASCII only, Postgres's is locale-aware), so the same data would match
--    on one and miss on the other. Storing the folded form — written by Go, so
--    one fold everywhere — keeps the predicate a plain equality that both
--    backends index identically.
--
-- 3. An index on (provider_id, provider_model_name_folded, price_updated_at):
--    exactly the look-up's predicate plus its ordering, so it reads one row
--    instead of scanning a provider's whole catalogue on every model selection.
--    The table's other indexes are UNIQUE(model_id, provider_id) and
--    UNIQUE(model_id, sort_order), neither of which starts with provider_id.

-- +goose Up
-- SQLite only accepts a constant default on ADD COLUMN — CURRENT_TIMESTAMP is
-- rejected — so the column lands on a sentinel and is backfilled immediately.
-- The default cannot be changed afterwards without rebuilding the table, and is
-- left as is: it is only reachable by a writer that does not know the column,
-- i.e. an older binary run against an already-migrated file, where a 1970 price
-- clock costs that row future price suggestions and nothing else. SQLite
-- deployments are single-instance, so there is no rolling-upgrade window here.
ALTER TABLE model_candidates ADD COLUMN price_updated_at DATETIME NOT NULL DEFAULT '1970-01-01 00:00:00';
ALTER TABLE model_candidates ADD COLUMN provider_model_name_folded VARCHAR(200) NOT NULL DEFAULT '';

UPDATE model_candidates
   SET price_updated_at           = updated_at,
       provider_model_name_folded = LOWER(provider_model_name);

CREATE INDEX idx_model_candidates_provider_model_price
    ON model_candidates (provider_id, provider_model_name_folded, price_updated_at);

-- +goose Down
DROP INDEX idx_model_candidates_provider_model_price;

ALTER TABLE model_candidates DROP COLUMN provider_model_name_folded;
ALTER TABLE model_candidates DROP COLUMN price_updated_at;
