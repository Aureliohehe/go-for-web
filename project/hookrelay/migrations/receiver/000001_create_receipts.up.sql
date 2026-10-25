-- Apply ONLY to the receiver's separate database, not the HookRelay database.
-- No foreign keys to HookRelay: receiver IDs remain meaningful independently.
CREATE TABLE receiver_receipts (
    delivery_id BIGINT UNSIGNED NOT NULL PRIMARY KEY,
    payload_hash BINARY(32) NOT NULL,
    created_at DATETIME(6) NOT NULL
) ENGINE=InnoDB;

CREATE TABLE receiver_effects (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    delivery_id BIGINT UNSIGNED NOT NULL,
    payload MEDIUMBLOB NOT NULL,
    created_at DATETIME(6) NOT NULL,
    UNIQUE KEY uq_receiver_effect_delivery (delivery_id),
    CONSTRAINT fk_receiver_effect_receipt FOREIGN KEY (delivery_id)
        REFERENCES receiver_receipts(delivery_id)
) ENGINE=InnoDB;
