package autoindex

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shengyanli1982/orbit-middlewares/middleware/compression"
	"github.com/shengyanli1982/orbit-middlewares/middleware/security"
	"github.com/shengyanli1982/orbit-middlewares/middleware/timeout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 本文件验证 README「AutoIndex — Mounting & conflict avoidance」宣称的规避方案真实有效。
// 前提：gin 全局 Use 中间件会自动前置包裹 NoRoute 链（combineHandlers），
// 因此 autoindex 以 NoRoute 挂载时，与 compression/timeout/security 的交互是默认发生的。

// ---------- a) compression + autoindex ----------

// TestConflict_Compression_ListingGzipped 验证目录列表 HTML 被 compression 正常 gzip：
// 列表页是可安全压缩的部分，规避方案只需排除下载路径。
func TestConflict_Compression_ListingGzipped(t *testing.T) {
	root := newTestTree(t, map[string]string{
		"hello.txt":   "hello\n",
		"sub/note.md": "# note\n",
	})
	r := gin.New()
	r.Use(compression.New(compression.Config{
		MinLength:     64,
		ExcludedPaths: []string{"/payload.bin"},
	}))
	r.NoRoute(New(Config{Root: root}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "gzip", rec.Header().Get("Content-Encoding"), "目录列表 HTML 应被 gzip")

	zr, err := gzip.NewReader(rec.Body)
	require.NoError(t, err, "body 应是合法 gzip 流")
	html, err := io.ReadAll(zr)
	require.NoError(t, err)
	assert.Contains(t, string(html), "Index of /")
	assert.Contains(t, string(html), ">hello.txt<", "解压后应包含条目名")
}

// TestConflict_Compression_ExcludedRangeIntact 验证把下载路径加入 ExcludedPaths 后
// Range 下载完好：206 + Content-Range 正确 + 分片字节与原始文件一致（规避方案有效）。
func TestConflict_Compression_ExcludedRangeIntact(t *testing.T) {
	content := bytes.Repeat([]byte("orbit-autoindex-payload."), 128) // 3072 字节 > MinLength
	root := newTestTree(t, nil)
	require.NoError(t, os.WriteFile(filepath.Join(root, "payload.bin"), content, 0o644))

	r := gin.New()
	r.Use(compression.New(compression.Config{
		MinLength:     64,
		ExcludedPaths: []string{"/payload.bin"}, // README 规避动作：下载路径排除出压缩
	}))
	r.NoRoute(New(Config{Root: root}))

	req := httptest.NewRequest(http.MethodGet, "/payload.bin", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Range", "bytes=0-1023")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusPartialContent, rec.Code)
	assert.Empty(t, rec.Header().Get("Content-Encoding"), "gzip 排除后不应有 Content-Encoding")
	assert.Equal(t, "bytes 0-1023/3072", rec.Header().Get("Content-Range"))
	assert.Equal(t, content[:1024], rec.Body.Bytes(), "分片字节必须与原始文件一致")
}

// TestConflict_Compression_UnexcludedRange206Exempted 正向钉住现状：
// 不排除下载路径时，206 分片响应也不再被 compression 重写——gzipWriter 的
// 状态门已将 206/304 与 4xx/5xx 一并豁免，分片字节、Content-Range 与
// Content-Length 原样到达客户端。排除下载路径由此降级为可选优化
// （省去压缩尝试开销），不再是正确性要求。
func TestConflict_Compression_UnexcludedRange206Exempted(t *testing.T) {
	content := bytes.Repeat([]byte("orbit-autoindex-payload."), 128) // 3072 字节
	root := newTestTree(t, nil)
	require.NoError(t, os.WriteFile(filepath.Join(root, "payload.bin"), content, 0o644))

	r := gin.New()
	r.Use(compression.New(compression.Config{MinLength: 64})) // 刻意不排除下载路径
	r.NoRoute(New(Config{Root: root}))

	req := httptest.NewRequest(http.MethodGet, "/payload.bin", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Range", "bytes=0-1023")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusPartialContent, rec.Code)
	assert.Empty(t, rec.Header().Get("Content-Encoding"),
		"206 已被压缩决策状态门自动豁免，不应携带 Content-Encoding")
	assert.Equal(t, "bytes 0-1023/3072", rec.Header().Get("Content-Range"),
		"Content-Range 必须原样保留")
	assert.Equal(t, "1024", rec.Header().Get("Content-Length"),
		"分片定长须保留（未被压缩路径删除），与实际分片字节数一致")
	assert.Equal(t, content[:1024], rec.Body.Bytes(),
		"分片字节必须与原始文件一致，不被 gzip 重写——Range 下载语义完好")
}

// ---------- b) timeout + autoindex ----------

// TestConflict_Timeout_SkipperBypassesBuffering 验证 timeout 的 Skipper 排除下载路径有效：
// 命中 Skipper 的请求不进 timeout 的缓冲 goroutine，由 autoindex 直接流式服务，
// 文件完整送达（README 规避动作：大文件下载必须排除出 timeout）。
//
// 区分性断言（杜绝假通过）：timeout 的缓冲回放会在其 goroutine 里重跑整条链，故注册在
// timeout 之前的中间件在直连路径每请求执行 1 次、缓冲路径执行 2 次（见 timeout.go New
// 注释）。用这一副作用做探针同时钉住两个方向：Skipper 命中 → 计数==1（直连）；未命中
// → 计数==2（缓冲回放）。若把 Skipper 变异为永不命中，下载路径计数会变 2 使断言失败——
// 测试得以真正检测 Skipper 失效，而非仅断言对小文件不可区分的 200+body。
func TestConflict_Timeout_SkipperBypassesBuffering(t *testing.T) {
	content := bytes.Repeat([]byte("timeout-skipper-payload."), 256) // 6144 字节
	root := newTestTree(t, nil, "download")
	require.NoError(t, os.WriteFile(filepath.Join(root, "download", "payload.bin"), content, 0o644))

	// 计数器探针须注册在 timeout 之前（才会被回放重跑）；回放发生在独立 goroutine，
	// 计数必须原子；timeout 回放依赖 Engine，故传 Engine: r。
	skipper := func(c *gin.Context) bool {
		return strings.HasPrefix(c.Request.URL.Path, "/download/")
	}
	newRouter := func(counter *atomic.Int64) *gin.Engine {
		r := gin.New()
		r.Use(func(c *gin.Context) { counter.Add(1); c.Next() })
		r.Use(timeout.New(timeout.Config{Engine: r, Timeout: 2 * time.Second, Skipper: skipper}))
		r.NoRoute(New(Config{Root: root}))
		return r
	}

	// 方向一：被 Skipper 命中的下载路径 → 直接完整送达，不进缓冲 goroutine（计数==1）。
	t.Run("skipper hit bypasses buffering", func(t *testing.T) {
		var counter atomic.Int64
		rec := do(newRouter(&counter), http.MethodGet, "/download/payload.bin", nil)

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, content, rec.Body.Bytes(), "Skipper 命中时文件应直接完整送达")
		assert.Equal(t, int64(1), counter.Load(),
			"直连时 timeout 前置中间件只执行 1 次（变 2 即说明走了缓冲回放，Skipper 失效）")
	})

	// 方向二：不被 Skipper 命中的路径（目录列表页）→ timeout 缓冲重放整链（计数==2），
	// 钉住缓冲机制本身，使方向一的计数含义自解释。
	t.Run("miss goes through buffered replay", func(t *testing.T) {
		var counter atomic.Int64
		rec := do(newRouter(&counter), http.MethodGet, "/", nil)

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "Index of /")
		assert.Equal(t, int64(2), counter.Load(),
			"缓冲回放会重跑整链，timeout 前置中间件执行 2 次")
	})
}

// TestConflict_Timeout_BufferedSmallResponseOK 验证不排除时小文件/小列表页也能通过
// timeout 的全量缓冲模型（整链在 goroutine 里缓冲进内存 bufferWriter 再刷写）。
// 缓冲模型对小响应无害；风险仅在大文件下载（占内存 + 超过 Timeout 被 504 切断、
// Range 流式失效），故 README 要求用 Skipper 排除下载路径或纯静态服务不挂 timeout。
func TestConflict_Timeout_BufferedSmallResponseOK(t *testing.T) {
	root := newTestTree(t, map[string]string{"hello.txt": "small file\n"})
	r := gin.New()
	r.Use(timeout.New(timeout.Config{Engine: r, Timeout: 2 * time.Second})) // 无 Skipper
	r.NoRoute(New(Config{Root: root}))

	rec := do(r, http.MethodGet, "/hello.txt", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "small file\n", rec.Body.String())

	rec = do(r, http.MethodGet, "/", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Index of /")
}

// ---------- c) security + autoindex ----------

// TestConflict_Security_CSPHeaderAndIntactListing 验证 security 与 autoindex 叠加后
// 服务端行为完好：CSP 头写入，且目录列表响应（状态码/body）完整不受影响。
// CSP `default-src 'self'` 阻断的是浏览器对列表页内联 <style> 的渲染——仅外观降级
// （页面失去等宽样式），链接与下载功能不受损；接受无样式或放宽 style-src 即可。
func TestConflict_Security_CSPHeaderAndIntactListing(t *testing.T) {
	root := newTestTree(t, map[string]string{
		"hello.txt":   "hello\n",
		"sub/note.md": "# note\n",
	})
	r := gin.New()
	r.Use(security.New(security.DefaultConfig()))
	r.NoRoute(New(Config{Root: root}))

	rec := do(r, http.MethodGet, "/", nil)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "default-src 'self'", rec.Header().Get("Content-Security-Policy"))
	body := rec.Body.String()
	assert.Contains(t, body, "Index of /")
	assert.Contains(t, body, ">hello.txt<", "列表 body 应完整")
	assert.Contains(t, body, "<style>", "内联样式仍照常输出（是否渲染由浏览器按 CSP 决定）")

	// 文件下载同样不受 security 影响
	rec = do(r, http.MethodGet, "/hello.txt", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "hello\n", rec.Body.String())
}

// ---------- d) Content-Length + compression（列表页声明定长 CL 后的压缩交互） ----------

// listingTreeOverChunkThreshold 构造条目数足以让列表页明确超过 2048 B 的目录树。
// 2048 B 是 net/http 自动回填 Content-Length 的上限（http.serverChunkThreshold）：
// body 不超过它时服务端才会在 WriteHeader 时补上 CL，超过则改用 Transfer-Encoding:
// chunked 分帧。100 条目实测页面约 12 KB，远超该阈值 ⇒ 这正是「必须由本包显式声明
// CL，否则恒走 chunked」的尺寸区间。
func listingTreeOverChunkThreshold(t *testing.T) string {
	t.Helper()
	files := make(map[string]string, 100)
	for i := 0; i < 100; i++ {
		files[fmt.Sprintf("entry-%03d.txt", i)] = "x"
	}
	return newTestTree(t, files)
}

// TestConflict_Compression_ListingContentLengthDeletedWhenGzipped 钉住「压缩路径」。
// autoindex 声明 Content-Length 后，compression 的快速决策路径据此判定 cl >= MinLength，
// 并在压缩前先 Del("Content-Length")（见 compression.go 的 (*gzipWriter).Write「根据
// Content-Length 快速决策」分支）。因此三件事必须同时成立：
//   - Content-Encoding: gzip 存在；
//   - Content-Length 为空（已被删除）⇒ 不可能以未压缩长度上线，无头-body 撕裂；
//   - body 可解压，且解压结果与「不经 compression 时的列表页字节」逐字节相同。
//
// 判别力（杜绝假通过）：第三条是核心。若本包声明的 CL 污染了压缩输出（例如让
// compression 误判跳过压缩，或 CL 残留在压缩响应上使客户端按未压缩长度截断读取），
// 解压结果就不会等于原始页面字节 ⇒ 断言 FAIL。参照组还额外断言页面 > 2048 B，确保
// 本测试覆盖的正是「不设 CL 就会被 chunked」的尺寸，而非小页面的退化情形。
func TestConflict_Compression_ListingContentLengthDeletedWhenGzipped(t *testing.T) {
	root := listingTreeOverChunkThreshold(t)

	// 参照组：不经 compression，取列表页原始字节。两次请求共用同一 root ⇒ mtime 等
	// 渲染输入完全一致，页面字节确定，可逐字节比对。
	plain := gin.New()
	plain.NoRoute(New(Config{Root: root}))
	rawRec := do(plain, http.MethodGet, "/", nil)
	require.Equal(t, http.StatusOK, rawRec.Code)
	rawPage := rawRec.Body.Bytes()
	require.Greater(t, len(rawPage), 2048,
		"列表页必须超过 net/http 自动回填 CL 的 2048 B 阈值，否则本测试无判别意义")
	assert.Equal(t, strconv.Itoa(len(rawPage)), rawRec.Header().Get("Content-Length"),
		"参照组自身就应声明准确 CL（若移除实现里的 CL 设置，此断言即 FAIL）")

	// 实验组：compression 在链上且客户端声明支持 gzip。
	r := gin.New()
	r.Use(compression.New(compression.Config{MinLength: 64}))
	r.NoRoute(New(Config{Root: root}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "gzip", rec.Header().Get("Content-Encoding"), "12 KB 列表页应被 gzip")
	assert.Empty(t, rec.Header().Get("Content-Length"),
		"压缩前 compression 必须删除 CL，否则会以未压缩长度上线（头-body 撕裂）")

	zr, err := gzip.NewReader(rec.Body)
	require.NoError(t, err, "body 应是合法 gzip 流")
	got, err := io.ReadAll(zr)
	require.NoError(t, err)
	assert.Equal(t, rawPage, got,
		"解压后必须与未压缩页面逐字节相同：证明本包声明的 CL 没有污染压缩输出")
	assert.Less(t, rec.Body.Len(), len(rawPage), "gzip 后线路字节应更小（确认真的压缩了）")
}

// TestConflict_Compression_ListingContentLengthRetainedWhenSkipped 钉住「跳过路径」的
// 两个入口；两者都必须让 autoindex 声明的 CL 原样且准确地到达客户端：
//
//	(i)  ExcludedPaths 命中 ⇒ shouldCompress 返回 false，gzipWriter 根本不包裹 writer，
//	     CL 直通；
//	(ii) MinLength 大于页面长度 ⇒ gzipWriter 包裹了 writer，但快速决策路径读到
//	     cl < minLen 后走 skipAndWrite：body 原样写出、CL 保留（skipCompress 只置标志位，
//	     removeGzipHeaders 仅在 status>=400 分支调用且只删 Content-Encoding/Vary）。
//
// (ii) 是真正的风险点——它确实经过了 compression 的 writer 包裹，若实现声明的 CL 与
// 实际写出字节不一致、或 skip 路径误删 CL，本断言就会失败。
//
// 判别力（杜绝假通过）：断言 CL 非空且数值精确等于 rec.Body.Len()。
// httptest.ResponseRecorder 不建模 net/http 服务端的 CL 自动回填（实测：handler 不设
// CL 时 rec.Header().Get("Content-Length") 为空字符串），因此若移除实现里的
// c.Header("Content-Length", strconv.Itoa(len(page))) 一行，本断言必然 FAIL。它检测的
// 是实现本身，而非对小响应恒真的空洞断言。
func TestConflict_Compression_ListingContentLengthRetainedWhenSkipped(t *testing.T) {
	root := listingTreeOverChunkThreshold(t)

	cases := []struct {
		name string
		cfg  compression.Config
	}{
		{
			// Contains 用 strings.HasPrefix 匹配，"/" 命中所有路径
			name: "excluded path bypasses gzipWriter entirely",
			cfg:  compression.Config{MinLength: 64, ExcludedPaths: []string{"/"}},
		},
		{
			name: "minLen above page size takes skipAndWrite",
			cfg:  compression.Config{MinLength: 1 << 20}, // 1 MiB > 任何列表页
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			r.Use(compression.New(tc.cfg))
			r.NoRoute(New(Config{Root: root}))

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Accept-Encoding", "gzip") // 客户端支持 gzip ⇒ 跳过纯由服务端决策
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			require.Equal(t, http.StatusOK, rec.Code)
			assert.Empty(t, rec.Header().Get("Content-Encoding"), "跳过路径不应压缩")
			require.Greater(t, rec.Body.Len(), 2048, "页面须超过 chunked 阈值，断言才有意义")

			clStr := rec.Header().Get("Content-Length")
			require.NotEmpty(t, clStr,
				"跳过压缩时 autoindex 声明的 CL 必须保留（移除实现的 CL 设置则此处为空即 FAIL）")
			cl, err := strconv.Atoi(clStr)
			require.NoError(t, err)
			assert.Equal(t, rec.Body.Len(), cl,
				"CL 必须精确等于实际写出的 body 字节数，不等即头-body 撕裂")
			assert.Contains(t, rec.Body.String(), "Index of /", "body 确为未压缩的列表页")
		})
	}
}

// ---------- e) Content-Length 线路级分帧（真实 server，非 ResponseRecorder） ----------

// rawWireResponse 经真实 TCP 连接发出手写 HTTP/1.1 请求，返回线路上的原始响应头文本
// 与 body 字节数。
//
// 为什么不能用 httptest.ResponseRecorder 或 http.Client：ResponseRecorder 根本不建模
// Transfer-Encoding（它对 chunked 与定长响应给出完全相同的结果），http.Client 则会透明
// 解压并抹掉分帧相关头。两者都结构性地观察不到分帧方式 ⇒ 必须读原始线路字节。
// 请求带 Connection: close，服务端写完即关闭连接，故读到 EOF 即 body 全长（无需猜长度）。
func rawWireResponse(t *testing.T, addr, method, target string) (head string, body []byte) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err, "应能连上真实 server")
	defer conn.Close()
	// 兜底 deadline：即使服务端行为异常也不会让 CI 悬挂
	require.NoError(t, conn.SetDeadline(time.Now().Add(10*time.Second)))

	wire := method + " " + target + " HTTP/1.1\r\nHost: " + addr + "\r\nConnection: close\r\n\r\n"
	_, err = conn.Write([]byte(wire))
	require.NoError(t, err)

	br := bufio.NewReader(conn)
	var sb strings.Builder
	for {
		line, rerr := br.ReadString('\n')
		sb.WriteString(line)
		if rerr != nil || line == "\r\n" { // 头块以空行结束；出错则已读部分即为全部
			break
		}
	}
	rest, _ := io.ReadAll(br) // Connection: close ⇒ EOF 即 body 结束
	return sb.String(), rest
}

