CREATE TABLE telegram_update_state (
    epoch TEXT NOT NULL,
    user_id INTEGER NOT NULL CHECK (user_id > 0),
    pts INTEGER NOT NULL CHECK (pts >= 0),
    qts INTEGER NOT NULL CHECK (qts >= 0),
    date INTEGER NOT NULL CHECK (date >= 0),
    seq INTEGER NOT NULL CHECK (seq >= 0),
    PRIMARY KEY (epoch, user_id)
) STRICT;

CREATE TABLE telegram_peer_hashes (
    epoch TEXT NOT NULL,
    user_id INTEGER NOT NULL CHECK (user_id > 0),
    kind TEXT NOT NULL CHECK (kind IN ('user', 'channel')),
    peer_id INTEGER NOT NULL CHECK (peer_id > 0),
    access_hash INTEGER NOT NULL CHECK (access_hash <> 0),
    PRIMARY KEY (epoch, user_id, kind, peer_id)
) STRICT;

CREATE TABLE telegram_channel_state (
    epoch TEXT NOT NULL,
    user_id INTEGER NOT NULL CHECK (user_id > 0),
    channel_id INTEGER NOT NULL CHECK (channel_id > 0),
    pts INTEGER NOT NULL CHECK (pts >= 0),
    PRIMARY KEY (epoch, user_id, channel_id)
) STRICT;

CREATE TRIGGER telegram_metadata_epoch_rotation AFTER UPDATE OF epoch ON authorization_state
WHEN OLD.epoch <> NEW.epoch
BEGIN
    DELETE FROM telegram_peer_hashes;
    DELETE FROM telegram_update_state;
    DELETE FROM telegram_channel_state;
END;

CREATE TRIGGER telegram_metadata_epoch_deletion AFTER DELETE ON authorization_state
BEGIN
    DELETE FROM telegram_peer_hashes;
    DELETE FROM telegram_update_state;
    DELETE FROM telegram_channel_state;
END;
