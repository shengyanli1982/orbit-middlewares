package compression

import (
	"bytes"
	"encoding/binary"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCompression_RealHTTP_MultiSegmentWrite_NoDataLoss AUD-01 回归测试（移植自审计 PoC-A）：
// 多段写入时，首段 <MinLength 进入缓冲区，次段 >=MinLength 触发压缩决策。
// 修复前次段直接 startCompress 而不冲刷缓冲区，已缓冲字节被静默丢弃。
// 修复后不变式：切换决策前必须先把 g.buf 排空到所选 sink，客户端收到完整且有序的字节。
func TestCompression_RealHTTP_MultiSegmentWrite_NoDataLoss(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{MinLength: 1024}))
	r.GET("/multi", func(c *gin.Context) {
		_, _ = c.Writer.Write(bytes.Repeat([]byte("A"), 100))  // <MinLength，进缓冲
		_, _ = c.Writer.Write(bytes.Repeat([]byte("B"), 2000)) // >=MinLength，触发压缩
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/multi", nil)
	require.NoError(t, err)
	req.Header.Set("Accept-Encoding", "gzip")

	resp, err := newHTTPClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "gzip", resp.Header.Get("Content-Encoding"))

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	want := bytes.Join([][]byte{bytes.Repeat([]byte("A"), 100), bytes.Repeat([]byte("B"), 2000)}, nil)
	got := gunzipBytes(t, raw)
	assert.Equal(t, want, got,
		"buffered first segment must not be dropped when a later segment triggers compression")
}

// TestCompression_RealHTTP_ErrorAfterBuffered_OrderPreserved AUD-06 回归测试（移植自审计 PoC-G）：
// 首段 <MinLength 进缓冲（状态 200），随后 handler 以 5xx 直写错误体。
// 修复前错误路径不排空缓冲，finish 又把缓冲追加到末尾，客户端看到 body="BOOM"+"AAA…"（乱序）。
// 修复后：切换直通前先把缓冲排空到 ResponseWriter，字节顺序与 handler 写入顺序一致、零丢失。
func TestCompression_RealHTTP_ErrorAfterBuffered_OrderPreserved(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{MinLength: 1024}))
	r.GET("/err", func(c *gin.Context) {
		_, _ = c.Writer.Write(bytes.Repeat([]byte("A"), 100)) // 缓冲（状态仍 200）
		c.String(http.StatusInternalServerError, "BOOM")      // 错误路径直写
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/err", nil)
	require.NoError(t, err)
	req.Header.Set("Accept-Encoding", "gzip")

	resp, err := newHTTPClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Content-Encoding"),
		"error response must not carry Content-Encoding: gzip")

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	want := string(bytes.Repeat([]byte("A"), 100)) + "BOOM"
	assert.Equal(t, want, string(body),
		"buffered bytes must precede the error body, matching handler write order")
}

// TestCompression_RealHTTP_LargeCompressed_ChunkedNoContentLength AUD-14 特征测试（移植自审计 PoC-A2b）：
// 压缩路径 finish 的 Close 触发首次真实写入时 net/http 已固化响应头（WriteHeader 时克隆 header map），
// 之后任何 Set("Content-Length") 在真实线路上都不生效：压缩输出 >2KB 必然 chunked。
// 本测试钉住被接受的行为：大压缩响应无 Content-Length、走 chunked、载荷经 gzip 后完整无损。
// （<2KB 的小压缩响应由 net/http 在 handler 结束时按缓冲长度自动补 CL，与中间件无关。）
func TestCompression_RealHTTP_LargeCompressed_ChunkedNoContentLength(t *testing.T) {
	payload := make([]byte, 300*1024)
	rng := rand.New(rand.NewPCG(42, 7)) // 固定种子，可复现
	for i := 0; i+8 <= len(payload); i += 8 {
		binary.LittleEndian.PutUint64(payload[i:], rng.Uint64())
	}
	copy(payload, []byte("ORBITMW!")) // 确保首两字节非 gzip 魔数，避免误入透传分支

	r := gin.New()
	r.Use(New(Config{MinLength: 1}))
	r.GET("/big", func(c *gin.Context) {
		c.Data(http.StatusOK, "application/octet-stream", payload)
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/big", nil)
	require.NoError(t, err)
	req.Header.Set("Accept-Encoding", "gzip")

	resp, err := newHTTPClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "gzip", resp.Header.Get("Content-Encoding"))

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, payload, gunzipBytes(t, raw), "payload must survive gzip round-trip byte-for-byte")
	assert.Empty(t, resp.Header.Get("Content-Length"))
	assert.Equal(t, int64(-1), resp.ContentLength,
		"large compressed response is framed as chunked; Content-Length is absent by design")
	t.Logf("TransferEncoding=%v rawLen=%d", resp.TransferEncoding, len(raw))
}

