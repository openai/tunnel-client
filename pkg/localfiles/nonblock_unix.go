//go:build unix

package localfiles

import "syscall"

// A FIFO swapped in after the initial file check must not block open.
const nonblockFlag = syscall.O_NONBLOCK