// wireContentLength 从原始头文本中取 Content-Length 数值；缺失返回 -1。
func wireContentLength(head string) int {
	for _, ln := range strings.Split(head, "\r\n") {
		if v, ok := strings.CutPrefix(ln, "Content-Length:"); ok {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return -1
			}
			return n
		}
	}
	return -1
}

// TestConflict_ContentLength_WireFramingNotChunked 在真实 net/http server 上验证线路行为。
//
// 本测试采用「受控对照」设计以杜绝假通过：对照组服务与 autoindex **完全相同的页面字节**，
// 唯一差别是刻意不声明 Content-Length。于是两组之间任何差异都只能归因于 CL 声明本身。
// 对照组被断言**必须**出现 Transfer-Encoding: chunked —— 这既是阳性对照（证明本测试的
// 检测手段真的能看见 chunked，autoindex 组的「无 chunked」断言不是空洞的），也复现了
// Q8 改造前的实际行为。
//
// 目录规模：100 条目 ⇒ 页面约 12 KB，明确超过 net/http 自动回填 CL 的 2048 B 阈值
// （http.serverChunkThreshold）。小于该阈值时 net/http 会自行补上 CL，chunked 不会
// 出现，测试将失去判别力，故规模不可调小。
func TestConflict_ContentLength_WireFramingNotChunked(t *testing.T) {
	root := listingTreeOverChunkThreshold(t)

	// 先取页面字节，供对照组原样回放（保证两组 body 内容逐字节相同）
	plain := gin.New()
	plain.NoRoute(New(Config{Root: root}))
	rawRec := do(plain, http.MethodGet, "/", nil)
	require.Equal(t, http.StatusOK, rawRec.Code)
	page := append([]byte(nil), rawRec.Body.Bytes()...)
	require.Greater(t, len(page), 2048, "页面必须超过 2048 B 的 chunked 阈值，否则测试无判别力")

	// 实验组：autoindex 真实链路（已声明 CL）
	autoSrv := gin.New()
	autoSrv.NoRoute(New(Config{Root: root}))
	srvA := httptest.NewServer(autoSrv)
	defer srvA.Close()

	// 对照组：同等字节、同等 Content-Type，但**不声明** CL
	ctrlSrv := gin.New()
	ctrlSrv.NoRoute(func(c *gin.Context) {
		c.Status(http.StatusOK)
		c.Header("Content-Type", "text/html; charset=utf-8")
		_, _ = c.Writer.Write(page) // 刻意不设 Content-Length
	})
	srvB := httptest.NewServer(ctrlSrv)
	defer srvB.Close()

	addrA := srvA.Listener.Addr().String()
	addrB := srvB.Listener.Addr().String()

	t.Run("GET declares exact Content-Length and is not chunked", func(t *testing.T) {
		head, body := rawWireResponse(t, addrA, http.MethodGet, "/")
		assert.Contains(t, head, "HTTP/1.1 200 OK")
		assert.Equal(t, len(page), wireContentLength(head),
			"线路上的 CL 必须精确等于页面字节数（移除实现的 CL 设置则此处为 -1 即 FAIL）")
		assert.NotContains(t, head, "Transfer-Encoding",
			"声明定长 CL 后不应再走 chunked 分帧")
		assert.Equal(t, len(page), len(body), "线路 body 字节数应等于 CL")
		assert.Equal(t, page, body, "线路 body 应与页面逐字节一致（CL 不得改变 body）")
	})

	t.Run("HEAD reports the same Content-Length with empty body", func(t *testing.T) {
		getHead, _ := rawWireResponse(t, addrA, http.MethodGet, "/")
		head, body := rawWireResponse(t, addrA, http.MethodHead, "/")
		assert.Contains(t, head, "HTTP/1.1 200 OK")
		assert.Equal(t, wireContentLength(getHead), wireContentLength(head),
			"HEAD 与 GET 的 CL 必须完全相同——这是 Q8 的实质改进：改造前 HEAD 打目录拿不到准确 CL")
		assert.Equal(t, len(page), wireContentLength(head), "HEAD 的 CL 应等于页面字节数")
		assert.NotContains(t, head, "Transfer-Encoding", "HEAD 同样不应 chunked")
		assert.Empty(t, body, "net/http 在 chunkWriter 层吃掉 HEAD 的 body，线路上应为 0 字节")
	})

	t.Run("positive control: same bytes without CL do go chunked", func(t *testing.T) {
		head, body := rawWireResponse(t, addrB, http.MethodGet, "/")
		assert.Contains(t, head, "HTTP/1.1 200 OK")
		assert.Equal(t, -1, wireContentLength(head), "对照组未声明 CL")
		assert.Contains(t, head, "Transfer-Encoding: chunked",
			"阳性对照：同等字节但不设 CL 时必须 chunked，证明上面的检测手段确实看得见分帧")
		assert.Greater(t, len(body), len(page),
			"chunked 分帧会在线路上多出块长/终止块开销，故线路字节数严格大于页面字节数")
	})

	t.Run("positive control: HEAD without CL yields no usable length", func(t *testing.T) {
		head, body := rawWireResponse(t, addrB, http.MethodHead, "/")
		assert.Equal(t, -1, wireContentLength(head),
			"对照组 HEAD 拿不到 CL：正是 Q8 改造前 autoindex 的行为")
		assert.Empty(t, body)
	})
}
