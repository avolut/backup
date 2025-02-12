package utils

import (
	"sync"
)

var (
	backupLock      sync.Mutex
	isBackupRunning bool
)

// TryLock attempts to acquire the backup lock
func TryLock() (bool, error) {
	backupLock.Lock()
	if isBackupRunning {
		backupLock.Unlock()
		return false, nil
	}
	isBackupRunning = true
	return true, nil
}

// Unlock releases the backup lock
func Unlock() {
	isBackupRunning = false
	backupLock.Unlock()
}
