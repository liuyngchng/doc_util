// Package slogutil provides a custom slog handler with local-timezone timestamps
// and a compact text format.
package slogutil

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"time"
)

// NewHandler returns a slog.Handler that writes text log entries in the format:
//
//	2026-10-31 11:28:46.123 level=INFO msg="server starting" key=value ...
//
// Time is always local timezone, milliseconds precision.
// All attributes are written as key=value pairs; string values are quoted when
// they contain spaces, '=', '"', '\\', or control characters.
func NewHandler(w io.Writer, opts *slog.HandlerOptions) slog.Handler {
	h := &customHandler{w: w, mu: &sync.Mutex{}}
	if opts == nil {
		h.level = slog.LevelInfo
	} else {
		h.level = opts.Level
		if h.level == nil {
			h.level = slog.LevelInfo
		}
	}
	return h
}

type customHandler struct {
	w     io.Writer
	mu    *sync.Mutex
	level slog.Leveler
}

func (h *customHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

func (h *customHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	// Not needed for current usage — return self.
	return h
}

func (h *customHandler) WithGroup(name string) slog.Handler {
	// Not needed for current usage — return self.
	return h
}

func (h *customHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	var buf []byte

	// 2026-10-31 11:28:46.123
	buf = r.Time.Local().AppendFormat(buf, "2006-01-02 15:04:05.000")
	buf = append(buf, " level="...)
	buf = append(buf, r.Level.String()...)

	// msg
	buf = append(buf, " msg="...)
	buf = appendString(buf, r.Message)

	// Remaining attributes
	r.Attrs(func(a slog.Attr) bool {
		buf = append(buf, ' ')
		buf = appendAttr(buf, a)
		return true
	})

	buf = append(buf, '\n')
	_, err := h.w.Write(buf)
	return err
}

func appendAttr(buf []byte, a slog.Attr) []byte {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return buf
	}
	buf = append(buf, a.Key...)
	buf = append(buf, '=')
	return appendValue(buf, a.Value)
}

func appendValue(buf []byte, v slog.Value) []byte {
	switch v.Kind() {
	case slog.KindString:
		return appendString(buf, v.String())
	case slog.KindInt64:
		return strconv.AppendInt(buf, v.Int64(), 10)
	case slog.KindUint64:
		return strconv.AppendUint(buf, v.Uint64(), 10)
	case slog.KindFloat64:
		return strconv.AppendFloat(buf, v.Float64(), 'g', -1, 64)
	case slog.KindBool:
		return strconv.AppendBool(buf, v.Bool())
	case slog.KindDuration:
		return appendQuoted(buf, v.Duration().String())
	case slog.KindTime:
		return appendQuoted(buf, v.Time().Local().Format(time.RFC3339))
	case slog.KindAny:
		return appendQuoted(buf, fmt.Sprint(v.Any()))
	default:
		return append(buf, "null"...)
	}
}

// appendString appends s to buf, quoting when necessary.
// Mirrors the quoting rules of log/slog.TextHandler.
func appendString(buf []byte, s string) []byte {
	if needsQuoting(s) {
		return appendQuoted(buf, s)
	}
	return append(buf, s...)
}

func appendQuoted(buf []byte, s string) []byte {
	buf = append(buf, '"')
	// strconv.Quote produces a Go string literal with surrounding quotes;
	// trim them and use our own.
	escaped := strconv.Quote(s)
	buf = append(buf, escaped[1:len(escaped)-1]...)
	buf = append(buf, '"')
	return buf
}

func needsQuoting(s string) bool {
	if len(s) == 0 {
		return true
	}
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b <= ' ' || b == '=' || b == '"' || b == '\\' {
			return true
		}
	}
	return false
}
