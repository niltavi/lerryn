// Package service holds the business rules of Lerryn. HTTP and MCP
// handlers decode requests, call a service and encode the result; every
// permission check happens here (AGENTS.md, layering).
//
// Files are split by domain, such as accounts.go and authz.go, so that
// workstreams editing different domains do not touch the same file.
package service

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Errors returned by services. The HTTP layer maps each to a problem code.
var (
	// ErrNotFound means the resource does not exist or the caller may not
	// see it; the two are deliberately indistinguishable.
	ErrNotFound = errors.New("not found")
	// ErrUnauthenticated means the credentials are missing, wrong, expired
	// or revoked.
	ErrUnauthenticated = errors.New("unauthenticated")
	// ErrForbidden means the caller can see the resource but may not
	// perform the action, or used the wrong kind of credential for it.
	ErrForbidden = errors.New("forbidden")
	// ErrPermissionDenied is the ErrForbidden of an operation the caller's
	// role does not allow, such as one only admins may perform or one the
	// caller's project role lacks (Authorizer.Require), as opposed to a
	// credential that lacks a scope. The API answers permission_denied.
	ErrPermissionDenied = fmt.Errorf("%w: permission denied", ErrForbidden)
	// ErrUsernameTaken means another account already uses the username,
	// compared case-insensitively.
	ErrUsernameTaken = errors.New("username taken")
	// ErrRateLimited means too many attempts in a short time.
	ErrRateLimited = errors.New("rate limited")
	// ErrRegistrationDisabled means an admin closed open registration.
	ErrRegistrationDisabled = errors.New("registration disabled")
	// ErrPasswordLoginDisabled means an admin turned off password login
	// for users who are not admins.
	ErrPasswordLoginDisabled = errors.New("password login disabled")
	// ErrLastOwner means the change would leave a project without an owner.
	ErrLastOwner = errors.New("last owner")
	// ErrRunInProgress means the task forbids overlapping runs and one of
	// its runs is still active. Runs.Start returns it wrapped in a
	// *RunInProgressError that names the active run.
	ErrRunInProgress = errors.New("run in progress")
	// ErrPreconditionRequired means a change to an existing resource came
	// without If-Match.
	ErrPreconditionRequired = errors.New("precondition required")
	// ErrVersionConflict means the If-Match value is not the resource's
	// current version: someone else changed it in between.
	ErrVersionConflict = errors.New("version conflict")
	// ErrQueueFull means the run queue, overall or for the project, is at
	// its limit (tech design 8.4).
	ErrQueueFull = errors.New("queue full")
	// ErrIdempotencyConflict means an Idempotency-Key was used before with
	// a different request.
	ErrIdempotencyConflict = errors.New("idempotency conflict")
)

// VersionConflictError is ErrVersionConflict with the current version,
// which the API reports so the caller can re-read and resubmit.
type VersionConflictError struct {
	// Current is 0 when the resource no longer exists under its name,
	// such as an archived task; Detail then says so.
	Current int
	Detail  string
}

func (e *VersionConflictError) Error() string {
	if e.Current == 0 {
		return "version conflict: " + e.Detail
	}
	return fmt.Sprintf("version conflict: the current version is %d", e.Current)
}

func (e *VersionConflictError) Unwrap() error { return ErrVersionConflict }

// PayloadTooLargeError is returned for a request body over its limit.
type PayloadTooLargeError struct {
	Limit int64
}

func (e *PayloadTooLargeError) Error() string {
	return fmt.Sprintf("request body exceeds %d bytes", e.Limit)
}

// RunInProgressError is returned when a task that forbids overlapping
// runs already has an active run.
type RunInProgressError struct {
	// RunID is the active run; uuid.Nil if it ended before it could be
	// looked up.
	RunID uuid.UUID
}

func (e *RunInProgressError) Error() string {
	return fmt.Sprintf("run in progress: task has the active run %s", e.RunID)
}

func (e *RunInProgressError) Unwrap() error { return ErrRunInProgress }

// FieldError is one invalid input. Exactly one of Pointer and Parameter is
// set: Pointer is a JSON pointer into the request body such as #/username,
// Parameter names a query or path parameter.
type FieldError struct {
	Pointer   string
	Parameter string
	Detail    string
}

// ValidationError reports every invalid input of a request at once.
type ValidationError struct {
	Fields []FieldError
	// Content is true when the request is well formed but what it says
	// breaks a rule, such as a task manifest that declares a stage twice.
	// The API answers such errors with 422 rather than 400.
	Content bool
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		where := f.Pointer
		if where == "" {
			where = f.Parameter
		}
		parts[i] = where + ": " + f.Detail
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

// validation collects field errors.
type validation struct {
	fields        []FieldError
	unprocessable bool
}

func (v *validation) body(pointer, detail string) {
	v.fields = append(v.fields, FieldError{Pointer: pointer, Detail: detail})
}

// content records an error in the content of a well-formed request body;
// see ValidationError.Content.
func (v *validation) content(pointer, detail string) {
	v.body(pointer, detail)
	v.unprocessable = true
}

func (v *validation) param(name, detail string) {
	v.fields = append(v.fields, FieldError{Parameter: name, Detail: detail})
}

func (v *validation) err() error {
	if len(v.fields) == 0 {
		return nil
	}
	return &ValidationError{Fields: v.fields, Content: v.unprocessable}
}
