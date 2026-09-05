CREATE TABLE policy_revision (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    revision INTEGER NOT NULL CHECK (revision > 0)
) STRICT;
INSERT INTO policy_revision VALUES (1, 1);

CREATE TRIGGER policy_grant_inserted AFTER INSERT ON text_grants
BEGIN UPDATE policy_revision SET revision = revision + 1 WHERE singleton = 1; END;
CREATE TRIGGER policy_grant_updated AFTER UPDATE ON text_grants
BEGIN UPDATE policy_revision SET revision = revision + 1 WHERE singleton = 1; END;
CREATE TRIGGER policy_grant_deleted AFTER DELETE ON text_grants
BEGIN UPDATE policy_revision SET revision = revision + 1 WHERE singleton = 1; END;

ALTER TABLE text_audit RENAME TO previous_text_audit;
CREATE TABLE text_audit (
    id INTEGER PRIMARY KEY,
    request_id TEXT NOT NULL CHECK (length(request_id) BETWEEN 5 AND 128),
    operation TEXT NOT NULL CHECK (operation IN ('list_chats', 'list_messages', 'get_message_context', 'search_messages', 'list_unread')),
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
