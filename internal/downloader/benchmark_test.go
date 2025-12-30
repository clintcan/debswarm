package downloader

import (
	"sync"
	"testing"
)

// BenchmarkBufferPool measures the performance gain from buffer pooling
func BenchmarkBufferPool(b *testing.B) {
	chunkSize := int64(4 * 1024 * 1024) // 4MB chunks

	b.Run("WithPool", func(b *testing.B) {
		pool := &sync.Pool{
			New: func() interface{} {
				buf := make([]byte, chunkSize)
				return &buf
			},
		}

		b.ResetTimer()
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			bufPtr := pool.Get().(*[]byte)
			buf := (*bufPtr)[:chunkSize]
			// Simulate some work
			buf[0] = byte(i)
			buf[len(buf)-1] = byte(i)
			pool.Put(bufPtr)
		}
	})

	b.Run("WithoutPool", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()

		for i := 0; i < b.N; i++ {
			buf := make([]byte, chunkSize)
			// Simulate some work
			buf[0] = byte(i)
			buf[len(buf)-1] = byte(i)
		}
	})
}

// BenchmarkBufferPoolParallel measures pool performance under concurrent load
func BenchmarkBufferPoolParallel(b *testing.B) {
	chunkSize := int64(4 * 1024 * 1024) // 4MB chunks

	b.Run("WithPool", func(b *testing.B) {
		pool := &sync.Pool{
			New: func() interface{} {
				buf := make([]byte, chunkSize)
				return &buf
			},
		}

		b.ResetTimer()
		b.ReportAllocs()

		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				bufPtr := pool.Get().(*[]byte)
				buf := (*bufPtr)[:chunkSize]
				buf[0] = 1
				buf[len(buf)-1] = 1
				pool.Put(bufPtr)
			}
		})
	})

	b.Run("WithoutPool", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()

		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				buf := make([]byte, chunkSize)
				buf[0] = 1
				buf[len(buf)-1] = 1
			}
		})
	})
}
