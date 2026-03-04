INSERT INTO `users` (`user_id`, `username`, `password_hash`, `status`)
VALUES
  ('u1001', 'u1001', 'plain:123456', 1),
  ('u1002', 'u1002', 'plain:123456', 1),
  ('u1003', 'u1003', 'plain:123456', 1)
ON DUPLICATE KEY UPDATE
  `status` = VALUES(`status`),
  `updated_at` = CURRENT_TIMESTAMP;
