CREATE TABLE authorization_state (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    epoch TEXT NOT NULL CHECK (length(epoch) BETWEEN 16 AND 128),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

