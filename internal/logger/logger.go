package logger

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

const (
	colorReset  = "\x1b[0m"
	colorRed    = "\x1b[31m"
	colorYellow = "\x1b[33m"
	colorGreen  = "\x1b[32m"
	colorCyan   = "\x1b[36m"
)

// New builds the app's logger.
// env decides the output format: "production" -> JSON (machines parse it),
// anything else -> human-readable text (nice at a terminal).
func New(env string, w io.Writer) *slog.Logger {
	// HandlerOptions lets us set the minimum level, add source info, etc.
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}

	var handler slog.Handler
	switch env {
	case "production":
		handler = slog.NewJSONHandler(w, opts)
	default:
		if isTerminal(w) {
			w = &colorWriter{w: w}
		}
		handler = slog.NewTextHandler(w, opts)
	}

	return slog.New(handler)
}

// isTerminal reports whether w is a character device - i.e. a real
// terminal (/dev/pts/*), not a pipe or a regular file.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}

	return fi.Mode()&os.ModeCharDevice != 0
}

// colorWriter wraps an io.Writer and paints each log line by its level.
type colorWriter struct {
	w io.Writer
}

// Write inspects the formatted line and wraps it in ANSI codes.
func (c *colorWriter) Write(p []byte) (int, error) {
	line := string(p)

	var color string
	switch {
	case strings.Contains(line, "level=ERROR"):
		color = colorRed
	case strings.Contains(line, "level=WARN"):
		color = colorYellow
	case strings.Contains(line, "level=DEBUG"):
		color = colorCyan
	case strings.Contains(line, "level=INFO"):
		color = colorGreen
	}

	colored := color + strings.TrimRight(line, "\n") + colorReset + "\n"

	_, err := c.w.Write([]byte(colored))
	return len(p), err
}
