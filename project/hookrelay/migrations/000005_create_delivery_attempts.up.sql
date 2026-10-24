-- Apply once after 000004, with the old Worker stopped.
-- Previous attempts cannot be reconstructed: only new executions are recorded.
CREATE TABLE delivery_attempts (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    delivery_id BIGINT UNSIGNED NOT NULL,
    generation INT UNSIGNED NOT NULL,
    attempt_number INT UNSIGNED NOT NULL,
    status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'processing',
    started_at DATETIME(6) NOT NULL,
    finished_at DATETIME(6) NULL,
    http_status SMALLINT UNSIGNED NULL,
    error_class VARCHAR(64) NOT NULL DEFAULT '',
    latency_ms BIGINT UNSIGNED NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uq_attempt_execution (delivery_id, generation, attempt_number),
    KEY idx_attempt_page (delivery_id, id),
    CONSTRAINT fk_attempt_delivery FOREIGN KEY (delivery_id)
        REFERENCES deliveries (id) ON DELETE RESTRICT,
    CONSTRAINT chk_attempt_status CHECK (status IN ('processing', 'succeeded', 'retrying', 'failed')),
    CONSTRAINT chk_attempt_generation CHECK (generation > 0),
    CONSTRAINT chk_attempt_number CHECK (attempt_number > 0)
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;
