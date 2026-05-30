package compression

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"golang.org/x/sync/errgroup"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func newHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DisableCompression: true,
			MaxIdleConns:        200,
			MaxIdleConnsPerHost: 200,
			IdleConnTimeout:     0,
		},
	}
}

func TestCompression_ConcurrentRequests(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{MinLength: 1024}))

	smallData := strings.Repeat("a", 100)
	largeData := strings.Repeat("b", 2048)

	r.GET("/small", func(c *gin.Context) {
		c.String(http.StatusOK, smallData)
	})
	r.GET("/large", func(c *gin.Context) {
		c.String(http.StatusOK, largeData)
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	client := newHTTPClient()

	var wg sync.WaitGroup
	g, ctx := errgroup.WithContext(t.Context())

	for i := 0; i < 50; i++ {
		wg.Add(1)
		g.Go(func() error {
			defer wg.Done()
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			req, err := http.NewRequest(http.MethodGet, srv.URL+"/small", nil)
			if err != nil {
				return err
			}
			req.Header.Set("Accept-Encoding", "gzip")

			resp, err := client.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("small: unexpected status %d", resp.StatusCode)
			}
			if enc := resp.Header.Get("Content-Encoding"); enc != "" {
				return fmt.Errorf("small: unexpected Content-Encoding: %s", enc)
			}

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return err
			}
			if string(body) != smallData {
				return fmt.Errorf("small: body mismatch")
			}
			return nil
		})

		wg.Add(1)
		g.Go(func() error {
			defer wg.Done()
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			req, err := http.NewRequest(http.MethodGet, srv.URL+"/large", nil)
			if err != nil {
				return err
			}
			req.Header.Set("Accept-Encoding", "gzip")

			resp, err := client.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("large: unexpected status %d", resp.StatusCode)
			}
			if enc := resp.Header.Get("Content-Encoding"); enc != "gzip" {
				return fmt.Errorf("large: expected Content-Encoding: gzip, got %q", enc)
			}

			gr, err := gzip.NewReader(resp.Body)
			if err != nil {
				return fmt.Errorf("large: gzip.NewReader: %w", err)
			}
			defer gr.Close()

			body, err := io.ReadAll(gr)
			if err != nil {
				return fmt.Errorf("large: read gzip: %w", err)
			}
			if string(body) != largeData {
				return fmt.Errorf("large: body mismatch after decompress")
			}
			return nil
		})
	}

	wg.Wait()
	assert.NoError(t, g.Wait())
}

func TestCompression_LargeResponse_RealHTTP(t *testing.T) {
	originalData := bytes.Repeat([]byte("ABCDEFGHIJKLMNOPQRSTUVWX"), (1024*1024)/24)
	if len(originalData) < 1024*1024 {
		originalData = append(originalData, bytes.Repeat([]byte("Z"), 1024*1024-len(originalData))...)
	}
	assert.Equal(t, 1024*1024, len(originalData))

	r := gin.New()
	r.Use(New(Config{MinLength: 1024}))
	r.GET("/test", func(c *gin.Context) {
		c.Data(http.StatusOK, "application/octet-stream", originalData)
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	client := newHTTPClient()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/test", nil)
	assert.NoError(t, err)
	req.Header.Set("Accept-Encoding", "gzip")

	resp, err := client.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "gzip", resp.Header.Get("Content-Encoding"),
		"1MB response must have Content-Encoding: gzip")

	gr, err := gzip.NewReader(resp.Body)
	assert.NoError(t, err)
	defer gr.Close()

	decompressed, err := io.ReadAll(gr)
	assert.NoError(t, err)
	assert.True(t, bytes.Equal(originalData, decompressed),
		"decompressed 1MB data must match original byte-for-byte")
}

func TestCompression_StreamingFlush_RealHTTP(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{MinLength: 1024}))
	r.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, strings.Repeat("A", 100))
		c.Writer.Flush()
		c.String(http.StatusOK, strings.Repeat("B", 100))
		c.Writer.Flush()
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	client := newHTTPClient()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/test", nil)
	assert.NoError(t, err)
	req.Header.Set("Accept-Encoding", "gzip")

	resp, err := client.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	assert.NoError(t, err, "full response body must be readable without corruption")
	assert.Equal(t, 200, len(body), "body must contain both 100-byte writes")
	assert.Equal(t, strings.Repeat("A", 100)+strings.Repeat("B", 100), string(body))
}

