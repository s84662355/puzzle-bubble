-- MySQL 8.0+
CREATE TABLE IF NOT EXISTS `users` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT 'surrogate key',
  `user_id` VARCHAR(64) NOT NULL COMMENT 'business user id',
  `username` VARCHAR(64) NOT NULL COMMENT 'login name',
  `password_hash` VARCHAR(255) NOT NULL COMMENT 'password hash, e.g. bcrypt/argon2',
  `password_salt` VARCHAR(64) DEFAULT NULL COMMENT 'optional extra salt',
  `status` TINYINT NOT NULL DEFAULT 1 COMMENT '1=active,2=disabled,3=locked',
  `last_login_at` DATETIME DEFAULT NULL,
  `last_login_ip` VARCHAR(45) DEFAULT NULL,
  `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at` DATETIME DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_user_id` (`user_id`),
  UNIQUE KEY `uk_username` (`username`),
  KEY `idx_status` (`status`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
