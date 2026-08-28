-- schema fixture for SQ0
CREATE TABLE users (
  id INT PRIMARY KEY,
  name VARCHAR(255) NOT NULL
);

CREATE TABLE orders (
  id INT PRIMARY KEY,
  user_id INT
);

ALTER TABLE users ADD COLUMN age INT;
ALTER TABLE users DROP COLUMN age;

CREATE VIEW active_users AS SELECT * FROM users WHERE id > 0;

CREATE INDEX idx_users_name ON users(name);

DROP TABLE orders;