// TestCompression_RealHTTP_NotModified304_ETagPreserved AUD-15 回归测试（移植自审计 PoC-J）：
// 无 body 响应（304）走非压缩直通路径，修复前 finish 的 removeGzipHeaders 无差别
// Del("ETag")，剥离 handler 自设的校验器，违反 RFC 7232 §4.1（304 应携带 ETag）。
// 修复后：非压缩路径不触碰 handler 自设 ETag（弱化仅发生在实际压缩路径）。
func TestCompression_RealHTTP_NotModified304_ETagPreserved(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{MinLength: 1024}))
	r.GET("/cached", func(c *gin.Context) {
		c.Header("ETag", `"v1"`)
		c.Status(http.StatusNotModified)
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/cached", nil)
	require.NoError(t, err)
	req.Header.Set("Accept-Encoding", "gzip")

	resp, err := newHTTPClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusNotModified, resp.StatusCode)
	assert.Equal(t, `"v1"`, resp.Header.Get("ETag"),
		"RFC 7232 §4.1: a 304 must carry the validator; the passthrough path must not strip or weaken it")
	assert.Empty(t, resp.Header.Get("Content-Encoding"),
		"bodyless 304 must not carry the middleware's pre-set Content-Encoding")
}

// TestCompression_RealHTTP_FlushBeforeDecision_PlainBodyNoGzipHeader P2-6 回归测试：
// 决策前（buf 仍在累积、尚未决定压缩与否）调用 Flush，修复前会把底层响应头
// 连同中间件预设置的 Content-Encoding: gzip 一并提交；此后写入触发压缩决策时
// 编码头已无法变更，线路上出现"头/体编码不一致"（缓冲明文字节被后续 gzip 流
// 裹挟，或明文 body 顶着 gzip 头上线路），且 Flush 前的缓冲字节未按流式语义
// 即时交付。修复后：决策前 Flush 先移除预设置压缩头、排空缓冲直通写出并转入
// 永久 skip 决策（流式响应本就无法安全压缩），响应不携带 Content-Encoding，
// body 为按写入顺序的完整明文。
func TestCompression_RealHTTP_FlushBeforeDecision_PlainBodyNoGzipHeader(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{MinLength: 1024}))
	r.GET("/stream", func(c *gin.Context) {
		_, _ = c.Writer.Write(bytes.Repeat([]byte("A"), 100))  // <MinLength，进缓冲
		c.Writer.Flush()                                       // 决策前 Flush：修复前提交预设置 gzip 响应头
		_, _ = c.Writer.Write(bytes.Repeat([]byte("B"), 2000)) // >=MinLength，修复前触发压缩决策
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/stream", nil)
	require.NoError(t, err)
	req.Header.Set("Accept-Encoding", "gzip")

	resp, err := newHTTPClient().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Empty(t, resp.Header.Get("Content-Encoding"),
		"决策前 Flush 必须转入直通决策，响应不得携带 Content-Encoding")

	want := append(bytes.Repeat([]byte("A"), 100), bytes.Repeat([]byte("B"), 2000)...)
	assert.Equal(t, want, body,
		"body 必须为按写入顺序的完整明文（缓冲字节零丢失、编码与头一致）")
}
