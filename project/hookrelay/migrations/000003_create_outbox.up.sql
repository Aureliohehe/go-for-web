-- Stop the old API and Worker before applying once, after 000002.
-- DDL is not transactional: if application fails, inspect before rerunning.
SET time_zone = '+00:00';

CREATE TABLE outbox (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    delivery_id BIGINT UNSIGNED NOT NULL,
    generation INT UNSIGNED NOT NULL,
    status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'pending',
    available_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    lease_until DATETIME(6) NULL,
    publish_attempts INT UNSIGNED NOT NULL DEFAULT 0,
    last_error VARCHAR(255) NOT NULL DEFAULT '',
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uq_outbox_delivery_generation (delivery_id, generation),
    KEY idx_outbox_poll (status, available_at, id),
    CONSTRAINT fk_outbox_delivery FOREIGN KEY (delivery_id)
        REFERENCES deliveries (id) ON DELETE RESTRICT,
    CONSTRAINT chk_outbox_status CHECK (status IN ('pending', 'processing', 'published')),
    CONSTRAINT chk_outbox_generation CHECK (generation > 0)
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;

-- Existing nonterminal deliveries also need a publication record. Duplicates
-- of tasks already in Redis are safe to enqueue with their stable task ID.
INSERT INTO outbox (delivery_id, generation)
SELECT id, generation FROM deliveries
WHERE status IN ('pending', 'processing', 'retrying');
