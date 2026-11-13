-- wecom-callback-relay 数据库初始化脚本
-- MySQL 8.0+；docker-entrypoint 会自动选择 MYSQL_DATABASE，无需在此 USE。
-- 手动执行示例：mysql -uroot -p wecom_relay < init.sql
-- 后端启动时也会通过 GORM AutoMigrate 做幂等校准。
-- 初始管理员由后端首次启动时根据 ADMIN_USERNAME / ADMIN_PASSWORD 自动创建。

CREATE TABLE IF NOT EXISTS `admin_users` (
  `id`            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `username`      VARCHAR(64)  NOT NULL,
  `password_hash` VARCHAR(255) NOT NULL,
  `nickname`      VARCHAR(64)  DEFAULT NULL,
  `role`          VARCHAR(32)  NOT NULL DEFAULT 'admin',
  `enabled`       TINYINT(1)   NOT NULL DEFAULT 1,
  `last_login_at` DATETIME(3)  DEFAULT NULL,
  `created_at`    DATETIME(3)  DEFAULT NULL,
  `updated_at`    DATETIME(3)  DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_admin_users_username` (`username`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `consumers` (
  `id`                       BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `name`                     VARCHAR(100) NOT NULL,
  `callback_url`             VARCHAR(500) NOT NULL,
  `forward_mode`             VARCHAR(20)  NOT NULL DEFAULT 'json', -- json | wecom_xml | wecom_proxy
  `auth_type`                VARCHAR(20)  NOT NULL DEFAULT 'none',
  `auth_config`              TEXT         DEFAULT NULL,
  `wecom_token`              VARCHAR(512)  DEFAULT NULL,
  `wecom_encoding_aes_key`   VARCHAR(1024) DEFAULT NULL,
  `wecom_corp_id`            VARCHAR(128) DEFAULT NULL,
  `wecom_agent_id`           VARCHAR(64)  DEFAULT NULL,
  `enabled`                  TINYINT(1)   NOT NULL DEFAULT 1,
  `timeout_ms`               BIGINT       NOT NULL DEFAULT 5000,
  `max_retry`                BIGINT       NOT NULL DEFAULT 5,
  `remark`                   VARCHAR(255) DEFAULT NULL,
  `created_by`               BIGINT UNSIGNED DEFAULT NULL,
  `created_at`               DATETIME(3)  DEFAULT NULL,
  `updated_at`               DATETIME(3)  DEFAULT NULL,
  `deleted_at`               DATETIME(3)  DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_consumers_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `wecom_config` (
  `id`               BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `corp_id`          VARCHAR(128)  DEFAULT NULL,
  `secret`           VARCHAR(1024) NOT NULL,
  `token`            VARCHAR(512)  NOT NULL,
  `encoding_aes_key` VARCHAR(1024) NOT NULL,
  `created_at`       DATETIME(3)   DEFAULT NULL,
  `updated_at`       DATETIME(3)   DEFAULT NULL,
  `deleted_at`       DATETIME(3)   DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_wecom_config_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `relay_tasks` (
  `id`            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `event_id`      VARCHAR(100) NOT NULL,
  `consumer_id`   BIGINT UNSIGNED NOT NULL,
  `consumer_name` VARCHAR(100) DEFAULT NULL,
  `callback_url`  VARCHAR(500) DEFAULT NULL,
  `forward_mode`  VARCHAR(20)  NOT NULL,
  `agent_id`      VARCHAR(64)  DEFAULT NULL,
  `raw_xml`       MEDIUMTEXT   DEFAULT NULL,
  `raw_encrypted_body` MEDIUMTEXT DEFAULT NULL,
  `inbound_query` TEXT         DEFAULT NULL,
  `json_payload`  LONGTEXT     DEFAULT NULL,
  `status`        VARCHAR(20)  NOT NULL DEFAULT 'pending',
  `retry_count`   BIGINT       NOT NULL DEFAULT 0,
  `max_retry`     BIGINT       NOT NULL DEFAULT 5,
  `next_retry_at` DATETIME(3)  DEFAULT NULL,
  `last_error`    TEXT         DEFAULT NULL,
  `last_http_status` BIGINT    NOT NULL DEFAULT 0,
  `last_latency_ms`  BIGINT    NOT NULL DEFAULT 0,
  `created_at`    DATETIME(3)  DEFAULT NULL,
  `updated_at`    DATETIME(3)  DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_event_consumer` (`event_id`, `consumer_id`),
  KEY `idx_status_retry` (`status`, `next_retry_at`),
  KEY `idx_status_updated` (`status`, `updated_at`),
  KEY `idx_relay_tasks_consumer_id` (`consumer_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `dead_letters` (
  `id`                      BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `event_id`                VARCHAR(100) NOT NULL,
  `consumer_id`             BIGINT UNSIGNED NOT NULL,
  `consumer_name`           VARCHAR(100) DEFAULT NULL,
  `forward_mode`            VARCHAR(20)  NOT NULL,
  `agent_id`                VARCHAR(64)  DEFAULT NULL,
  `callback_url`            VARCHAR(500) DEFAULT NULL,
  `raw_xml`                 MEDIUMTEXT   DEFAULT NULL,
  `raw_encrypted_body`      MEDIUMTEXT   DEFAULT NULL,
  `inbound_query`           TEXT         DEFAULT NULL,
  `json_payload`            LONGTEXT     DEFAULT NULL,
  `auth_type`               VARCHAR(20)  DEFAULT NULL,
  `auth_config`             TEXT         DEFAULT NULL,
  `wecom_token_snapshot`    VARCHAR(512)  DEFAULT NULL,
  `wecom_aes_key_snapshot`  VARCHAR(1024) DEFAULT NULL,
  `wecom_corp_id`           VARCHAR(128) DEFAULT NULL,
  `timeout_ms`              BIGINT       NOT NULL DEFAULT 5000,
  `last_error`              TEXT         DEFAULT NULL,
  `retry_count`             BIGINT       NOT NULL DEFAULT 0,
  `status`                  VARCHAR(20)  NOT NULL DEFAULT 'pending',
  `created_at`              DATETIME(3)  DEFAULT NULL,
  `resolved_at`             DATETIME(3)  DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_dead_letters_event_id` (`event_id`),
  KEY `idx_dead_letters_consumer_id` (`consumer_id`),
  KEY `idx_dead_letters_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `audit_logs` (
  `id`          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `admin_id`    BIGINT UNSIGNED DEFAULT NULL,
  `admin_name`  VARCHAR(64) DEFAULT NULL,
  `action`      VARCHAR(64) DEFAULT NULL,
  `target_type` VARCHAR(64) DEFAULT NULL,
  `target_id`   BIGINT UNSIGNED DEFAULT NULL,
  `detail`      TEXT        DEFAULT NULL,
  `result`      VARCHAR(32) DEFAULT NULL,
  `created_at`  DATETIME(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_audit_logs_admin_id` (`admin_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
