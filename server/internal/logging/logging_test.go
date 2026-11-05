package logging

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"
)

func TestClientGone(t *testing.T) {
	live := context.Background()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	// What net/http returns when the client closed the connection first.
	brokenPipe := &net.OpError{Op: "write", Net: "tcp", Err: os.NewSyscallError("write", syscall.EPIPE)}
	reset := &net.OpError{Op: "write", Net: "tcp", Err: os.NewSyscallError("write", syscall.ECONNRESET)}
	for _, tc := range []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{"no error", cancelled, nil, false},
		{"request cancelled", cancelled, fmt.Errorf("list runs: %w", context.Canceled), true},
		{"server cancelled its own work", live, context.Canceled, false},
		{"deadline", cancelled, context.DeadlineExceeded, false},
		{"broken pipe on write", live, fmt.Errorf("encode: %w", brokenPipe), true},
		{"connection reset on write", live, reset, true},
		{"other error", cancelled, errors.New("database is down"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClientGone(tc.ctx, tc.err); got != tc.want {
				t.Fatalf("ClientGone = %v, want %v", got, tc.want)
			}
		})
	}
}
