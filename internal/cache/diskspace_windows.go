//go:build windows

package cache

import (
	"math"

	"golang.org/x/sys/windows"
)

// getDiskFreeSpace returns the available disk space in bytes for the cache path
func (c *Cache) getDiskFreeSpace() (int64, error) {
	var freeBytesAvailable, totalBytes, totalFreeBytes uint64

	pathPtr, err := windows.UTF16PtrFromString(c.basePath)
	if err != nil {
		return 0, err
	}

	err = windows.GetDiskFreeSpaceEx(
		pathPtr,
		&freeBytesAvailable,
		&totalBytes,
		&totalFreeBytes,
	)
	if err != nil {
		return 0, err
	}

	// Cap at max int64 to prevent overflow (>9 exabytes is unrealistic)
	if freeBytesAvailable > math.MaxInt64 {
		return math.MaxInt64, nil
	}
	return int64(freeBytesAvailable), nil
}
