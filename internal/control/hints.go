package control

import (
	"errors"
	"fmt"
	"io"
)

// humanHint contains only fixed application-authored guidance, never submitted
// values, filesystem paths or external error text.
type humanHint string

func (h humanHint) Error() string { return string(h) }

type humanStepError struct {
	hint  string
	cause error
}

func (e *humanStepError) Error() string { return e.hint }
func (e *humanStepError) Unwrap() error { return e.cause }

func writeHumanHint(writer io.Writer, err error) bool {
	var step *humanStepError
	if errors.As(err, &step) {
		fmt.Fprintln(writer, "telegram-mcp:", step.hint)
		return true
	}
	var hint humanHint
	if errors.As(err, &hint) {
		fmt.Fprintln(writer, "telegram-mcp:", string(hint))
		return true
	}
	return false
}
