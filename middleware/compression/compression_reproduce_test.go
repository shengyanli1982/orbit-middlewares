package compression

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	gin.SetMode(gin.ReleaseMode)
}

var reproBytesBufferPool = sync.Pool{
	New: func() interface{} { return &bytes.Buffer{} },
}

type reproResponseBodyWriter struct {
	gin.ResponseWriter
	buffer *bytes.Buffer
}

func (w *reproResponseBodyWriter) Write(b []byte) (int, error) {
	w.buffer.Write(b)
	return w.ResponseWriter.Write(b)
}

func reproBodyBuffer() gin.HandlerFunc {
	return func(c *gin.Context) {
		respBodyBuffer := reproBytesBufferPool.Get().(*bytes.Buffer)
		respBodyBuffer.Reset()
		bw := &reproResponseBodyWriter{ResponseWriter: c.Writer, buffer: respBodyBuffer}
		orig := c.Writer
		c.Writer = bw
		defer func() {
			c.Writer = orig
			respBodyBuffer.Reset()
			reproBytesBufferPool.Put(respBodyBuffer)
		}()
		c.Next()
	}
}

func smallJSONHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		stocks := make([]gin.H, 0, 50)
		for i := range 50 {
			stocks = append(stocks, gin.H{
				"code":         fmt.Sprintf("%06d", i),
				"name":         fmt.Sprintf("股票%d", i),
				"decimalPoint": 2,
				"preClose":     10.5,
				"volumeUnit":   100,
			})
		}
		c.JSON(200, gin.H{
			"errorCode":    0,
			"errorMessage": "success",
			"data": gin.H{
				"count":      50,
				"noParseCount": 0,
				"data":       stocks,
			},
		})
	}
}

func largeJSONHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		stocks := make([]gin.H, 0, 200)
		for i := range 200 {
			stocks = append(stocks, gin.H{
				"code":         fmt.Sprintf("%06d", i),
				"name":         fmt.Sprintf("股票%d号证券名称比较长用于测试压缩效果", i),
				"decimalPoint": 2,
				"preClose":     10.5,
				"volumeUnit":   100,
			})
		}
		c.JSON(200, gin.H{
			"errorCode":    0,
			"errorMessage": "success",
			"data": gin.H{
				"count":      200,
				"noParseCount": 0,
				"data":       stocks,
			},
		})
	}
}

func tinyHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.String(200, "ok")
	}
}

func setupRouter(handler gin.HandlerFunc) *gin.Engine {
	r := gin.New()
	r.Use(reproBodyBuffer())
	r.Use(New(Config{MinLength: 1024}))
	r.POST("/tdx/v1/base/query/GetSecurityList/", handler)
	r.POST("/small", handler)
	r.POST("/tiny", handler)
	return r
}

type debugResponse struct {
	StatusCode    int
	ContentEncoding string
	ContentLength   string
	TransferEncoding string
	RawBodyLen    int
	RawBodyPreview []byte
	DecompressedBody []byte
	JSONParseOK   bool
	ExtraData     bool
	ExtraDataDetail string
	FullBodyHex   string
}

