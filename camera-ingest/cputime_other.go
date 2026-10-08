//go:build !linux

// Used only when the program is built for something other than Linux, for
// example to try it on a Mac. The cluster never uses this file.

package main

import "time"

// programStart is remembered once, when the program starts.
var programStart = time.Now()

// cpuTimeOfThisThread is a rough stand-in here: it returns plain clock time
// since the program started. That is good enough to try the service on a
// laptop, but it does not measure real CPU the way the Linux version does.
func cpuTimeOfThisThread() time.Duration {
	return time.Since(programStart)
}
