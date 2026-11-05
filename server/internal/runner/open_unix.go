//go:build unix

package runner

import (
	"os"
	"syscall"
)

// openNonBlocking opens name for reading without blocking on a FIFO. For a
// regular file O_NONBLOCK has no effect on reads.
func openNonBlocking(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
