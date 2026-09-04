package cli

import (
	"strings"
	"testing"
)

func TestRenderQRCodeDoesNotPrintTokenURL(t *testing.T) {
	t.Parallel()

	const tokenURL = "tg://login?token=secret-capability-value"
	rendered, err := renderQRCode(tokenURL)
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