func TestCompression_MemoryPressure(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{MinLength: 1024}))
	payload := strings.Repeat("M", 5*1024)
	r.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, payload)
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	client := newHTTPClient()

	warmup, err := http.NewRequest(http.MethodGet, srv.URL+"/test", nil)
	assert.NoError(t, err)
	warmup.Header.Set("Accept-Encoding", "gzip")
	for i := 0; i < 5; i++ {
		resp, err := client.Do(warmup)
		assert.NoError(t, err)
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	runtime.GC()
	baseline := runtime.NumGoroutine()

	var wg sync.WaitGroup
	g, _ := errgroup.WithContext(t.Context())
	for i := 0; i < 50; i++ {
		wg.Add(1)
		g.Go(func() error {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodGet, srv.URL+"/test", nil)
			if err != nil {
				return err
			}
			req.Header.Set("Accept-Encoding", "gzip")

			resp, err := client.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return err
			}
			if len(body) <= 0 {
				return fmt.Errorf("empty body")
			}
			return nil
		})
	}
	wg.Wait()
	assert.NoError(t, g.Wait())

	srv.Close()

	runtime.GC()
	debug.FreeOSMemory()
	runtime.GC()

	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	final := runtime.NumGoroutine()
	stack := debug.Stack()
	activeGoroutines := bytes.Count(stack, []byte("goroutine "))
	assert.Greater(t, activeGoroutines, 0)

	assert.LessOrEqual(t, final, baseline+5,
		"goroutine leak detected: baseline=%d final=%d", baseline, final)
}

func TestCompression_HandlerPresetContentEncoding_RealHTTP(t *testing.T) {
	originalMessage := "this is pre-gzipped data that must not be double-compressed"

	var preCompressed bytes.Buffer
	gzw := gzip.NewWriter(&preCompressed)
	_, err := gzw.Write([]byte(originalMessage))
	assert.NoError(t, err)
	assert.NoError(t, gzw.Close())
	compressedBytes := preCompressed.Bytes()

	r := gin.New()
	r.Use(New(Config{MinLength: 50}))
	r.GET("/test", func(c *gin.Context) {
		c.Header("Content-Encoding", "gzip")
		c.Data(http.StatusOK, "application/octet-stream", compressedBytes)
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	client := newHTTPClient()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/test", nil)
	assert.NoError(t, err)
	req.Header.Set("Accept-Encoding", "gzip")

	resp, err := client.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "gzip", resp.Header.Get("Content-Encoding"),
		"pre-set Content-Encoding must be preserved")

	rawBody, err := io.ReadAll(resp.Body)
	assert.NoError(t, err,
		"client must receive a valid (non-double-compressed) gzip body")

	gr, err := gzip.NewReader(bytes.NewReader(rawBody))
	assert.NoError(t, err, "raw body must be valid gzip (not double-compressed)")
	defer gr.Close()

	decompressed, err := io.ReadAll(gr)
	assert.NoError(t, err)
	assert.Equal(t, originalMessage, string(decompressed),
		"decompressed content must equal the original message")
}

func TestCompression_ErrorNoBody_RealHTTP(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{MinLength: 1024}))
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusBadRequest, nil)
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	client := newHTTPClient()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/test", nil)
	assert.NoError(t, err)
	req.Header.Set("Accept-Encoding", "gzip")

	resp, err := client.Do(req)
	assert.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Content-Encoding"),
		"small error body must not carry Content-Encoding: gzip")

	body, err := io.ReadAll(resp.Body)
	assert.NoError(t, err)
	assert.Equal(t, "null", string(body),
		"c.JSON(400, nil) must serialize to 4-byte 'null'")
}

func BenchmarkCompression_ConcurrentRequests(b *testing.B) {
	r := gin.New()
	r.Use(New(Config{MinLength: 1024}))
	payload := strings.Repeat("X", 5*1024)
	r.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, payload)
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		client := newHTTPClient()
		for pb.Next() {
			req, err := http.NewRequest(http.MethodGet, srv.URL+"/test", nil)
			if err != nil {
				b.Fatal(err)
			}
			req.Header.Set("Accept-Encoding", "gzip")

			resp, err := client.Do(req)
			if err != nil {
				b.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	})
}

func BenchmarkCompression_LargeResponse_RealHTTP(b *testing.B) {
	largeData := bytes.Repeat([]byte("benchmark-data-256kb"), (256*1024)/20)
	r := gin.New()
	r.Use(New(Config{MinLength: 1024}))
	r.GET("/test", func(c *gin.Context) {
		c.Data(http.StatusOK, "application/octet-stream", largeData)
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	b.SetBytes(int64(len(largeData)))
	b.ResetTimer()
	b.ReportAllocs()

	client := newHTTPClient()
	for i := 0; i < b.N; i++ {
		req, err := http.NewRequest(http.MethodGet, srv.URL+"/test", nil)
		if err != nil {
			b.Fatal(err)
		}
		req.Header.Set("Accept-Encoding", "gzip")

		resp, err := client.Do(req)
		if err != nil {
			b.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}

