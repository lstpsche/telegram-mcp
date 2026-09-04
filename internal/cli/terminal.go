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

// Terminal is a direct /dev/tty channel. Authentication material never uses
// process stdin, stdout, environment variables, or command-line arguments.
type Terminal struct {
	file *os.File
}

func OpenTerminal() (*Terminal, error) {
	file, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, errors.New("an interactive terminal is required")
	}
	if !term.IsTerminal(int(file.Fd())) {
		_ = file.Close()
		return nil, errors.New("an interactive terminal is required")
	}
	return &Terminal{file: file}, nil
}

func (t *Terminal) Close() error {
	if t == nil || t.file == nil {
		return nil
	}
	return t.file.Close()
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
	return t.readHidden(ctx, "Test-DC phone number: ", true)
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
	if t == nil || t.file == nil {
		return errors.New("interactive terminal is not initialized")
	}
	rendered, err := renderQRCode(tokenURL)
	if err != nil {
		return errors.New("render Telegram login QR code")
	}
	remaining := time.Until(expiresAt).Truncate(time.Second)
	if remaining < 0 {
		remaining = 0
	}
	if _, err := fmt.Fprintf(
		t.file,
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
	if t == nil || t.file == nil {
		return nil, errors.New("interactive terminal is not initialized")
	}
	if _, err := io.WriteString(t.file, label); err != nil {
		return nil, errors.New("write interactive prompt")
	}
	value, err := readPasswordBounded(ctx, t.file, maximumOperatorInputBytes)
	_, newlineError := io.WriteString(t.file, "\n")
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
	if len(value) == 0 {
		clear(value)
		return nil, errors.New("interactive input length is outside the allowed range")
	}
	return value, nil
}

func renderQRCode(tokenURL string) (string, error) {
	code, err := qr.Encode(tokenURL, qr.M)
	if err != nil {
		return "", err
	}
	size := code.Size + 2*qrQuietZoneModules
	var output strings.Builder
	for y := 0; y < size; y += 2 {
		// Black foreground on a white background keeps the code scannable on
		// both light and dark terminal themes. Each half-block is one QR row.
		output.WriteString("\x1b[30;47m")
		for x := 0; x < size; x++ {
			top := qrPixel(code, x-qrQuietZoneModules, y-qrQuietZoneModules)
			bottom := qrPixel(code, x-qrQuietZoneModules, y+1-qrQuietZoneModules)
			switch {
			case top && bottom:
				output.WriteRune('█')
			case top:
				output.WriteRune('▀')
			case bottom:
				output.WriteRune('▄')
			default:
				output.WriteByte(' ')
			}
		}
		output.WriteString("\x1b[0m\n")
	}
	return output.String(), nil
}

func qrPixel(code *qr.Code, x, y int) bool {
	return x >= 0 && y >= 0 && x < code.Size && y < code.Size && code.Black(x, y)
}
