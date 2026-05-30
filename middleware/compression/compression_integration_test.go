package compression

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// simResponseBodyWriter 模拟 orbit 的 BodyBuffer ResponseBodyWriter
// 同时写入 buffer 和 gin.responseWriter（与真实 BodyBuffer 行为一致）
type simResponseBodyWriter struct {
	gin.ResponseWriter
	buffer *bytes.Buffer
}

func (w *simResponseBodyWriter) Write(b []byte) (int, error) {
	w.buffer.Write(b)
	return w.ResponseWriter.Write(b)
}

func (w *simResponseBodyWriter) WriteString(s string) (int, error) {
	w.buffer.WriteString(s)
	return w.ResponseWriter.WriteString(s)
}

// simBodyBuffer 是 orbit 引擎 BodyBuffer 中间件的简化版
func simBodyBuffer() gin.HandlerFunc {
	return func(c *gin.Context) {
		buf := &bytes.Buffer{}
		bw := &simResponseBodyWriter{ResponseWriter: c.Writer, buffer: buf}
		orig := c.Writer
		c.Writer = bw
		defer func() {
			c.Writer = orig
		}()
		c.Next()
	}
}

// TestIntegration_BodyBufferPlusCompression_SmallJSON
// 模拟 orbit BodyBuffer + 我们的 compression + 小 JSON 响应
// 验证真实 HTTP 响应不包含重复数据（"Extra data" error）
func TestIntegration_BodyBufferPlusCompression_SmallJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	// 1. BodyBuffer 在 compression 之前（与 orbit 引擎一致）
	r.Use(simBodyBuffer())
	// 2. compression 中间件
	r.Use(New(Config{MinLength: 1024}))

	r.POST("/tdx/v1/base/query/GetSecurityList/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"errorCode":    0,
			"errorMessage": "success",
			"data": gin.H{
				"count": 5,
				"stocks": []gin.H{
					{"code": "000001", "name": "平安银行"},
					{"code": "000002", "name": "万科A"},
				},
			},
		})
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	client := &http.Client{
		Transport: &http.Transport{
			DisableCompression: true, // 不让 client 自动解压，看到原始响应
		},
	}

	// 发送带 Accept-Encoding: gzip 的请求（Python requests 默认行为）
	req, err := http.NewRequest("POST", srv.URL+"/tdx/v1/base/query/GetSecurityList/",
		strings.NewReader(`{"market":0,"start":0}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	ce := resp.Header.Get("Content-Encoding")
	t.Logf("Response headers: Content-Encoding=%q, Content-Length=%q", ce, resp.Header.Get("Content-Length"))
	t.Logf("Transfer-Encoding: %v", resp.Header.Values("Transfer-Encoding"))

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	t.Logf("Response body (%d bytes): %q", len(body), string(body))

	// 关键断言：响应体必须是有效 JSON（不多不少正好一个对象）
	var parsed map[string]interface{}
	err = json.Unmarshal(body, &parsed)
	require.NoError(t, err, "Response body must be valid JSON (single object): %q", string(body))

	// 验证是单个 JSON，没有 "Extra data"
	dec := json.NewDecoder(bytes.NewReader(body))
	var parsed2 map[string]interface{}
	err = dec.Decode(&parsed2)
	require.NoError(t, err)
	if dec.More() {
		rest, _ := io.ReadAll(dec.Buffered())
		t.Fatalf("Response has EXTRA JSON data after first object: %q", string(rest))
	}
}

// TestIntegration_BodyBufferPlusCompression_LargeJSON
// 模拟 BodyBuffer + compression + 大 JSON 响应（会触发真实 gzip 压缩）
func TestIntegration_BodyBufferPlusCompression_LargeJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	r.Use(simBodyBuffer())
	r.Use(New(Config{MinLength: 1024}))

	// 模拟 GetSecurityList 返回大量股票列表
	r.POST("/tdx/v1/base/query/GetSecurityList/", func(c *gin.Context) {
		stocks := make([]gin.H, 0, 1000)
		for i := 0; i < 1000; i++ {
			stocks = append(stocks, gin.H{
				"code":         fmt.Sprintf("%06d", i),
				"name":         fmt.Sprintf("股票%d", i),
				"decimalPoint": 2,
				"preClose":     10.5,
				"volumeUnit":   100,
			})
		}
		c.JSON(http.StatusOK, gin.H{
			"errorCode":    0,
			"errorMessage": "success",
			"data": gin.H{
				"count":      1000,
				"noParseCount": 0,
				"data":       stocks,
			},
		})
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	client := &http.Client{
		Transport: &http.Transport{
			DisableCompression: true,
		},
	}

	req, _ := http.NewRequest("POST", srv.URL+"/tdx/v1/base/query/GetSecurityList/",
		strings.NewReader(`{"market":0,"start":0}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	t.Logf("Response: Status=%d, CE=%q, CL=%q", resp.StatusCode,
		resp.Header.Get("Content-Encoding"),
		resp.Header.Get("Content-Length"))

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	// 如果响应有 Content-Encoding: gzip，先解压
	var decoded []byte
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gr, err := gzip.NewReader(bytes.NewReader(body))
		require.NoError(t, err)
		defer gr.Close()
		decoded, err = io.ReadAll(gr)
		require.NoError(t, err, "gzip decompression failed, body: %q", string(body))
	} else {
		decoded = body
	}

	t.Logf("Decoded body size: %d bytes", len(decoded))

	var parsed map[string]interface{}
	err = json.Unmarshal(decoded, &parsed)
	require.NoError(t, err, "Decoded body must be valid JSON: %s", string(decoded[:min(500, len(decoded))]))

	// 验证没有 extra data
	dec := json.NewDecoder(bytes.NewReader(decoded))
	err = dec.Decode(&parsed)
	require.NoError(t, err)
	if dec.More() {
		t.Fatalf("Response has EXTRA JSON after first object! This is the exact bug the user reports.")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestIntegration_BodyBufferPlusCompression_KeepAliveMultipleRequests
// 模拟 Python requests 在同一 keep-alive 连接上连续请求
// 复现 HTTP/1.1 connection reuse 场景
func TestIntegration_BodyBufferPlusCompression_KeepAliveMultipleRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	r.Use(simBodyBuffer())
	r.Use(New(Config{MinLength: 1024}))

	// 处理不同 start 偏移的股票列表请求
	r.POST("/tdx/v1/base/query/GetSecurityList/", func(c *gin.Context) {
		var req struct {
			Market int `json:"market"`
			Start  int `json:"start"`
		}
		c.BindJSON(&req)

		// 根据 start 返回不同数量的股票（模拟真实 TDX 行为）
		numStocks := 10
		if req.Start < 100 {
			numStocks = 100 // 初始请求返回更多
		} else if req.Start >= 1000 {
			numStocks = 0 // 没有更多股票
		}

		stocks := make([]gin.H, 0, numStocks)
		for i := 0; i < numStocks; i++ {
			stocks = append(stocks, gin.H{
				"code":         fmt.Sprintf("%06d", req.Start+i),
				"name":         fmt.Sprintf("股票%d", req.Start+i),
				"decimalPoint": 2,
				"preClose":     0.0, // This was NaN before SDK fix!
				"volumeUnit":   100,
			})
		}

		c.JSON(http.StatusOK, gin.H{
			"errorCode":    0,
			"errorMessage": "success",
			"data": gin.H{
				"count":        numStocks,
				"noParseCount": 0,
				"data":         stocks,
			},
		})
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	// 使用 http.Client with keep-alive (默认)
	transport := &http.Transport{
		DisableCompression:  true,
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     60 * 1000000000,
	}
	client := &http.Client{Transport: transport}

	// 模拟 Python stocktrader2 客户端的连续分页请求
	for start := 0; start <= 2000; start += 100 {
		body := fmt.Sprintf(`{"market":0,"start":%d}`, start)
		req, _ := http.NewRequest("POST", srv.URL+"/tdx/v1/base/query/GetSecurityList/",
			strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept-Encoding", "gzip, deflate")

		resp, err := client.Do(req)
		require.NoError(t, err, "start=%d request failed", start)

		rawBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.NoError(t, err, "start=%d body read failed", start)

		var decoded []byte
		ce := resp.Header.Get("Content-Encoding")
		if ce == "gzip" {
			gr, err := gzip.NewReader(bytes.NewReader(rawBody))
			require.NoError(t, err, "start=%d gzip reader failed", start)
			decoded, err = io.ReadAll(gr)
			gr.Close()
			require.NoError(t, err, "start=%d decompress failed, raw=%q", start, string(rawBody))
		} else {
			decoded = rawBody
		}

		// 用 json.Decoder 严格检测 extra data
		dec := json.NewDecoder(bytes.NewReader(decoded))
		var obj map[string]interface{}
		err = dec.Decode(&obj)
		require.NoError(t, err, "start=%d: decoded body not valid JSON: %q",
			start, string(decoded[:min(200, len(decoded))]))

		if dec.More() {
			// 读取剩余数据看是什么
			rest, _ := io.ReadAll(dec.Buffered())
			all, _ := io.ReadAll(bytes.NewReader(decoded))
			t.Fatalf("start=%d: EXTRA data detected! After first JSON object, remaining: %q\nFull decoded: %q",
				start, string(rest), string(all))
		}

		t.Logf("start=%d: OK (status=%d, CE=%q, decoded=%d bytes)", start, resp.StatusCode, ce, len(decoded))
	}
}