func inspectResponse(t *testing.T, resp *http.Response) debugResponse {
	t.Helper()
	dr := debugResponse{
		StatusCode:      resp.StatusCode,
		ContentEncoding: resp.Header.Get("Content-Encoding"),
		ContentLength:   resp.Header.Get("Content-Length"),
		TransferEncoding: strings.Join(resp.TransferEncoding, ","),
	}

	rawBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	resp.Body.Close()
	dr.RawBodyLen = len(rawBody)

	if len(rawBody) > 100 {
		dr.RawBodyPreview = rawBody[:100]
	} else {
		dr.RawBodyPreview = rawBody
	}

	dumpLen := len(rawBody)
	if dumpLen > 200 {
		dumpLen = 200
	}
	dr.FullBodyHex = hex.EncodeToString(rawBody[:dumpLen])

	var bodyBytes []byte
	if dr.ContentEncoding == "gzip" {
		if len(rawBody) >= 2 {
			gz, gerr := gzip.NewReader(bytes.NewReader(rawBody))
			if gerr != nil {
				dr.ExtraDataDetail = fmt.Sprintf("gzip.NewReader error: %v", gerr)
				return dr
			}
			decompressed, derr := io.ReadAll(gz)
			gz.Close()
			if derr != nil {
				dr.ExtraDataDetail = fmt.Sprintf("gzip read error: %v", derr)
				return dr
			}
			bodyBytes = decompressed
			dr.DecompressedBody = bodyBytes
		}
	} else {
		bodyBytes = rawBody
	}

	if len(bodyBytes) > 0 {
		var js json.RawMessage
		if err := json.Unmarshal(bodyBytes, &js); err != nil {
			dr.JSONParseOK = false
			dr.ExtraDataDetail = fmt.Sprintf("json.Unmarshal error: %v", err)
		} else {
			dr.JSONParseOK = true
		}

		dec := json.NewDecoder(bytes.NewReader(bodyBytes))
		_ = dec.Decode(&json.RawMessage{})
		dr.ExtraData = dec.More()
		if dr.ExtraData {
			extraStart := dec.InputOffset()
			remaining := bodyBytes[extraStart:]
			dr.ExtraDataDetail = fmt.Sprintf("json.Decoder.More()=true at offset %d, remaining bytes: %d, remaining preview: %s",
				extraStart, len(remaining), string(remaining[:min(len(remaining), 100)]))
		}
	}

	return dr
}

func printDebug(t *testing.T, dr debugResponse) {
	t.Helper()
	t.Logf("=== DEBUG RESPONSE ===")
	t.Logf("  StatusCode:       %d", dr.StatusCode)
	t.Logf("  Content-Encoding: %s", dr.ContentEncoding)
	t.Logf("  Content-Length:   %s", dr.ContentLength)
	t.Logf("  Transfer-Encoding: %s", dr.TransferEncoding)
	t.Logf("  RawBodyLen:       %d", dr.RawBodyLen)
	t.Logf("  RawBodyPreview(first <=100 bytes): %q", dr.RawBodyPreview)
	if dr.DecompressedBody != nil {
		previewLen := len(dr.DecompressedBody)
		if previewLen > 100 {
			previewLen = 100
		}
		t.Logf("  DecompressedLen:  %d", len(dr.DecompressedBody))
		t.Logf("  DecompressedPreview(first <=100 bytes): %q", dr.DecompressedBody[:previewLen])
	}
	t.Logf("  JSONParseOK:      %v", dr.JSONParseOK)
	t.Logf("  ExtraData(More):  %v", dr.ExtraData)
	if dr.ExtraDataDetail != "" {
		t.Logf("  ExtraDataDetail:  %s", dr.ExtraDataDetail)
	}
	t.Logf("  FullBodyHex(first <=200 bytes): %s", dr.FullBodyHex)
	t.Logf("======================")
}

func buildRawHTTPRequest(method, path string, headers map[string]string, body string) []byte {
	req := fmt.Sprintf("%s %s HTTP/1.1\r\n", method, path)
	for k, v := range headers {
		req += fmt.Sprintf("%s: %s\r\n", k, v)
	}
	req += "\r\n"
	if body != "" {
		req += body
	}
	return []byte(req)
}

func readHTTPResponse(t *testing.T, conn net.Conn) (resp *http.Response, bodyBytes []byte, err error) {
	br := bufio.NewReader(conn)
	resp, err = http.ReadResponse(br, nil)
	if err != nil {
		return nil, nil, err
	}
	bodyBytes, err = io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return resp, nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	return resp, bodyBytes, nil
}

func TestReproduce_SmallJSONWithBodyBuffer_RealHTTP(t *testing.T) {
	router := setupRouter(smallJSONHandler())
	ts := httptest.NewServer(router)
	defer ts.Close()

	rawConn, err := net.Dial("tcp", ts.Listener.Addr().String())
	require.NoError(t, err)
	defer rawConn.Close()

	rawReq := buildRawHTTPRequest("POST", "/tdx/v1/base/query/GetSecurityList/", map[string]string{
		"Accept-Encoding": "gzip, deflate",
		"Connection":      "keep-alive",
		"Host":            ts.Listener.Addr().String(),
		"Content-Type":    "application/json",
		"Content-Length":  "2",
	}, "{}")

	_, err = rawConn.Write(rawReq)
	require.NoError(t, err)

	resp, bodyBytes, err := readHTTPResponse(t, rawConn)
	require.NoError(t, err)

	r := inspectResponse(t, resp)
	printDebug(t, r)

	assert.Equal(t, 200, r.StatusCode)
	t.Logf("Raw body length: %d, Decompressed length: %d", r.RawBodyLen, len(r.DecompressedBody))

	if r.JSONParseOK {
		t.Log("JSON parse succeeded")
	} else {
		t.Errorf("JSON parse FAILED: %s", r.ExtraDataDetail)
	}

	if r.ExtraData {
		t.Errorf("EXTRA DATA DETECTED: %s", r.ExtraDataDetail)
	} else {
		t.Log("No extra data detected")
	}

	t.Logf("Full raw body (first 500 bytes): %s", string(bodyBytes[:min(len(bodyBytes), 500)]))
}

