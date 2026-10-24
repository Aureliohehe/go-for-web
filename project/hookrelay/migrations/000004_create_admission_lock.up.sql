-- Apply once after 000003. All API instances must use the same backlog limit.
-- This singleton serializes capacity checks and event creation; it is not a counter.
CREATE TABLE admission_locks (
    id TINYINT UNSIGNED NOT NULL,
    PRIMARY KEY (id),
    CONSTRAINT chk_admission_singleton CHECK (id = 1)
) ENGINE=InnoDB;

INSERT INTO admission_locks (id) VALUES (1);
