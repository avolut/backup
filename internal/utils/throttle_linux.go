//go:build linux

package utils

import (
	"runtime"
	"syscall"

	"golang.org/x/sys/unix"
)

// ThrottleCPU sets the process priority to reduce CPU usage
func ThrottleCPU() error {
	if runtime.GOOS == "linux" {
		// Get the current process
		proc := syscall.Getpid()

		// Set nice value to 19 (lowest priority)
		// This will reduce CPU usage significantly
		if err := syscall.Setpriority(syscall.PRIO_PROCESS, proc, 19); err != nil {
			return err
		}
	}

	return nil
}

// setLinuxCPUAffinity is a stub for non-Linux systems
func setLinuxCPUAffinity() error {
	// Create a CPU set with only one CPU enabled
	// This helps reduce CPU usage by preventing the process from running on multiple cores
	mask := unix.CPUSet{}
	mask.Set(0) // Use only the first CPU

	// Set CPU affinity for the current process
	if err := unix.SchedSetaffinity(0, &mask); err != nil {
		return err
	}

	return nil
}

// SetProcessPriority sets the process priority for the current process
func SetProcessPriority() error {
	// Set process priority
	if err := ThrottleCPU(); err != nil {
		return err
	}

	// CPU affinity is only supported on Linux
	if runtime.GOOS == "linux" {
		if err := setLinuxCPUAffinity(); err != nil {
			return err
		}
	}

	return nil
}
