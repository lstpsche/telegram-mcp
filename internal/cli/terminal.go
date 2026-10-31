package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
	"rsc.io/qr"
)

const (
	maximumOperatorInputBytes = 1024
	qrQuietZoneModules        = 4
)

// Terminal is a direct OS console channel. Authentication material never uses
// process stdin, stdout, environment variables, or command-line arguments.
type Terminal struct {
	input  *os.File
	output *os.File
}

// QRCodeSizeError contains only console dimensions, never login material.
type QRCodeSizeError struct {
	Columns, Rows                 int
	RequiredColumns, RequiredRows int
}

func (e *QRCodeSizeError) Error() string {
	return fmt.Sprintf("Telegram login QR code needs at least %d columns and %d rows (terminal: %d columns, %d rows); enlarge the terminal or reduce its font size", e.RequiredColumns, e.RequiredRows, e.Columns, e.Rows)
}

func OpenTerminal() (*Terminal, error) {
	input, output, err := openConsole()
	if err != nil {
		return nil, fmt.Errorf("an interactive terminal is required: %w", err)
	}
	if !term.IsTerminal(int(input.Fd())) {
		return nil, errors.Join(errors.New("an interactive terminal is required"), input.Close(), output.Close())
	}
	return &Terminal{input: input, output: output}, nil
}

func (t *Terminal) Close() error {
	if t == nil {
		return nil
	}
	var err error
	if t.input != nil {
		err = t.input.Close()
	}
	if t.output != nil {
		err = errors.Join(err, t.output.Close())
	}
	return err
}

func (t *Terminal) ReadAPIID(ctx context.Context) (int, error) {
	value, err := t.readHidden(ctx, "Telegram API ID: ", true)
	if err != nil {
		return 0, err
	}
	defer clear(value)
	apiID, err := strconv.ParseInt(string(value), 10, 32)
	if err != nil || apiID <= 0 {
		return 0, errors.New("Telegram API ID must be a positive integer")
	}
	return int(apiID), nil
}

func (t *Terminal) ReadAPIHash(ctx context.Context) ([]byte, error) {
	return t.readHidden(ctx, "Telegram API hash: ", true)
}

func (t *Terminal) Phone(ctx context.Context) ([]byte, error) {
	return t.readHidden(ctx, "Telegram phone number: ", true)
}

func (t *Terminal) Code(ctx context.Context) ([]byte, error) {
	return t.readHidden(ctx, "Telegram login code: ", true)
}

func (t *Terminal) Password(ctx context.Context) ([]byte, error) {
	return t.readHidden(ctx, "Telegram 2FA password: ", false)
}

func (t *Terminal) ShowQRCode(ctx context.Context, tokenURL string, expiresAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t == nil || t.input == nil || t.output == nil {
		return errors.New("interactive terminal is not initialized")
	}
	columns, rows, err := term.GetSize(int(t.output.Fd()))
	if err != nil {
		return fmt.Errorf("read terminal dimensions for Telegram login QR code: %w", err)
	}
	rendered, err := renderQRCode(tokenURL, columns, rows)
	if err != nil {
		return err
	}
	remaining := time.Until(expiresAt).Truncate(time.Second)
	if remaining < 0 {
		remaining = 0
	}
	if _, err := fmt.Fprintf(
		t.output,
		"Scan with Telegram: Settings > Devices > Link Desktop Device\n%sExpires in %s. Waiting for approval...\n",
		rendered,
		remaining,
	); err != nil {
		return errors.New("write Telegram login QR code to terminal")
	}
	return nil
}

func (t *Terminal) readHidden(ctx context.Context, label string, trimSpace bool) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if t == nil || t.input == nil || t.output == nil {
		return nil, errors.New("interactive terminal is not initialized")
	}
	if _, err := io.WriteString(t.output, label); err != nil {
		return nil, errors.New("write interactive prompt")
	}
	value, err := readPasswordBounded(ctx, t.input, maximumOperatorInputBytes)
	_, newlineError := io.WriteString(t.output, "\n")
	if err != nil {
		clear(value)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, errors.New("read hidden terminal input")
	}
	if newlineError != nil {
		clear(value)
		return nil, errors.New("write interactive prompt terminator")
	}
	if trimSpace {
		trimmed := bytes.TrimSpace(value)
		if len(trimmed) != len(value) {
			copy(value, trimmed)
			clear(value[len(trimmed):])
			value = value[:len(trimmed)]
		}
	}
	// Callers validate empty input according to the requested credential.
	return value, nil
}

func renderQRCode(tokenURL string, columns, rows int) (string, error) {
	code, err := qr.Encode(tokenURL, qr.M)
	if err != nil {
		return "", errors.New("encode Telegram login QR code")
	}
	size := code.Size + 2*qrQuietZoneModules
	// Leave one column to avoid automatic wrapping and three rows for the
	// instructions, expiry message and cursor after the image.
	minimumColumns, minimumRows := 2*size+1, size+3
	if columns < minimumColumns || rows < minimumRows {
		return "", &QRCodeSizeError{Columns: columns, Rows: rows, RequiredColumns: minimumColumns, RequiredRows: minimumRows}
	}
	var output strings.Builder
	for y := 0; y < size; y++ {
		output.WriteString("\x1b[0m")
		for x := 0; x < size; x++ {
			// Cell backgrounds fill font leading as well as the glyph area.
			// Two columns per module keep the QR approximately square.
			if code.Black(x-qrQuietZoneModules, y-qrQuietZoneModules) {
				output.WriteString("\x1b[48;5;16m  ")
			} else {
				output.WriteString("\x1b[48;5;231m  ")
			}
		}
		output.WriteString("\x1b[0m\n")
	}
	return output.String(), nil
}

// Ask reads a bounded human response directly from the console. Input remains
// hidden so setup never echoes submitted account identifiers or consent text.
func (t *Terminal) Ask(ctx context.Context, label string) (string, error) {
	value, err := t.readHidden(ctx, label, true)
	defer clear(value)
	if err != nil {
		return "", err
	}
	return string(value), nil
}
