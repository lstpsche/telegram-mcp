CREATE TABLE full_send_access (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    authorization_epoch TEXT NOT NULL CHECK (length(authorization_epoch) BETWEEN 22 AND 128)
) STRICT;

CREATE TRIGGER policy_full_send_inserted AFTER INSERT ON full_send_access
BEGIN UPDATE policy_revision SET revision = revision + 1 WHERE singleton = 1; END;
CREATE TRIGGER policy_full_send_updated AFTER UPDATE ON full_send_access
BEGIN UPDATE policy_revision SET revision = revision + 1 WHERE singleton = 1; END;
CREATE TRIGGER policy_full_send_deleted AFTER DELETE ON full_send_access
BEGIN UPDATE policy_revision SET revision = revision + 1 WHERE singleton = 1; END;
CREATE TRIGGER full_send_authorization_rotated AFTER UPDATE OF epoch ON authorization_state
WHEN OLD.epoch != NEW.epoch
BEGIN DELETE FROM full_send_access; END;
CREATE TRIGGER full_send_authorization_removed AFTER DELETE ON authorization_state
BEGIN DELETE FROM full_send_access; END;

-- Retained send identities must survive authorization rotation and logout.
CREATE TABLE outgoing_operations (
    operation_id TEXT PRIMARY KEY CHECK (length(operation_id) = 36),
    authorization_epoch TEXT NOT NULL CHECK (length(authorization_epoch) BETWEEN 22 AND 128),
    peer TEXT NOT NULL CHECK (length(peer) BETWEEN 1 AND 128),
    reply_to TEXT CHECK (reply_to IS NULL OR length(reply_to) BETWEEN 1 AND 128),
    payload_digest BLOB NOT NULL CHECK (length(payload_digest) = 32),
    random_id INTEGER NOT NULL UNIQUE CHECK (random_id != 0),
    state TEXT NOT NULL CHECK (state IN ('not_sent', 'in_flight', 'sent', 'uncertain')),
    message_id TEXT CHECK (message_id IS NULL OR length(message_id) BETWEEN 1 AND 128),
    sent_at TEXT,
    failure_code TEXT CHECK (failure_code IS NULL OR failure_code IN (
        'write_forbidden', 'payment_required', 'rate_limited',
        'account_restricted', 'send_effect_uncertain', 'state_sync_failed',
        'receipt_not_persisted', 'recipient_unavailable', 'invalid_reference',
        'message_too_long'
    )),
    retry_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    retain_until TEXT NOT NULL,
    CHECK ((state = 'sent' AND message_id IS NOT NULL) OR
           (state != 'sent' AND message_id IS NULL AND sent_at IS NULL)),
    CHECK (retry_at IS NULL OR failure_code = 'rate_limited')
) STRICT;
CREATE INDEX outgoing_operations_retention ON outgoing_operations(retain_until);

-- Protocol waits bind to the actual account and DC environment, not consent.
CREATE TABLE telegram_rpc_waits (
    environment TEXT NOT NULL CHECK (length(environment) BETWEEN 1 AND 32),
    account_id INTEGER NOT NULL CHECK (account_id > 0),
    method TEXT NOT NULL CHECK (length(method) BETWEEN 1 AND 64),
    scope_peer TEXT NOT NULL DEFAULT '' CHECK (length(scope_peer) <= 128),
    not_before TEXT NOT NULL,
    PRIMARY KEY (environment, account_id, method, scope_peer)
) STRICT;
