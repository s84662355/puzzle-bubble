CREATE TABLE IF NOT EXISTS `users` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT 'surrogate key',
  `user_id` VARCHAR(64) NOT NULL COMMENT 'business user id',
  `username` VARCHAR(64) NOT NULL COMMENT 'login name',
  `password_hash` VARCHAR(255) NOT NULL COMMENT 'password hash',
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

CREATE TABLE IF NOT EXISTS `player_roles` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id` VARCHAR(64) NOT NULL,
  `role_id` VARCHAR(64) NOT NULL,
  `nickname` VARCHAR(64) NOT NULL,
  `avatar` VARCHAR(255) DEFAULT NULL,
  `level` INT NOT NULL DEFAULT 1,
  `exp` BIGINT NOT NULL DEFAULT 0,
  `status` TINYINT NOT NULL DEFAULT 1 COMMENT '1=normal,2=banned',
  `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_role_id` (`role_id`),
  KEY `idx_user_id` (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS `player_assets` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `role_id` VARCHAR(64) NOT NULL,
  `gold` BIGINT NOT NULL DEFAULT 0,
  `diamond` BIGINT NOT NULL DEFAULT 0,
  `ticket` INT NOT NULL DEFAULT 0,
  `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_role_assets` (`role_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS `player_stats` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `role_id` VARCHAR(64) NOT NULL,
  `mode` VARCHAR(32) NOT NULL DEFAULT 'rank',
  `total_games` INT NOT NULL DEFAULT 0,
  `wins` INT NOT NULL DEFAULT 0,
  `losses` INT NOT NULL DEFAULT 0,
  `draws` INT NOT NULL DEFAULT 0,
  `max_streak` INT NOT NULL DEFAULT 0,
  `rank_score` INT NOT NULL DEFAULT 1000,
  `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_role_mode` (`role_id`, `mode`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS `match_records` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `match_id` VARCHAR(64) NOT NULL,
  `room_id` VARCHAR(64) NOT NULL,
  `mode` VARCHAR(32) NOT NULL,
  `status` TINYINT NOT NULL DEFAULT 1 COMMENT '1=created,2=playing,3=finished,4=aborted',
  `started_at` DATETIME DEFAULT NULL,
  `ended_at` DATETIME DEFAULT NULL,
  `result_json` JSON DEFAULT NULL,
  `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_match_id` (`match_id`),
  KEY `idx_room_id` (`room_id`),
  KEY `idx_mode_status` (`mode`, `status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS `orders` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `order_id` VARCHAR(64) NOT NULL,
  `user_id` VARCHAR(64) NOT NULL,
  `role_id` VARCHAR(64) DEFAULT NULL,
  `product_id` VARCHAR(64) NOT NULL,
  `amount` DECIMAL(12,2) NOT NULL DEFAULT 0.00,
  `currency` VARCHAR(8) NOT NULL DEFAULT 'CNY',
  `status` TINYINT NOT NULL DEFAULT 1 COMMENT '1=created,2=paid,3=failed,4=refunded',
  `channel` VARCHAR(32) DEFAULT NULL,
  `third_party_txn` VARCHAR(128) DEFAULT NULL,
  `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_order_id` (`order_id`),
  KEY `idx_user_id` (`user_id`),
  KEY `idx_role_id` (`role_id`),
  KEY `idx_status_created` (`status`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
