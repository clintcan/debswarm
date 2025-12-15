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
	return int64(stat.Bavail) * stat.Bsize, nil
}
