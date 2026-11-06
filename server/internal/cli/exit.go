package cli

import (
	"errors"
	"strconv"
)

// Exit codes. docs/zh-CN/cli.md documents them; scripts and agents rely on them,
// so they only ever gain new values.
const (
	ExitOK = 0
	// ExitFailure is a local problem: config, files, or the server cannot be
	// reached.
	ExitFailure = 1
	// ExitUsage is a wrong command line.
	ExitUsage = 2
	// ExitAuth means no credential, or the server rejected it (HTTP 401).
	ExitAuth = 3
	// ExitAPI is any other error answer from the API.
	ExitAPI = 4
	// ExitRunFailed means the waited-for run ended without success.
	ExitRunFailed = 5
	// ExitWaitTimeout means the wait ended before the run was confirmed ended.
	ExitWaitTimeout = 6
)

// ExitError carries the process exit code for an error. Reported is true
// when the command already printed the error, so main prints nothing more.
type ExitError struct {
	Code     int
	Err      error
	Reported bool
}

func (e *ExitError) Error() string {
	if e.Err == nil {
		return "exit status " + strconv.Itoa(e.Code)
	}
	return e.Err.Error()
}

func (e *ExitError) Unwrap() error { return e.Err }

// ExitCode returns the exit code for err: 0 for nil, the code of an
// *ExitError, and ExitFailure otherwise.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var e *ExitError
	if errors.As(err, &e) {
		return e.Code
	}
	return ExitFailure
}

// Reported tells main whether err was already printed.
func Reported(err error) bool {
	var e *ExitError
	return errors.As(err, &e) && e.Reported
}

func usageError(err error) error { return &ExitError{Code: ExitUsage, Err: err} }

func authError(err error) error { return &ExitError{Code: ExitAuth, Err: err} }
