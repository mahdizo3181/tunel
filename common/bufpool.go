package common

import "sync"

// copyBufSize is the chunk size used for splicing streams. 32KiB balances
// syscall overhead against per-connection memory footprint under high
// concurrency.
const copyBufSize = 32 * 1024

var bufPool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, copyBufSize)
		return &b
	},
}

func getBuf() *[]byte {
	return bufPool.Get().(*[]byte)
}

func putBuf(b *[]byte) {
	bufPool.Put(b)
}
