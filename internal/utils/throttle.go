//go:build !linux

package utils

// ThrottleCPU sets the process priority to reduce CPU usage
func ThrottleCPU() error {
	return nil
}

// SetProcessPriority sets the process priority for the current process
func SetProcessPriority() error {
	return nil
}
