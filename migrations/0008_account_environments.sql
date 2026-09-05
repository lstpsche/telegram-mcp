CREATE TABLE account_config_new (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    api_id INTEGER NOT NULL CHECK (api_id > 0 AND api_id <= 2147483647),
    environment TEXT NOT NULL,
    test_dc INTEGER NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK ((environment = 'test' AND test_dc BETWEEN 1 AND 3)
        OR (environment = 'production' AND test_dc = 0))
) STRICT;
INSERT INTO account_config_new SELECT * FROM account_config;
DROP TABLE account_config;
ALTER TABLE account_config_new RENAME TO account_config;

CREATE TABLE authentication_checks_new (
    method TEXT PRIMARY KEY CHECK (method IN ('phone', 'qr')),
    environment TEXT NOT NULL,
    test_dc INTEGER NOT NULL,
    authorization_epoch TEXT NOT NULL CHECK (length(authorization_epoch) BETWEEN 16 AND 128),
    passed_at TEXT NOT NULL,
    CHECK ((environment = 'test' AND test_dc BETWEEN 1 AND 3)
        OR (environment = 'production' AND test_dc = 0))
) STRICT;
INSERT INTO authentication_checks_new
    SELECT method, 'test', test_dc, authorization_epoch, passed_at FROM authentication_checks;
DROP TABLE authentication_checks;
ALTER TABLE authentication_checks_new RENAME TO authentication_checks;

-- Channel content and its independent sequence are outside supported reads.
DELETE FROM telegram_channel_state;
DELETE FROM telegram_peer_hashes WHERE kind = 'channel';
