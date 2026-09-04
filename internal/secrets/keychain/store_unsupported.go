//go:build !darwin || !cgo

package keychain

func (s *Store) put(string, []byte) error { return ErrUnsupported }

func (s *Store) get(string) ([]byte, error) { return nil, ErrUnsupported }

func (s *Store) delete(string) error { return ErrUnsupported }
