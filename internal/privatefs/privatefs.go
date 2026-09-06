// Package privatefs manages local state protected by the current OS account.
package privatefs

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

func validPath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Dir(path) == path {
		return errors.New("private path must be canonical, absolute, and not a filesystem root")
	}
	return validatePlatformPath(path)
}

// CheckAncestors rejects paths whose existing ancestors can be replaced by another user.
func CheckAncestors(path string) error {
	if err := validPath(path); err != nil {
		return err
	}
	return checkAncestors(path)
}

// EnsureDirectory creates a private directory, rejecting an unsafe existing one.
func EnsureDirectory(path string) error {
	if err := CheckAncestors(path); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		return CheckDirectory(path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(path)
	if _, err := os.Stat(parent); errors.Is(err, os.ErrNotExist) {
		if err := EnsureDirectory(parent); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err := createDirectory(path); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return CheckDirectory(path)
}

func CheckDirectory(path string) error { return checkPath(path, true) }
func CheckFile(path string) error      { return checkPath(path, false) }

func checkPath(path string, directory bool) error {
	if err := CheckAncestors(path); err != nil {
		return err
	}
	f, err := openPrivate(path, directory, false)
	if err != nil {
		return err
	}
	return f.Close()
}

// OpenFile opens or creates a private regular file without following a link.
// The caller owns the handle; existing files are never truncated.
func OpenFile(path string, create bool) (*os.File, error) {
	if err := validPath(path); err != nil {
		return nil, err
	}
	if err := CheckDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	return openPrivate(path, false, create)
}

func ReadFile(path string, max int64) ([]byte, error) {
	if max <= 0 || max == int64(^uint64(0)>>1) {
		return nil, errors.New("invalid private file size limit")
	}
	f, err := OpenFile(path, false)
	if err != nil {
		return nil, err
	}
	return readBounded(f, max)
}

// ReadExecutable reads an owner-only regular executable without following links.
// It does not relax the permissions required for credential or metadata files.
func ReadExecutable(path string, max int64) ([]byte, error) {
	if max <= 0 || max == int64(^uint64(0)>>1) {
		return nil, errors.New("invalid executable size limit")
	}
	if err := CheckDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := validPath(path); err != nil {
		return nil, err
	}
	f, err := openExecutable(path)
	if err != nil {
		return nil, err
	}
	return readBounded(f, max)
}

func readBounded(f *os.File, max int64) ([]byte, error) {
	data, readError := io.ReadAll(io.LimitReader(f, max+1))
	err := errors.Join(readError, f.Close())
	if int64(len(data)) > max {
		err = errors.Join(err, errors.New("private file exceeds size limit"))
	}
	if err != nil {
		clear(data)
		return nil, err
	}
	return data, nil
}

// WriteFile publishes a complete, synced file. replace=false never overwrites.
func WriteFile(path string, data []byte, replace bool) (result error) {
	if err := validPath(path); err != nil {
		return err
	}
	if err := CheckDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	if replace {
		if err := CheckFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	f, err := createTemporary(filepath.Dir(path))
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() {
		err := os.Remove(name)
		if !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}()
	_, writeError := f.Write(data)
	if writeError == nil {
		writeError = f.Sync()
	}
	if err = errors.Join(writeError, f.Close()); err != nil {
		return err
	}
	if err = publish(name, path, replace); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}
