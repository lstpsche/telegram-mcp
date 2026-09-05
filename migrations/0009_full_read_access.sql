CREATE TABLE full_read_access (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    authorization_epoch TEXT NOT NULL CHECK (length(authorization_epoch) BETWEEN 22 AND 128)
) STRICT;

CREATE TRIGGER policy_full_read_inserted AFTER INSERT ON full_read_access
BEGIN UPDATE policy_revision SET revision = revision + 1 WHERE singleton = 1; END;
CREATE TRIGGER policy_full_read_updated AFTER UPDATE ON full_read_access
BEGIN UPDATE policy_revision SET revision = revision + 1 WHERE singleton = 1; END;
CREATE TRIGGER policy_full_read_deleted AFTER DELETE ON full_read_access
BEGIN UPDATE policy_revision SET revision = revision + 1 WHERE singleton = 1; END;
CREATE TRIGGER full_read_authorization_rotated AFTER UPDATE OF epoch ON authorization_state
WHEN OLD.epoch != NEW.epoch
BEGIN DELETE FROM full_read_access; END;
CREATE TRIGGER full_read_authorization_removed AFTER DELETE ON authorization_state
BEGIN DELETE FROM full_read_access; END;
