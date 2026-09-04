package telegram

import (
	"context"
	"database/sql"
	"errors"

	"github.com/gotd/td/telegram/updates"
)

// readStorage contains only authorization-bound access hashes and checkpoints.
// A failed write is latched because gotd reports some storage errors through its
// logger and otherwise continues with an in-memory sequence.
type readStorage struct {
	db      *sql.DB
	epoch   string
	runtime *readRuntime
}

func (s *readStorage) capture(err error) error {
	if err != nil {
		s.runtime.fail(err)
	}
	return err
}

func (s *readStorage) checkEpoch(ctx context.Context) error {
	var epoch string
	if err := s.db.QueryRowContext(ctx, `SELECT epoch FROM authorization_state WHERE singleton = 1`).Scan(&epoch); err != nil {
		return s.capture(err)
	}
	if epoch != s.epoch {
		return s.capture(errors.New("Telegram metadata authorization changed"))
	}
	return nil
}

func (s *readStorage) exec(ctx context.Context, query string, args ...any) error {
	if err := s.checkEpoch(ctx); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return s.capture(err)
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		err = errors.New("Telegram checkpoint row is missing")
	}
	return s.capture(err)
}

func (s *readStorage) GetState(ctx context.Context, user int64) (updates.State, bool, error) {
	if err := s.checkEpoch(ctx); err != nil {
		return updates.State{}, false, err
	}
	var state updates.State
	err := s.db.QueryRowContext(ctx, `SELECT pts,qts,date,seq FROM telegram_update_state WHERE epoch=? AND user_id=?`, s.epoch, user).Scan(&state.Pts, &state.Qts, &state.Date, &state.Seq)
	if errors.Is(err, sql.ErrNoRows) {
		return updates.State{}, false, nil
	}
	return state, err == nil, s.capture(err)
}
func (s *readStorage) SetState(ctx context.Context, user int64, state updates.State) error {
	return s.exec(ctx, `INSERT INTO telegram_update_state(epoch,user_id,pts,qts,date,seq) SELECT ?,?,?,?,?,? WHERE EXISTS(SELECT 1 FROM authorization_state WHERE epoch=?) ON CONFLICT(epoch,user_id) DO UPDATE SET pts=excluded.pts,qts=excluded.qts,date=excluded.date,seq=excluded.seq`, s.epoch, user, state.Pts, state.Qts, state.Date, state.Seq, s.epoch)
}
func (s *readStorage) SetPts(ctx context.Context, user int64, value int) error {
	return s.exec(ctx, `UPDATE telegram_update_state SET pts=? WHERE epoch=? AND user_id=?`, value, s.epoch, user)
}
func (s *readStorage) SetQts(ctx context.Context, user int64, value int) error {
	return s.exec(ctx, `UPDATE telegram_update_state SET qts=? WHERE epoch=? AND user_id=?`, value, s.epoch, user)
}
func (s *readStorage) SetDate(ctx context.Context, user int64, value int) error {
	return s.exec(ctx, `UPDATE telegram_update_state SET date=? WHERE epoch=? AND user_id=?`, value, s.epoch, user)
}
func (s *readStorage) SetSeq(ctx context.Context, user int64, value int) error {
	return s.exec(ctx, `UPDATE telegram_update_state SET seq=? WHERE epoch=? AND user_id=?`, value, s.epoch, user)
}
func (s *readStorage) SetDateSeq(ctx context.Context, user int64, date, seq int) error {
	return s.exec(ctx, `UPDATE telegram_update_state SET date=?,seq=? WHERE epoch=? AND user_id=?`, date, seq, s.epoch, user)
}
func (s *readStorage) GetChannelPts(ctx context.Context, user, channel int64) (int, bool, error) {
	if err := s.checkEpoch(ctx); err != nil {
		return 0, false, err
	}
	var pts int
	err := s.db.QueryRowContext(ctx, `SELECT pts FROM telegram_channel_state WHERE epoch=? AND user_id=? AND channel_id=?`, s.epoch, user, channel).Scan(&pts)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return pts, err == nil, s.capture(err)
}
func (s *readStorage) SetChannelPts(ctx context.Context, user, channel int64, pts int) error {
	return s.exec(ctx, `INSERT INTO telegram_channel_state(epoch,user_id,channel_id,pts) SELECT ?,?,?,? WHERE EXISTS(SELECT 1 FROM authorization_state WHERE epoch=?) ON CONFLICT(epoch,user_id,channel_id) DO UPDATE SET pts=excluded.pts`, s.epoch, user, channel, pts, s.epoch)
}
func (s *readStorage) ForEachChannels(ctx context.Context, user int64, f func(context.Context, int64, int) error) error {
	if err := s.checkEpoch(ctx); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT channel_id,pts FROM telegram_channel_state WHERE epoch=? AND user_id=?`, s.epoch, user)
	if err != nil {
		return s.capture(err)
	}
	// Close rows before callbacks: the metadata database may use one connection.
	type channelState struct {
		id  int64
		pts int
	}
	var states []channelState
	for rows.Next() {
		var state channelState
		if err := rows.Scan(&state.id, &state.pts); err != nil {
			rows.Close()
			return s.capture(err)
		}
		states = append(states, state)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return s.capture(err)
	}
	for _, state := range states {
		if err := f(ctx, state.id, state.pts); err != nil {
			return s.capture(err)
		}
	}
	return nil
}
func (s *readStorage) setHash(ctx context.Context, user, peer, hash int64, kind string) error {
	if hash == 0 {
		return s.capture(errors.New("Telegram access hash is invalid"))
	}
	return s.exec(ctx, `INSERT INTO telegram_peer_hashes(epoch,user_id,kind,peer_id,access_hash) SELECT ?,?,?,?,? WHERE EXISTS(SELECT 1 FROM authorization_state WHERE epoch=?) ON CONFLICT(epoch,user_id,kind,peer_id) DO UPDATE SET access_hash=excluded.access_hash`, s.epoch, user, kind, peer, hash, s.epoch)
}
func (s *readStorage) getHash(ctx context.Context, user, peer int64, kind string) (int64, bool, error) {
	if err := s.checkEpoch(ctx); err != nil {
		return 0, false, err
	}
	var hash int64
	err := s.db.QueryRowContext(ctx, `SELECT access_hash FROM telegram_peer_hashes WHERE epoch=? AND user_id=? AND kind=? AND peer_id=?`, s.epoch, user, kind, peer).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err == nil && hash == 0 {
		err = errors.New("Telegram access hash is invalid")
	}
	return hash, err == nil, s.capture(err)
}
func (s *readStorage) SetUserAccessHash(ctx context.Context, user, peer, hash int64) error {
	return s.setHash(ctx, user, peer, hash, "user")
}
func (s *readStorage) GetUserAccessHash(ctx context.Context, user, peer int64) (int64, bool, error) {
	return s.getHash(ctx, user, peer, "user")
}
func (s *readStorage) SetChannelAccessHash(ctx context.Context, user, peer, hash int64) error {
	return s.setHash(ctx, user, peer, hash, "channel")
}
func (s *readStorage) GetChannelAccessHash(ctx context.Context, user, peer int64) (int64, bool, error) {
	return s.getHash(ctx, user, peer, "channel")
}
