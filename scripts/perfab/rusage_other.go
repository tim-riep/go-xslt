//go:build !unix

package main

import "os"

// peakRSS is not available here: the RSS columns show 0 and take no part
// in the verdict.
func peakRSS(*os.ProcessState) int64 { return 0 }
