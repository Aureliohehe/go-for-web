-- Apply once, after 000001_create_endpoints.up.sql.
-- Session time zone agrees with the UTC convention used by the Go service.
SET time_zone = '+00:00';

CREATE TABLE events (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    -- VARBINARY keeps idempotency keys byte-exact (including trailing spaces).
    idempotency_key VARBINARY(128) NOT NULL,
    request_hash BINARY(32) NOT NULL,
    endpoint_id BIGINT UNSIGNED NOT NULL,
    event_type VARCHAR(128) NOT NULL,
    payload MEDIUMBLOB NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),

    PRIMARY KEY (id),
    UNIQUE KEY uq_events_idempotency_key (idempotency_key),
    KEY idx_events_endpoint_id (endpoint_id),
    CONSTRAINT fk_events_endpoint FOREIGN KEY (endpoint_id)
        REFERENCES endpoints (id) ON DELETE RESTRICT,
    CONSTRAINT chk_events_idempotency_key_length CHECK (OCTET_LENGTH(idempotency_key) > 0),
    CONSTRAINT chk_events_event_type_length CHECK (CHAR_LENGTH(event_type) > 0)
) ENGINE=InnoDB
  DEFAULT CHARACTER SET utf8mb4
  COLLATE utf8mb4_bin;

CREATE TABLE deliveries (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    event_id BIGINT UNSIGNED NOT NULL,
    status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'pending',
    generation INT UNSIGNED NOT NULL DEFAULT 1,
    attempt_count INT UNSIGNED NOT NULL DEFAULT 0,
    last_http_status SMALLINT UNSIGNED NULL,
    last_error VARCHAR(255) NULL,
    lease_until DATETIME(6) NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),

    PRIMARY KEY (id),
    UNIQUE KEY uq_deliveries_event_id (event_id),
    KEY idx_deliveries_status_lease (status, lease_until),
    CONSTRAINT fk_deliveries_event FOREIGN KEY (event_id)
        REFERENCES events (id) ON DELETE RESTRICT,
    CONSTRAINT chk_deliveries_status CHECK (
        status IN ('pending', 'processing', 'retrying', 'succeeded', 'failed')
    ),
    CONSTRAINT chk_deliveries_generation CHECK (generation > 0)
) ENGINE=InnoDB
  DEFAULT CHARACTER SET utf8mb4
  COLLATE utf8mb4_bin;
