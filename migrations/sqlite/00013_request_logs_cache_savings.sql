-- migrations/sqlite/00013_request_logs_cache_savings.sql
--
-- Cache economics columns for request_logs, stored per-request at write time
-- (like cost_micros) because the counterfactual depends on the candidate's
-- prices at the moment of the request, which are not preserved per-row later.
--
-- cache_read_saved_micros:  how much cheaper the cache-read tokens were than
--                           reprocessing them at the input price
--                           = cache_read * (input_price - cache_read_price), floored at 0.
-- cache_write_extra_micros: the premium paid to establish the cache, i.e. how
--                           much MORE cache-write tokens cost than the input price
--                           = cache_write * (cache_write_price - input_price), floored at 0.
--
-- Net cache savings = cache_read_saved_micros - cache_write_extra_micros,
-- derived by the reader. Both are non-negative so each side reads on its own.
-- Rows written before this migration keep 0/0 (history cannot be backfilled),
-- so the cumulative figure is only exact from this point forward.
--
-- +goose Up
ALTER TABLE request_logs ADD COLUMN cache_read_saved_micros INTEGER NOT NULL DEFAULT 0;
ALTER TABLE request_logs ADD COLUMN cache_write_extra_micros INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE request_logs DROP COLUMN cache_write_extra_micros;
ALTER TABLE request_logs DROP COLUMN cache_read_saved_micros;
