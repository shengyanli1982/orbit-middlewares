package compression

import (
	"compress/gzip"
	"io"
)

type gzipWriter struct {
	writer *gzip.Writer
	size   int64
	level  int
}

func newGzipWriter() *gzipWriter {
	w, _ := gzip.NewWriterLevel(io.Discard, DefaultCompression)
	return &gzipWriter{
		writer: w,
		level:  DefaultCompression,
	}
}

func (g *gzipWriter) Reset(w io.Writer) {
	g.writer.Reset(w)
	g.size = 0
}

func (g *gzipWriter) ResetLevel(level int) {
	if g.writer == nil {
		g.writer, _ = gzip.NewWriterLevel(io.Discard, level)
	} else {
		g.writer.Reset(io.Discard)
		if level != g.level {
			g.writer, _ = gzip.NewWriterLevel(io.Discard, level)
			g.level = level
		}
	}
	g.size = 0
}

func (g *gzipWriter) Write(data []byte) (int, error) {
	n, err := g.writer.Write(data)
	g.size += int64(n)
	return n, err
}

func (g *gzipWriter) Close() error {
	return g.writer.Close()
}

func (g *gzipWriter) Flush() error {
	return g.writer.Flush()
}

func (g *gzipWriter) CompressedSize() int64 {
	return g.size
}

func init() {
	gzipPool.New = func() interface{} {
		return newGzipWriter()
	}
}
