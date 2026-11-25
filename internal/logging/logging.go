package logging

/*
	Leveled logging on top of the stdlib log, with no dependencies.
	Levels run debug < info < warn < error; lower levels are dropped. Set the
	level with the -log-level flag or the LOG_LEVEL environment variable
	(default: info).
*/

import (
	"fmt"
	"log"
	"os"
	"strings"
)

type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

var (
	level  = LevelInfo
	logger = log.New(os.Stderr, "", log.Ldate|log.Ltime)
)

// String returns the level name.
func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	default:
		return "info"
	}
}

// SetLevel configures the minimum level that is emitted.
func SetLevel(l Level) { level = l }

// ParseLevel parses "debug"/"info"/"warn"/"error" case-insensitively.
func ParseLevel(s string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return LevelDebug, nil
	case "info", "":
		return LevelInfo, nil
	case "warn", "warning":
		return LevelWarn, nil
	case "error":
		return LevelError, nil
	}
	return LevelInfo, nil
}

// SetFlags passes stdlib log flags through (e.g. log.Lshortfile).
func SetFlags(fl int) { logger.SetFlags(fl) }

func emit(l Level, msg string) {
	if l < level {
		return
	}
	logger.Output(2, msg)
}

// Stringer formatters (printf-style).
func Debugf(format string, args ...any) { emit(LevelDebug, "[DEBUG] "+fmt.Sprintf(format, args...)) }
func Infof(format string, args ...any)  { emit(LevelInfo, fmt.Sprintf(format, args...)) }
func Warnf(format string, args ...any)  { emit(LevelWarn, "[WARN] "+fmt.Sprintf(format, args...)) }
func Errorf(format string, args ...any) { emit(LevelError, "[ERROR] "+fmt.Sprintf(format, args...)) }

// Plain formatters.
func Debug(args ...any)  { emit(LevelDebug, "[DEBUG] "+fmt.Sprint(args...)) }
func Info(args ...any)   { emit(LevelInfo, fmt.Sprint(args...)) }
func Infoln(args ...any) { emit(LevelInfo, fmt.Sprintln(args...)) }
func Warn(args ...any)   { emit(LevelWarn, "[WARN] "+fmt.Sprint(args...)) }
func Error(args ...any)  { emit(LevelError, "[ERROR] "+fmt.Sprint(args...)) }
