// Package logging builds the process-wide structured logger.
package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"syscall"
)

// New returns a slog logger writing to w. level is one of debug, info,
// warn or error; format is json (the default for deployments) or text.
func New(w io.Writer, level, format string) (*slog.Logger, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("log.level %q: %w", level, err)
	}
	opts := &slog.HandlerOptions{Level: lvl}
	switch format {
	case "json":
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	case "text":
		return slog.New(slog.NewTextHandler(w, opts)), nil
	default:
		return nil, fmt.Errorf("log.format %q: want json or text", format)
	}
}

// ClientGone reports whether err is only the consequence of the client
// going away: the request's context is cancelled and err is that
// cancellation, or writing the response failed because the client had
// already closed the connection (the write can fail before the server
// notices the cancellation). Such failures are not server faults; logging
// them as errors would bury real ones when many clients close pages or
// time out. A cancellation the server caused itself, with the request
// context still live, is not matched and stays an error.
func ClientGone(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	return ctx.Err() != nil && errors.Is(err, context.Canceled) && errors.Is(ctx.Err(), context.Canceled)
}
