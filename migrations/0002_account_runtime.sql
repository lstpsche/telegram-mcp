CREATE TABLE account_config (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    api_id INTEGER NOT NULL CHECK (api_id > 0 AND api_id <= 2147483647),
    environment TEXT NOT NULL CHECK (environment = 'test'),
    test_dc INTEGER NOT NULL CHECK (test_dc BETWEEN 1 AND 3),
    updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE authentication_checks (
    method TEXT PRIMARY KEY CHECK (method IN ('phone', 'qr')),
    test_dc INTEGER NOT NULL CHECK (test_dc BETWEEN 1 AND 3),
    authorization_epoch TEXT NOT NULL CHECK (length(authorization_epoch) BETWEEN 16 AND 128),
    passed_at TEXT NOT NULL
) STRICT;
