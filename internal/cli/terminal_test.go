package cli

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"rsc.io/qr"
)

func TestRenderQRCodeRequiresEnoughVisibleSpace(t *testing.T) {
	t.Parallel()
	const tokenURL = "tg://login?token=synthetic-qr-rendering-test"
	code, err := qr.Encode(tokenURL, qr.M)
	if err != nil {
		t.Fatal(err)
	}
	size := code.Size + 2*qrQuietZoneModules
	for _, tc := range []struct {
		name          string
		columns, rows int
		fits          bool
	}{
		{name: "exact fit with cursor space", columns: 2*size + 1, rows: size + 3, fits: true},
		{name: "would wrap", columns: 2 * size, rows: size + 3},
		{name: "would scroll out of view", columns: 2*size + 1, rows: size + 2},
		{name: "unavailable dimensions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered, err := renderQRCode(tokenURL, tc.columns, tc.rows)
			if tc.fits {
				if err != nil || rendered == "" {
					t.Fatalf("fitting QR was not rendered: %v", err)
				}
				return
			}
			var sizeError *QRCodeSizeError
			if rendered != "" || !errors.As(err, &sizeError) {
				t.Fatalf("expected a sizing error without a partial QR: %v", err)
			}
			if sizeError.RequiredColumns != 2*size+1 || sizeError.RequiredRows != size+3 || sizeError.Columns != tc.columns || sizeError.Rows != tc.rows {
				t.Fatalf("incorrect console dimensions: %v", err)
			}
			if !strings.Contains(err.Error(), "enlarge the terminal or reduce its font size") || strings.Contains(err.Error(), "synthetic-qr") {
				t.Fatal("sizing error must guide the operator without including login material")
			}
		})
	}
}

func TestRenderQRCodeDoesNotPrintTokenURL(t *testing.T) {
	t.Parallel()

	const tokenURL = "tg://login?token=secret-capability-value"
	rendered, err := renderQRCode(tokenURL, 200, 100)
	if err != nil {
		t.Fatal(err)
	}
	if rendered == "" || !strings.Contains(rendered, "\n") {
		t.Fatal("renderQRCode() returned no terminal image")
	}
	if strings.Contains(rendered, tokenURL) || strings.Contains(rendered, "secret-capability-value") {
		t.Fatal("renderQRCode() included the raw login token")
	}
}

func TestRenderQRCodePreservesModulesWithoutGlyphGaps(t *testing.T) {
	t.Parallel()

	const tokenURL = "tg://login?token=synthetic-qr-rendering-test"
	code, err := qr.Encode(tokenURL, qr.M)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := renderQRCode(tokenURL, 200, 100)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(rendered, "█▀▄") {
		t.Fatal("QR modules must use cell backgrounds, independent of glyph height and line spacing")
	}
	for _, tc := range []struct {
		name             string
		ansiBlackIsWhite bool
		inverted         bool
	}{
		{name: "default"},
		{name: "theme maps ANSI black to white", ansiBlackIsWhite: true},
		{name: "inherited inverse text", inverted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			foreground, background, inverted := 7, 0, tc.inverted
			paletteDark := func(index int) bool {
				return index == 16 || index == 0 && !tc.ansiBlackIsWhite
			}
			x, y := 0, 0
			for remaining := rendered; remaining != ""; {
				if strings.HasPrefix(remaining, "\x1b[") {
					end := strings.IndexByte(remaining, 'm')
					if end < 0 {
						t.Fatal("unterminated terminal style")
					}
					parts := strings.Split(remaining[2:end], ";")
					for i := 0; i < len(parts); i++ {
						parameter, err := strconv.Atoi(parts[i])
						if err != nil {
							t.Fatal("invalid terminal style")
						}
						switch {
						case parameter == 0:
							foreground, background, inverted = 7, 0, false
						case parameter >= 30 && parameter <= 37:
							foreground = parameter - 30
						case parameter >= 40 && parameter <= 47:
							background = parameter - 40
						case parameter == 38 || parameter == 48:
							if i+2 >= len(parts) || parts[i+1] != "5" {
								t.Fatal("unsupported extended terminal color")
							}
							index, err := strconv.Atoi(parts[i+2])
							if err != nil || index < 0 || index > 255 {
								t.Fatal("invalid terminal color")
							}
							if parameter == 38 {
								foreground = index
							} else {
								background = index
							}
							i += 2
						default:
							t.Fatalf("unsupported terminal style %d", parameter)
						}
					}
					remaining = remaining[end+1:]
					continue
				}
				glyph, width := utf8.DecodeRuneInString(remaining)
				remaining = remaining[width:]
				if glyph == '\n' {
					if x != 2*(code.Size+2*qrQuietZoneModules) {
						t.Fatal("QR row has incorrect width")
					}
					x, y = 0, y+1
					continue
				}
				if glyph != ' ' {
					t.Fatal("unexpected QR glyph")
				}
				fg, bg := foreground, background
				if inverted {
					fg, bg = bg, fg
				}
				if paletteDark(bg) != code.Black(x/2-qrQuietZoneModules, y-qrQuietZoneModules) {
					t.Fatalf("QR cell at (%d, %d) is not visible with the intended polarity", x, y)
				}
				x++
			}
			if y != code.Size+2*qrQuietZoneModules {
				t.Fatal("QR image has incorrect height")
			}
		})
	}
}
