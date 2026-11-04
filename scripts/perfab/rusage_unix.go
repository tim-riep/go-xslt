//go:build unix

package main

import (
	"os"
	"runtime"
	"syscall"
)

// peakRSS returns the peak resident set size of a finished process in
// bytes, or 0 when the platform does not report it.
func peakRSS(ps *os.ProcessState) int64 {
	ru, ok := ps.SysUsage().(*syscall.Rusage)
	if !ok {
		return 0
	}
	if runtime.GOOS == "darwin" {
		return int64(ru.Maxrss) // bytes
	}
	return int64(ru.Maxrss) * 1024 // kilobytes on Linux and the BSDs
}
