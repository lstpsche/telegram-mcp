CREATE TABLE audit_retention (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    days INTEGER NOT NULL CHECK (days BETWEEN 1 AND 3650),
    max_records INTEGER NOT NULL CHECK (max_records BETWEEN 1 AND 1000000)
) STRICT;
INSERT INTO audit_retention VALUES (1, 30, 10000);
CREATE INDEX text_audit_recorded_at ON text_audit(recorded_at);