func TestReproduce_LargeJSONWithBodyBuffer_RealHTTP(t *testing.T) {
	router := setupRouter(largeJSONHandler())
	ts := httptest.NewServer(router)
	defer ts.Close()

	rawConn, err := net.Dial("tcp", ts.Listener.Addr().String())
	require.NoError(t, err)
	defer rawConn.Close()

	rawReq := buildRawHTTPRequest("POST", "/tdx/v1/base/query/GetSecurityList/", map[string]string{
		"Accept-Encoding": "gzip, deflate",
		"Connection":      "keep-alive",
		"Host":            ts.Listener.Addr().String(),
		"Content-Type":    "application/json",
		"Content-Length":  "2",
	}, "{}")

	_, err = rawConn.Write(rawReq)
	require.NoError(t, err)

	resp, bodyBytes, err := readHTTPResponse(t, rawConn)
	require.NoError(t, err)

	r := inspectResponse(t, resp)
	printDebug(t, r)

	assert.Equal(t, 200, r.StatusCode)
	t.Logf("Raw body length: %d, Decompressed length: %d", r.RawBodyLen, len(r.DecompressedBody))

	bodyToParse := bodyBytes
	if r.ContentEncoding == "gzip" && len(r.DecompressedBody) > 0 {
		bodyToParse = r.DecompressedBody
	}

	if len(bodyToParse) >= 1024 {
		t.Logf("Body is >= 1024 bytes, compression should have triggered")
	}

	var js json.RawMessage
	if err := json.Unmarshal(bodyToParse, &js); err != nil {
		t.Errorf("JSON parse FAILED: %v", err)
	} else {
		t.Log("JSON parse succeeded")
	}

	dec := json.NewDecoder(bytes.NewReader(bodyToParse))
	_ = dec.Decode(&json.RawMessage{})
	if dec.More() {
		extraStart := dec.InputOffset()
		remaining := bodyToParse[extraStart:]
		t.Errorf("EXTRA DATA DETECTED at offset %d, remaining: %d bytes, preview: %q",
			extraStart, len(remaining), remaining[:min(len(remaining), 200)])
	} else {
		t.Log("No extra data detected")
	}
}

func TestReproduce_KeepAliveMultipleWithBodyBuffer_RealHTTP(t *testing.T) {
	router := setupRouter(smallJSONHandler())
	ts := httptest.NewServer(router)
	defer ts.Close()

	rawConn, err := net.Dial("tcp", ts.Listener.Addr().String())
	require.NoError(t, err)
	defer rawConn.Close()

	numRequests := 3
	for i := range numRequests {
		t.Logf("--- Request %d/%d ---", i+1, numRequests)

		rawReq := buildRawHTTPRequest("POST", "/tdx/v1/base/query/GetSecurityList/", map[string]string{
			"Accept-Encoding": "gzip, deflate",
			"Connection":      "keep-alive",
			"Host":            ts.Listener.Addr().String(),
			"Content-Type":    "application/json",
			"Content-Length":  "2",
		}, "{}")

		_, err = rawConn.Write(rawReq)
		require.NoError(t, err)

		resp, bodyBytes, err := readHTTPResponse(t, rawConn)
		require.NoError(t, err)

		r := inspectResponse(t, resp)
		printDebug(t, r)

		assert.Equal(t, 200, r.StatusCode, "Request %d status", i+1)

		bodyToParse := bodyBytes
		if r.ContentEncoding == "gzip" && len(r.DecompressedBody) > 0 {
			bodyToParse = r.DecompressedBody
		}

		var js json.RawMessage
		if err := json.Unmarshal(bodyToParse, &js); err != nil {
			t.Errorf("Request %d: JSON parse FAILED: %v", i+1, err)
		} else {
			t.Logf("Request %d: JSON parse OK", i+1)
		}

		dec := json.NewDecoder(bytes.NewReader(bodyToParse))
		_ = dec.Decode(&json.RawMessage{})
		if dec.More() {
			extraStart := dec.InputOffset()
			remaining := bodyToParse[extraStart:]
			t.Errorf("Request %d: EXTRA DATA DETECTED at offset %d, remaining: %d bytes, preview: %q",
				i+1, extraStart, len(remaining), remaining[:min(len(remaining), 100)])
		} else {
			t.Logf("Request %d: No extra data", i+1)
		}

		t.Logf("Request %d: raw body len=%d, content-encoding=%s", i+1, r.RawBodyLen, r.ContentEncoding)
	}
}

