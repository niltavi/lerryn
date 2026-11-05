// Package acceptance holds the acceptance scenarios of the PRD (A01 to
// A18, B01 to B03) as black-box tests: each builds the lerryn binary,
// starts it as an operator would and talks to it over the public API.
// They need Docker and run with the integration build tag:
//
//	go test -tags integration -v ./tests/acceptance/
//
// LERRYN_TEST_MOUNT_ROOT places the data directory where the Docker
// daemon can mount it, as for the other integration tests.
package acceptance
