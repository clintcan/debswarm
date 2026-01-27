//go:build !windows

package cache

import "syscall"

// getDiskFreeSpace returns the available disk space in bytes for the cache path
func (c *Cache) getDiskFreeSpace() (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(c.basePath, &stat); err != nil {
		return 0, err
	}
	// Available blocks * block size
	// Both conversions needed for cross-platform compatibility (Bsize is int32 on arm, int64 on amd64)
	// #nosec G115 -- overflow would require >9 exabytes free space, which is unrealistic
	return int64(stat.Bavail) * int64(stat.Bsize), nil //nolint:unconvert
}