func TestReproduce_SmallNonJSONWithBodyBuffer_RealHTTP(t *testing.T) {
	router := setupRouter(tinyHandler())
	ts := httptest.NewServer(router)
	defer ts.Close()

	rawConn, err := net.Dial("tcp", ts.Listener.Addr().String())
	require.NoError(t, err)
	defer rawConn.Close()

	rawReq := buildRawHTTPRequest("POST", "/tiny", map[string]string{
		"Accept-Encoding": "gzip, deflate",
		"Connection":      "keep-alive",
		"Host":            ts.Listener.Addr().String(),
		"Content-Type":    "text/plain",
		"Content-Length":  "0",
	}, "")

	_, err = rawConn.Write(rawReq)
	require.NoError(t, err)

	resp, bodyBytes, err := readHTTPResponse(t, rawConn)
	require.NoError(t, err)

	r := inspectResponse(t, resp)
	printDebug(t, r)

	assert.Equal(t, 200, r.StatusCode)
	t.Logf("Raw body: %q (len=%d)", bodyBytes, len(bodyBytes))
	t.Logf("Content-Encoding: %q", r.ContentEncoding)
	t.Logf("Full body hex: %s", r.FullBodyHex)
}

func getSecurityListHandlerSmall(n int) gin.HandlerFunc {
	return func(c *gin.Context) {
		stocks := make([]gin.H, 0, n)
		for i := 0; i < n; i++ {
			stocks = append(stocks, gin.H{
				"code":         fmt.Sprintf("%06d", i),
				"name":         fmt.Sprintf("股票%d", i),
				"decimalPoint": 2,
				"preClose":     10.5,
				"volumeUnit":   100,
			})
		}
		c.JSON(200, gin.H{
			"errorCode":    0,
			"errorMessage": "success",
			"data": gin.H{
				"count":      n,
				"noParseCount": 0,
				"data":       stocks,
			},
		})
	}
}

func setupRouterWithHandler(handler gin.HandlerFunc) *gin.Engine {
	r := gin.New()
	r.Use(reproBodyBuffer())
	r.Use(New(Config{MinLength: 1024}))
	r.POST("/tdx/v1/base/query/GetSecurityList/", handler)
	return r
}

