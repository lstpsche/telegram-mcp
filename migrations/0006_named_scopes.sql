CREATE TABLE named_scopes (
    id TEXT PRIMARY KEY CHECK (length(id) = 43 AND substr(id, 1, 11) = 'tgscope:v1:' AND substr(id, 12) NOT GLOB '*[^0-9a-f]*'),
    name TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 32 AND substr(name, 1, 1) GLOB '[a-z]' AND name NOT GLOB '*[^a-z0-9_-]*'),
    authorization_epoch TEXT NOT NULL CHECK (length(authorization_epoch) BETWEEN 22 AND 128)
) STRICT;

CREATE TABLE named_scope_peers (
    scope_id TEXT NOT NULL REFERENCES named_scopes(id) ON DELETE CASCADE,
    peer TEXT NOT NULL,
    PRIMARY KEY (scope_id, peer)
) STRICT;

CREATE TRIGGER policy_scope_inserted AFTER INSERT ON named_scopes
BEGIN UPDATE policy_revision SET revision = revision + 1 WHERE singleton = 1; END;
CREATE TRIGGER policy_scope_updated AFTER UPDATE ON named_scopes
BEGIN UPDATE policy_revision SET revision = revision + 1 WHERE singleton = 1; END;
CREATE TRIGGER policy_scope_deleted AFTER DELETE ON named_scopes
BEGIN UPDATE policy_revision SET revision = revision + 1 WHERE singleton = 1; END;
CREATE TRIGGER policy_scope_peer_inserted AFTER INSERT ON named_scope_peers
BEGIN UPDATE policy_revision SET revision = revision + 1 WHERE singleton = 1; END;
CREATE TRIGGER policy_scope_peer_updated AFTER UPDATE ON named_scope_peers
BEGIN UPDATE policy_revision SET revision = revision + 1 WHERE singleton = 1; END;
CREATE TRIGGER policy_scope_peer_deleted AFTER DELETE ON named_scope_peers
BEGIN UPDATE policy_revision SET revision = revision + 1 WHERE singleton = 1; END;

CREATE TRIGGER scope_authorization_rotated
AFTER UPDATE OF epoch ON authorization_state
WHEN OLD.epoch != NEW.epoch
BEGIN DELETE FROM named_scopes; END;
CREATE TRIGGER scope_authorization_removed
AFTER DELETE ON authorization_state
BEGIN DELETE FROM named_scopes; END;

ALTER TABLE text_audit RENAME TO previous_text_audit;
CREATE TABLE text_audit (
    id INTEGER PRIMARY KEY,
    request_id TEXT NOT NULL CHECK (length(request_id) BETWEEN 5 AND 128),
    operation TEXT NOT NULL CHECK (operation IN ('list_chats', 'list_messages', 'get_message_context', 'search_messages', 'list_unread', 'list_scopes')),
    outcome TEXT NOT NULL CHECK (outcome IN ('success', 'failure')),
    category TEXT NOT NULL CHECK (category IN (
        '', 'invalid_input', 'not_ready', 'reauth_required', 'policy_denied',
        'consent_required', 'unsupported_peer', 'protected_content', 'ephemeral_content',
        'invalid_reference', 'cursor_invalid', 'cursor_expired', 'resource_expired',
        'rate_limited', 'freshness_degraded', 'partial_result', 'telegram_unavailable',
        'result_too_large', 'media_too_large', 'cancelled', 'internal', 'read_effect_uncertain'
    )),
    item_count INTEGER NOT NULL CHECK (item_count BETWEEN 0 AND 100),
    uncertain INTEGER NOT NULL CHECK (uncertain IN (0, 1)),
    recorded_at TEXT NOT NULL,
    CHECK ((outcome = 'success' AND category = '' AND uncertain = 0) OR
           (outcome = 'failure' AND category != '' AND item_count = 0))
) STRICT;

INSERT INTO text_audit SELECT * FROM previous_text_audit;
DROP TABLE previous_text_audit;
