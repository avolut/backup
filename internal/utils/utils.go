package utils

import (
	"fmt"
	"sync"
	"time"
)

var (
	backupLock      sync.Mutex
	isBackupRunning bool
	progressMutex   sync.Mutex
	currentProgress *BackupProgress
)

type BackupProgress struct {
	TotalItems      int
	CurrentItem     int
	CurrentItemName string
	StartTime       time.Time
	LastUpdateTime  time.Time
}

func InitProgress(totalItems int) *BackupProgress {
	progressMutex.Lock()
	defer progressMutex.Unlock()

	currentProgress = &BackupProgress{
		TotalItems:     totalItems,
		StartTime:      time.Now(),
		LastUpdateTime: time.Now(),
	}
	return currentProgress
}

func UpdateProgress(itemName string) {
	progressMutex.Lock()
	defer progressMutex.Unlock()

	if currentProgress == nil {
		return
	}

	currentProgress.CurrentItem++
	currentProgress.CurrentItemName = itemName
	currentProgress.LastUpdateTime = time.Now()
}

func GetProgressStatus() string {
	progressMutex.Lock()
	defer progressMutex.Unlock()

	if currentProgress == nil {
		return "No backup in progress"
	}

	percentage := float64(currentProgress.CurrentItem) / float64(currentProgress.TotalItems) * 100
	elapsed := time.Since(currentProgress.StartTime)
	estimatedTotal := time.Duration(0)
	if currentProgress.CurrentItem > 0 {
		estimatedTotal = time.Duration(float64(elapsed) / float64(currentProgress.CurrentItem) * float64(currentProgress.TotalItems))
	}
	estimatedRemaining := estimatedTotal - elapsed

	return fmt.Sprintf("%.1f%% (%d/%d) | %s | Elapsed: %s | Remaining: ~%s",
		percentage,
		currentProgress.CurrentItem,
		currentProgress.TotalItems,
		currentProgress.CurrentItemName,
		formatDuration(elapsed),
		formatDuration(estimatedRemaining))
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second

	if h > 0 {
		return fmt.Sprintf("%dh%dm%ds", h, m, s)
	} else if m > 0 {
		return fmt.Sprintf("%dm%ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

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
