-- migrations/postgres/00026_fold_owner_label_into_remark.sql
-- Retire the free-text api_keys.owner_label. It predates real account
-- ownership: it existed so a single admin could note who a key was handed
-- to. With per-account keys every row carries user_id, and the owning
-- account's username is what every view now displays.
--
-- The data is folded into remark rather than discarded — on upgraded
-- instances the labels are the only record of who legacy keys were handed
-- to, and the pre-multi-user contract promised they survive as a remark-
-- style tag. The column itself stays for now (no code reads or writes it
-- anymore): dropping it in the same release would break still-running old
-- binaries in a multi-instance Postgres rollout, which still SELECT and
-- INSERT the column. A later release drops it once old binaries are
-- retired (expand/contract); that contract migration must re-run this
-- same fold first, because old binaries may keep writing labels during
-- the rollout window after this one-time fold ran.
--
-- The fold empties owner_label in the same statement, which makes it
-- idempotent: a rollback (whose Down cannot un-merge free text) followed
-- by re-apply must not concatenate the same label twice. The result must
-- fit remark's 200-char limit — remark VARCHAR(200) plus a 50-char label
-- can exceed it, and an oversized value would abort the migration here
-- and trap the key beyond the API's edit validation. The EXISTING remark
-- is what gets trimmed to make room: the label is the data this fold
-- exists to preserve, so truncating the label away would defeat it. The
-- outer LEFT is a belt-and-braces cap for labels that are themselves
-- oversized (GREATEST keeps the reserve non-negative for them).
--
-- Also repairs request_logs attribution: the multi-user backfill assigned
-- EVERY historical row to the local admin account, including auth-rejected
-- rows that never resolved a key. Those rows have no owner and must not
-- display as admin traffic.

-- +goose Up
UPDATE api_keys
SET remark = LEFT(
        CASE
            WHEN remark = '' THEN owner_label
            ELSE LEFT(remark, GREATEST(200 - 3 - LENGTH(owner_label), 0)) || ' · ' || owner_label
        END, 200),
    owner_label = ''
WHERE owner_label <> '';

UPDATE request_logs SET user_id = NULL WHERE api_key_id IS NULL;

-- +goose Down
-- The fold is not reversible (labels are merged into free text) and the
-- attribution repair restores intended semantics; nothing to undo. Up is
-- idempotent (it empties owner_label), so rollback + re-apply is safe.
SELECT 1;