func pythonStyleProcessResponse(t *testing.T, reqNum int, resp *http.Response) {
	t.Helper()
	t.Logf("============================================================")
	t.Logf("=== Processing Request #%d (Python requests + urllib3 style) ===", reqNum)
	t.Logf("============================================================")

	t.Logf("[Req %d] HTTP Status: %d", reqNum, resp.StatusCode)
	t.Logf("[Req %d] ALL Response Headers:", reqNum)
	for k, v := range resp.Header {
		t.Logf("  %s: %s", k, strings.Join(v, ", "))
	}
	t.Logf("[Req %d] Content-Length:    %q", reqNum, resp.Header.Get("Content-Length"))
	t.Logf("[Req %d] Transfer-Encoding: %v", reqNum, resp.TransferEncoding)

	contentEncoding := resp.Header.Get("Content-Encoding")

	rawBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err, "[Req %d] read body failed", reqNum)
	resp.Body.Close()

	rawDumpLen := 200
	if rawDumpLen > len(rawBody) {
		rawDumpLen = len(rawBody)
	}
	t.Logf("[Req %d] Raw body length:               %d bytes", reqNum, len(rawBody))
	t.Logf("[Req %d] Raw body (first %d bytes, hex):    %s", reqNum, rawDumpLen, hex.EncodeToString(rawBody[:rawDumpLen]))

	var bodyBytes []byte
	if contentEncoding == "gzip" {
		t.Logf("[Req %d] Content-Encoding=gzip → attempting gzip decompress (Python urllib3 path)...", reqNum)
		gz, gerr := gzip.NewReader(bytes.NewReader(rawBody))
		if gerr != nil {
			fmt.Println("DECOMPRESSION FAILED")
			t.Errorf("[Req %d] DECOMPRESSION FAILED (gzip.NewReader): %v", reqNum, gerr)
			t.Logf("[Req %d] FULL RAW BODY HEX DUMP:", reqNum)
			t.Logf("%s", hex.EncodeToString(rawBody))
			return
		}
		decompressed, derr := io.ReadAll(gz)
		gz.Close()
		if derr != nil {
			fmt.Println("DECOMPRESSION FAILED")
			t.Errorf("[Req %d] DECOMPRESSION FAILED (ReadAll): %v", reqNum, derr)
			t.Logf("[Req %d] FULL RAW BODY HEX DUMP:", reqNum)
			t.Logf("%s", hex.EncodeToString(rawBody))
			return
		}
		bodyBytes = decompressed

		decompDumpLen := 200
		if decompDumpLen > len(decompressed) {
			decompDumpLen = len(decompressed)
		}
		t.Logf("[Req %d] Decompressed length:             %d bytes", reqNum, len(decompressed))
		t.Logf("[Req %d] Decompressed (first %d bytes):     %s", reqNum, decompDumpLen, string(decompressed[:decompDumpLen]))
	} else {
		t.Logf("[Req %d] No Content-Encoding → json.loads(raw_body) (Python direct path)", reqNum)
		bodyBytes = rawBody
		bodyDumpLen := 200
		if bodyDumpLen > len(bodyBytes) {
			bodyDumpLen = len(bodyBytes)
		}
		t.Logf("[Req %d] Raw body as string (first %d bytes): %s", reqNum, bodyDumpLen, string(bodyBytes[:bodyDumpLen]))
	}

	t.Logf("[Req %d] Attempting json.loads(body) ...", reqNum)
	var parsed map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &parsed); err != nil {
		fmt.Println("JSON PARSE FAILED")
		t.Errorf("[Req %d] JSON PARSE FAILED: %v", reqNum, err)
		t.Logf("[Req %d] Body length: %d bytes", reqNum, len(bodyBytes))
		t.Logf("[Req %d] ===== FULL RAW BODY HEX DUMP (len=%d) =====", reqNum, len(rawBody))
		t.Logf("%s", hex.EncodeToString(rawBody))
		t.Logf("[Req %d] ===== FULL RAW BODY STRING (len=%d) =====", reqNum, len(rawBody))
		t.Logf("%s", string(rawBody))
		return
	}

	keys := make([]string, 0, len(parsed))
	for k := range parsed {
		keys = append(keys, k)
	}
	t.Logf("[Req %d] JSON parse OK! Top-level keys: %v", reqNum, keys)
}

func TestReproduce_PythonClientBehavior_RealHTTP(t *testing.T) {
	router := setupRouterWithHandler(getSecurityListHandlerSmall(8))
	ts := httptest.NewServer(router)
	defer ts.Close()

	transport := &http.Transport{
		DisableCompression:  true,
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     60 * 1000000000,
	}
	client := &http.Client{Transport: transport}

	doPythonStyleRequest := func(t *testing.T, reqNum int) {
		t.Helper()
		body := fmt.Sprintf(`{"market":0,"start":%d}`, (reqNum-1)*8)
		req, err := http.NewRequest("POST", ts.URL+"/tdx/v1/base/query/GetSecurityList/",
			strings.NewReader(body))
		require.NoError(t, err, "[Req %d] build request failed", reqNum)
		req.Header.Set("Accept-Encoding", "gzip, deflate")
		req.Header.Set("Connection", "keep-alive")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "python-requests/2.31.0")

		t.Logf("[Req %d] Sending POST request to %s", reqNum, ts.URL)
		t.Logf("[Req %d] Request headers: Accept-Encoding=gzip,deflate | Connection=keep-alive | User-Agent=python-requests/2.31.0", reqNum)

		resp, err := client.Do(req)
		require.NoError(t, err, "[Req %d] request failed", reqNum)
		defer resp.Body.Close()

		pythonStyleProcessResponse(t, reqNum, resp)
	}

	t.Log("========== FIRST REQUEST (keep-alive connection established) ==========")
	doPythonStyleRequest(t, 1)

	t.Log("")
	t.Log("========== SECOND REQUEST (reusing keep-alive connection) ==========")
	doPythonStyleRequest(t, 2)
}

