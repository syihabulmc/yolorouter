-- migrations/postgres/00013_request_logs_cache_savings.sql
--
-- Cache economics columns for request_logs. See the sqlite twin for the
-- column semantics (read-side saving vs write-side premium, both floored at 0,
-- net derived by the reader; history stays 0/0).

-- +goose Up
ALTER TABLE request_logs ADD COLUMN cache_read_saved_micros BIGINT NOT NULL DEFAULT 0;
ALTER TABLE request_logs ADD COLUMN cache_write_extra_micros BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE request_logs DROP COLUMN IF EXISTS cache_write_extra_micros;
ALTER TABLE request_logs DROP COLUMN IF EXISTS cache_read_saved_micros;
