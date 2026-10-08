//go:build linux

// The line above tells the Go compiler to use this file only when building
// for Linux (which is what the cluster runs). cputime_other.go is the
// stand-in for every other operating system.

package main

import (
	"syscall"
	"time"
)

// rusageThread is the Linux code for "tell me about the current thread only",
// as opposed to the whole program.
const rusageThread = 1

// cpuTimeOfThisThread asks Linux how much processor time the current thread
// has used so far.
//
// This is NOT the same as time on the clock. If the pod is squeezed for CPU
// (because the node is busy, or the pod hit its CPU limit) the clock keeps
// running but this number stops going up. That is exactly why we use it:
// each frame then costs a fixed amount of real CPU, so a pod's capacity is
// honestly limited by the CPU it is given.
func cpuTimeOfThisThread() time.Duration {
	var usage syscall.Rusage
	if err := syscall.Getrusage(rusageThread, &usage); err != nil {
		return 0
	}
	// Linux reports two parts: time spent in our own code (Utime) and time
	// the kernel spent working for us (Stime). Each is given as whole
	// seconds plus millionths of a second. Add it all up.
	user := time.Duration(usage.Utime.Sec)*time.Second + time.Duration(usage.Utime.Usec)*time.Microsecond
	system := time.Duration(usage.Stime.Sec)*time.Second + time.Duration(usage.Stime.Usec)*time.Microsecond
	return user + system
}
