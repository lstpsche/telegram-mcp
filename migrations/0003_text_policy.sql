CREATE TABLE text_grants (
    peer TEXT PRIMARY KEY,
    authorization_epoch TEXT NOT NULL CHECK (length(authorization_epoch) BETWEEN 22 AND 128),
    author TEXT NOT NULL,
    min_id INTEGER NOT NULL CHECK (min_id BETWEEN 1 AND 2147483647),
    max_id INTEGER NOT NULL CHECK (max_id BETWEEN min_id AND 2147483647),
    read_through INTEGER NOT NULL CHECK (read_through BETWEEN 0 AND 2147483647),
    profile TEXT NOT NULL CHECK (profile IN ('self-authored', 'consented')),
    expires_at TEXT NOT NULL,
    eligible INTEGER NOT NULL CHECK (eligible = 1)
) STRICT;

CREATE TABLE text_audit (
    id INTEGER PRIMARY KEY,
    request_id TEXT NOT NULL CHECK (length(request_id) BETWEEN 5 AND 128),
    operation TEXT NOT NULL CHECK (operation IN ('list_chats', 'list_messages', 'get_message_context')),
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

CREATE TRIGGER text_policy_authorization_rotated
AFTER UPDATE OF epoch ON authorization_state
WHEN OLD.epoch != NEW.epoch
BEGIN
    DELETE FROM text_grants;
END;

CREATE TRIGGER text_policy_authorization_removed
AFTER DELETE ON authorization_state
BEGIN
    DELETE FROM text_grants;
END;
