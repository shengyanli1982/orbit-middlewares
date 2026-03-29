package compression

import (
	"io"

	"github.com/andybalholm/brotli"
)

type brotliWriter struct {
	writer *brotli.Writer
	size   int64
	level  int
}

func newBrotliWriter() *brotliWriter {
	w := brotli.NewWriterLevel(io.Discard, brotli.DefaultCompression)
	return &brotliWriter{
		writer: w,
		level:  brotli.DefaultCompression,
	}
}

func (b *brotliWriter) Reset(w io.Writer) {
	b.writer.Reset(w)
	b.size = 0
}

func (b *brotliWriter) ResetLevel(level int) {
	if b.writer == nil {
		b.writer = brotli.NewWriterLevel(io.Discard, level)
	} else {
		b.writer.Reset(io.Discard)
		if level != b.level {
			b.writer = brotli.NewWriterLevel(io.Discard, level)
			b.level = level
		}
	}
	b.size = 0
}

func (b *brotliWriter) Write(data []byte) (int, error) {
	n, err := b.writer.Write(data)
	b.size += int64(n)
	return n, err
}

func (b *brotliWriter) Close() error {
	return b.writer.Close()
}

func (b *brotliWriter) Flush() error {
	return b.writer.Flush()
}

func (b *brotliWriter) CompressedSize() int64 {
	return b.size
}

func init() {
	brotliPool.New = func() interface{} {
		return newBrotliWriter()
	}
}
