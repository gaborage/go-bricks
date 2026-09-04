package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
)

// TestActionLogForgedPathStaysOnOneLine drives logActionSummary through a REAL framework
// logger for a request whose path and User-Agent carry a newline. Console mode used to
// render the message verbatim and split the event across lines; JSON mode is the
// regression guard. The logger is built inside captureStdout because the console writer
// captures os.Stdout at construction.
func TestActionLogForgedPathStaysOnOneLine(t *testing.T) {
	cases := map[string]bool{"console": true, "json": false}
	for name, pretty := range cases {
		t.Run(name, func(t *testing.T) {
			out := captureStdout(t, func() {
				e := echo.New()
				req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/test", http.NoBody)
				req.URL.Path = "/test\n[FORGED] level=info msg=owned"
				req.Header.Set("User-Agent", "ua\nforged-agent")
				c := e.NewContext(req, httptest.NewRecorder())
				logActionSummary(c, logger.New("info", pretty), LoggerConfig{SlowRequestThreshold: time.Second}, 5*time.Millisecond, 200, nil)
			})
			lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
			require.Len(t, lines, 1, "one request must emit one line, got: %q", out)
			assert.Contains(t, lines[0], `\n[FORGED]`, "path newline must render as an escape sequence")
			assert.Contains(t, lines[0], `ua\nforged-agent`, "User-Agent field newline must render as an escape sequence")
		})
	}
}
