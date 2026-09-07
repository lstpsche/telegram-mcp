package reader

import (
	"strings"
	"unicode/utf8"

	"github.com/lstpsche/telegram-mcp/internal/model"
)

func normalizeDiscoveryQuery(query string) (string, error) {
	if !utf8.ValidString(query) || len(query) > 1024 {
		return "", model.TextError(model.ErrorInvalidInput, nil)
	}
	normalized := strings.ToLower(strings.TrimSpace(query))
	if (query != "" && normalized == "") || utf8.RuneCountInString(normalized) > 256 {
		return "", model.TextError(model.ErrorInvalidInput, nil)
	}
	return normalized, nil
}