type rawCapturingTransport struct {
	inner http.RoundTripper
}

func (rct *rawCapturingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := rct.inner.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	rawBytes, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(rawBytes))
	return resp, nil
}

func TestReproduce_DoubleWriteDetection_RealHTTP(t *testing.T) {
	router := setupRouterWithHandler(getSecurityListHandlerSmall(8))
	ts := httptest.NewServer(router)
	defer ts.Close()

	innerTransport := &http.Transport{
		DisableCompression:  true,
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     60 * 1000000000,
	}
	transport := &rawCapturingTransport{inner: innerTransport}
	client := &http.Client{Transport: transport}

	req, err := http.NewRequest("POST", ts.URL+"/tdx/v1/base/query/GetSecurityList/",
		strings.NewReader(`{"market":0,"start":0}`))
	require.NoError(t, err)
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "python-requests/2.31.0")

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	t.Logf("============================================================")
	t.Logf("=== Double Write Detection (rawCapturingTransport) ===")
	t.Logf("============================================================")
	t.Logf("Response status:        %d", resp.StatusCode)
	t.Logf("Content-Encoding:       %q", resp.Header.Get("Content-Encoding"))
	t.Logf("Content-Length:         %q", resp.Header.Get("Content-Length"))
	t.Logf("Transfer-Encoding:      %v", resp.TransferEncoding)
	t.Logf("Raw body length:        %d bytes", len(rawBody))

	dumpLen := 200
	if dumpLen > len(rawBody) {
		dumpLen = len(rawBody)
	}
	t.Logf("Raw body (first %d hex):    %s", dumpLen, hex.EncodeToString(rawBody[:dumpLen]))
	t.Logf("Raw body (first %d string): %q", dumpLen, string(rawBody[:dumpLen]))

	dec := json.NewDecoder(bytes.NewReader(rawBody))
	var first, second json.RawMessage

	err = dec.Decode(&first)
	if err != nil {
		t.Fatalf("Failed to decode first JSON object: %v\nFull hex dump: %s",
			err, hex.EncodeToString(rawBody))
	}
	firstDumpLen := 200
	if firstDumpLen > len(first) {
		firstDumpLen = len(first)
	}
	t.Logf("First JSON object:  %d bytes", len(first))
	t.Logf("First JSON preview: %s", string(first[:firstDumpLen]))
	t.Logf("Decoder offset after first JSON: %d / %d bytes", dec.InputOffset(), len(rawBody))

	if dec.More() {
		err = dec.Decode(&second)
		if err != nil {
			t.Logf("Second JSON decode FAILED (may be partial/garbage): %v", err)
			remaining := rawBody[dec.InputOffset():]
			t.Logf("Remaining unread bytes: %d", len(remaining))
			if len(remaining) > 0 {
				remDumpLen := 200
				if remDumpLen > len(remaining) {
					remDumpLen = len(remaining)
				}
				t.Logf("Remaining hex:    %s", hex.EncodeToString(remaining[:remDumpLen]))
				t.Logf("Remaining string: %q", string(remaining[:remDumpLen]))
			}
		} else {
			secondDumpLen := 200
			if secondDumpLen > len(second) {
				secondDumpLen = len(second)
			}
			t.Logf("SECOND JSON OBJECT DETECTED! Length: %d bytes", len(second))
			t.Logf("Second JSON preview: %s", string(second[:secondDumpLen]))
		}
		t.Errorf("DOUBLE WRITE DETECTED: Response body contains MORE THAN ONE JSON object!")
	} else {
		t.Logf("No double write detected — response contains exactly ONE JSON object (good)")
	}
}
