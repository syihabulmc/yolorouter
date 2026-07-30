-- migrations/postgres/00019_model_candidates_price_history.sql
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
--    SQL LOWER() is not the same function on both supported backends (Postgres's
--    is locale-aware, SQLite's folds ASCII only), so the same data would match
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
-- Both defaults are deliberately kept rather than dropped after the backfill.
-- Postgres deployments can be multi-instance (hence the migration advisory
-- lock), so during a rolling upgrade — and after an application-only rollback —
-- a binary that predates these columns is still creating candidates, and it
-- omits them. With no default those inserts fail the NOT NULL constraint
-- outright and the older instances cannot create candidates at all until the
-- rollout finishes. now() is also the honest value for that writer: it is
-- storing a price at that moment. The folded name defaults to empty, which
-- merely makes such a row invisible to price suggestions until it is next saved
-- — a missing convenience rather than a wrong number.
ALTER TABLE model_candidates
    ADD COLUMN price_updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE model_candidates
    ADD COLUMN provider_model_name_folded VARCHAR(200) NOT NULL DEFAULT '';

-- Runs after both ADDs so it overwrites the now() the existing rows just took.
UPDATE model_candidates
   SET price_updated_at           = updated_at,
       provider_model_name_folded = LOWER(provider_model_name);

CREATE INDEX idx_model_candidates_provider_model_price
    ON model_candidates (provider_id, provider_model_name_folded, price_updated_at);

-- +goose Down
DROP INDEX idx_model_candidates_provider_model_price;

ALTER TABLE model_candidates DROP COLUMN provider_model_name_folded;
ALTER TABLE model_candidates DROP COLUMN price_updated_at;
