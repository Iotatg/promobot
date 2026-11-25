package notify

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/Iotatg/gotdbot"
)

// Logger writes structured logs to slog and, when a log group is configured,
// mirrors them to Telegram as status updates.
type Logger struct {
	slog       *slog.Logger
	client     *gotdbot.Client
	logGroupID int64
}

// New creates a Logger. The client may be nil (remote logging disabled).
func New(base *slog.Logger, client *gotdbot.Client, logGroupID int64) *Logger {
	if base == nil {
		base = slog.Default()
	}
	return &Logger{slog: base, client: client, logGroupID: logGroupID}
}

func (l *Logger) Info(msg string, kv ...any)  { l.emit(slog.LevelInfo, "INFO", msg, kv...) }
func (l *Logger) Warn(msg string, kv ...any)  { l.emit(slog.LevelWarn, "WARN", msg, kv...) }
func (l *Logger) Error(msg string, kv ...any) { l.emit(slog.LevelError, "ERROR", msg, kv...) }

func (l *Logger) emit(level slog.Level, tag, msg string, kv ...any) {
	l.slog.Log(nil, level, msg, kv...)

	if l.client == nil || l.logGroupID == 0 {
		return
	}
	line := fmt.Sprintf("[%s] %s", tag, msg)
	if len(kv) > 0 {
		parts := make([]string, 0, len(kv)/2)
		for i := 0; i+1 < len(kv); i += 2 {
			parts = append(parts, fmt.Sprintf("%v=%v", kv[i], kv[i+1]))
		}
		line += " | " + strings.Join(parts, " ")
	}
	_, _ = l.client.SendTextMessage(l.logGroupID, line, nil)
}
