SET time_zone = '+00:00';

   CREATE TABLE endpoints (
       id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
       url VARCHAR(2048) NOT NULL,
       signing_secret VARCHAR(64)
           CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
       enabled TINYINT UNSIGNED NOT NULL DEFAULT 1,
       created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),

       PRIMARY KEY (id),
       CONSTRAINT chk_endpoints_enabled CHECK (enabled IN (0, 1)),
       CONSTRAINT chk_endpoints_secret_length
           CHECK (CHAR_LENGTH(signing_secret) = 64)
   ) ENGINE=InnoDB
     DEFAULT CHARACTER SET utf8mb4
     COLLATE utf8mb4_bin;