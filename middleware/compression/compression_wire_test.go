package compression

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 本文件针对「预置头撤销」类缺陷做线路级回归测试。
//
// 为什么必须走真实线路（复用 compression_reproduce_test.go 的 raw conn 工具）：
// httptest.ResponseRecorder 的 Header() 是活 map 且不建模头提交语义——
// WriteHeaderNow 提交后头撤销即失效（头已固化上线），Recorder 下「撤销失败」
// 与「撤销过晚」均不可见；只有经真实 net/http server 读线路字节才能断言
// 客户端实际收到的头。

// wireGet 发起带 Accept-Encoding: gzip 的 GET，返回解析后的线路响应与 body 字节。
func wireGet(t *testing.T, ts *httptest.Server, path string) (*http.Response, []byte) {
	t.Helper()
	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	require.NoError(t, err, "应能连上真实 server")
	defer conn.Close()
	// 兜底 deadline：即使服务端行为异常也不会让测试悬挂
	require.NoError(t, conn.SetDeadline(time.Now().Add(10*time.Second)))

	rawReq := buildRawHTTPRequest("GET", path, map[string]string{
		"Accept-Encoding": "gzip",
		"Connection":      "close",
		"Host":            ts.Listener.Addr().String(),
	}, "")
	_, err = conn.Write(rawReq)
	require.NoError(t, err)

	resp, body, err := readHTTPResponse(t, conn)
	require.NoError(t, err)
	return resp, body
}

// TestCompression_RealHTTP_AbortWithStatus403_NoPresetHeaderLeak 回归测试：
// 中间件在 handler 运行前预置 Content-Encoding: gzip 与 Vary: Accept-Encoding，
// 依赖 Write 路径事后撤销。但 c.AbortWithStatus(403) 等无 body 路径直调
// WriteHeaderNow 提交响应头，无守卫直接转发时头一经提交撤销即失效，线路上出现
// 「403 + Content-Encoding: gzip + 空 body」的头-body 撕裂。
// WriteHeaderNow 在未决策时先撤销预置头、排空缓冲并转入 skip，再转发提交。
func TestCompression_RealHTTP_AbortWithStatus403_NoPresetHeaderLeak(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{MinLength: 1024}))
	r.GET("/forbidden", func(c *gin.Context) {
		c.AbortWithStatus(http.StatusForbidden)
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, body := wireGet(t, srv, "/forbidden")

	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Content-Encoding"),
		"P1-1: 无 body 的 403 不得让预置的 Content-Encoding: gzip 上线路（头-body 撕裂）")
	assert.Empty(t, resp.Header.Values("Vary"),
		"P1-1: 预置的 Vary: Accept-Encoding 应随撤销一并移除")
	assert.Empty(t, body, "AbortWithStatus(403) 应无响应体")
}

