-- Apply once, after 000005. Stop old Workers before enabling the Reconciler.
-- Existing rows use their deterministic initial queue task ID until deferred.
ALTER TABLE deliveries
    ADD COLUMN queue_task_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
    ADD COLUMN recovery_count INT UNSIGNED NOT NULL DEFAULT 0,
    ADD KEY idx_deliveries_status_updated (status, updated_at);
