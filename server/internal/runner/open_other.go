//go:build !unix

package runner

import "os"

// openNonBlocking opens name for reading. Platforms without FIFOs in the
// file system need nothing special.
func openNonBlocking(root *os.Root, name string) (*os.File, error) {
	return root.Open(name)
}
