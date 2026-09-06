package service

import (
	"errors"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalidInstallation = errors.New("service installation is not canonical")
	ErrUnsafePath          = errors.New("service path is unsafe")
)

const maxInstallationBytes = 16 * 1024

func validatePath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || len(path) > 4096 || !utf8.ValidString(path) || strings.ContainsFunc(path, func(r rune) bool { return unicode.IsControl(r) || r == '\ufffe' || r == '\uffff' }) {
		return ErrUnsafePath
	}
	return nil
}
