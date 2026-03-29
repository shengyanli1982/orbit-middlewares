package compression

import (
	"io"

	"github.com/klauspost/compress/zstd"
)

type zstdWriter struct {
	encoder *zstd.Encoder
	size    int64
	level   zstd.EncoderLevel
}

func newZstdWriter() *zstdWriter {
	enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	return &zstdWriter{
		encoder: enc,
		level:   zstd.SpeedDefault,
	}
}

func (z *zstdWriter) Reset(w io.Writer) {
	z.encoder.Reset(w)
	z.size = 0
}

func (z *zstdWriter) ResetLevel(level int) {
	encLevel := zstd.EncoderLevelFromZstd(level)

	if z.encoder == nil {
		z.encoder, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(encLevel))
	} else {
		z.encoder.Reset(nil)
		z.encoder, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(encLevel))
	}
	z.level = encLevel
	z.size = 0
}

func (z *zstdWriter) Write(data []byte) (int, error) {
	n, err := z.encoder.Write(data)
	z.size += int64(n)
	return n, err
}

func (z *zstdWriter) Close() error {
	return z.encoder.Close()
}

func (z *zstdWriter) Flush() error {
	return z.encoder.Flush()
}

func (z *zstdWriter) CompressedSize() int64 {
	return z.size
}

func init() {
	zstdPool.New = func() interface{} {
		return newZstdWriter()
	}
}