// TestCompression_RealHTTP_SkipPath_PreservesExternalVary 回归测试：
// 撤销预置头时 Vary 键是所有写入方共享的——Del("Vary") 会把外部组件
// （如 CORS 中间件）设置的 Vary: Origin 一并删除。因此 Vary 精确移除，
// 仅剔除本中间件添加的 Accept-Encoding 值，保留 Origin 等外部值。
// 小 body（< MinLength）走 skip 路径，撤销发生在 finish 中。
func TestCompression_RealHTTP_SkipPath_PreservesExternalVary(t *testing.T) {
	r := gin.New()
	r.Use(New(Config{MinLength: 1024}))
	r.GET("/cors", func(c *gin.Context) {
		// 模拟外部组件（如 CORS 中间件）追加的 Vary 值
		c.Writer.Header().Add("Vary", "Origin")
		c.String(http.StatusOK, "small body") // < MinLength → skip 路径
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, body := wireGet(t, srv, "/cors")

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "small body", string(body))
	assert.Empty(t, resp.Header.Get("Content-Encoding"), "skip 路径不应携带编码头")
	varyValues := resp.Header.Values("Vary")
	assert.Contains(t, varyValues, "Origin",
		"P1-2: 撤销预置头时必须保留外部组件设置的 Vary: Origin")
	assert.NotContains(t, varyValues, headerAcceptEncoding,
		"P1-2: 本中间件预置的 Vary: Accept-Encoding 应被精确移除")
}

// TestCompression_RealHTTP_PartialContent206_NotGzipped 回归测试：
// 206 的 body 若被 gzip 重写，Content-Range 仍指向原始字节偏移，
// Range 下载即被破坏。206 与 304 一并豁免压缩，
// 分片字节、Content-Range、Content-Length 原样上线路。
func TestCompression_RealHTTP_PartialContent206_NotGzipped(t *testing.T) {
	payload := bytes.Repeat([]byte("0123456789abcdef"), 64) // 1024 字节分片，≥ MinLength
	r := gin.New()
	r.Use(New(Config{MinLength: 64}))
	r.GET("/range", func(c *gin.Context) {
		c.Status(http.StatusPartialContent)
		c.Header("Content-Range", fmt.Sprintf("bytes 0-1023/%d", len(payload)))
		c.Header("Content-Length", "1024")
		_, _ = c.Writer.Write(payload)
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, body := wireGet(t, srv, "/range")

	require.Equal(t, http.StatusPartialContent, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Content-Encoding"),
		"P2-2: 206 分片响应必须豁免压缩，不得携带 Content-Encoding: gzip")
	assert.Equal(t, "bytes 0-1023/1024", resp.Header.Get("Content-Range"),
		"Content-Range 必须原样保留")
	assert.Equal(t, "1024", resp.Header.Get("Content-Length"),
		"分片定长应保留（未被压缩路径删除）")
	assert.Equal(t, payload, body,
		"P2-2: body 必须是未被 gzip 重写的原始分片字节，与 Content-Range 偏移一致")
}

// TestCompression_RealHTTP_DataFromReaderSmallBody_NoPresetHeaderLeak 回归测试：
// 消费方用 c.DataFromReader 流式服务静态文件，gin 的 render.Reader.Render 先设置
// Content-Length 再 io.Copy，首次 Write 命中 Content-Length 快速决策分支
// （body 长度 < MinLength）转入 skip 直通。skipAndWrite 的首次底层 Write 会触发
// net/http 提交响应头，若未先撤销预置头，Content-Encoding: gzip 即固化上线，
// 此后 finish 的撤销对线路无效——线路上出现「gzip 头 + 明文 body」的撕裂响应
// （浏览器 ERR_CONTENT_DECODING_FAILED）。
// 快速决策分支必须在 skipAndWrite 前 removeGzipHeaders（镜像 Write 状态门分支）。
func TestCompression_RealHTTP_DataFromReaderSmallBody_NoPresetHeaderLeak(t *testing.T) {
	// 526 字节明文 HTML，与生产复现证据一致（< MinLength=1024）
	body := []byte("<!doctype html>" + strings.Repeat("x", 504) + "</html>")
	require.Len(t, body, 526)

	r := gin.New()
	r.Use(New(Config{MinLength: 1024}))
	r.GET("/", func(c *gin.Context) {
		c.DataFromReader(http.StatusOK, int64(len(body)), "text/html; charset=utf-8",
			bytes.NewReader(body), nil)
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, gotBody := wireGet(t, srv, "/")

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Content-Encoding"),
		"P0: Content-Length 快速决策 skip 分支必须在响应头提交前撤销预置的 Content-Encoding: gzip")
	assert.Equal(t, body, gotBody,
		"P0: body 必须与写入的明文逐字节一致（线路上不得携带 gzip 头误导客户端解码）")
	assert.Equal(t, "526", resp.Header.Get("Content-Length"),
		"P0: 未压缩时 Content-Length 应保持明文字节数")
}

// TestCompression_RealHTTP_CompressedPath_SingleVaryAcceptEncoding 回归测试：
// 被压缩的响应 Vary 头必须恰好只含一个 Accept-Encoding 值。中间件在 handler
// 运行前已预置 Vary: Accept-Encoding，若决定压缩时再次追加，线路上即出现
// Vary: Accept-Encoding, Accept-Encoding 的重复值——RFC 9110 虽允许但冗余，
// 部分缓存/CDN 对重复 Vary 的处理未定义。
// 必须走真实线路：httptest.ResponseRecorder 的 Header map 是共享引用且不建模
// 头提交语义，重复值断言不可靠；只有读真实 server 线路字节才能断言客户端实际
// 收到的 Vary 值精确数量。
func TestCompression_RealHTTP_CompressedPath_SingleVaryAcceptEncoding(t *testing.T) {
	payload := strings.Repeat("orbit-compression-payload.", 32) // 832 字节，远 ≥ MinLength
	r := gin.New()
	r.Use(New(Config{MinLength: 64}))
	r.GET("/big", func(c *gin.Context) {
		c.String(http.StatusOK, payload) // 单次写入 ≥ MinLength → 压缩路径
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, body := wireGet(t, srv, "/big")

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "gzip", resp.Header.Get("Content-Encoding"),
		"足够大的响应应走压缩路径（前置条件，否则本断言无意义）")
	// 核心断言：Vary 必须恰好只含单个 Accept-Encoding 值。
	// 若 startCompress 再次追加 Vary（本测试防护的缺陷场景），线路值将为两个重复项。
	assert.Equal(t, []string{headerAcceptEncoding}, resp.Header.Values(headerVary),
		"压缩响应的 Vary 必须恰好只含一个 Accept-Encoding，不得重复")
	// 验证 body 确为合法 gzip 流且内容无损，坐实走的是压缩路径。
	assert.Equal(t, payload, string(gunzipBytes(t, body)))
}
