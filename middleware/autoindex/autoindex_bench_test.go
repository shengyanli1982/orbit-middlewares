package autoindex

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// ---------------------------------------------------------------------------
// 基准专用 ResponseWriter
//
// 为什么不用 httptest.NewRecorder()：
//  1. 复用一个 recorder 且从不 Reset 会让 Body(bytes.Buffer) 逐轮累积——目录列表
//     单轮响应体实测 12485 B，跑 5000 轮即驻留约 59 MiB。这既污染 B/op（几何扩容
//     的突发性使同一 bench 三次采样的 B/op 从 8778 抖到 13442，极差 53%），又因巨型
//     存活对象抬高 GC heap goal、抑制 GC 触发，使泄漏版本反而「虚快」。
//  2. recorder.HeaderMap 跨轮残留 Content-Type 后，net/http.serveContent 读到
//     w.Header()["Content-Type"] 存在即走 haveType=true 分支，跳过
//     mime.TypeByExtension 与（扩展名无映射时的）512B 嗅探读 + DetectContentType +
//     Seek 回卷——第 2..N 轮测的是与生产不同的廉价路径。
//  3. 每轮新建 recorder 虽能避免上述两点，但实测其自身开销为 +15 allocs/op、
//     +43.7 KB/op（约占目录列表 B/op 的 24%），把测试脚手架成本计入了被测对象。
//
// 故采用下面的 benchWriter：每轮 reset 到干净状态，Header 返回被清空的 map（复现
// 生产语义），Write 只累加字节数不缓冲（零 body 驻留、零扩容突发），WriteHeader
// 记录状态码。相对复用 recorder 仅 +1 allocs/op，脚手架成本可忽略。
//
// 刻意不模拟的：真实 net/http 响应头的序列化与 socket 写入。这与 httptest.Recorder
// 的取舍一致——本基准度量中间件自身的 CPU 与分配成本，不含传输层。
// ---------------------------------------------------------------------------

// benchWriter 是基准专用的干净状态 ResponseWriter。
type benchWriter struct {
	header http.Header
	status int
	bytes  int
}

// newBenchWriter 创建一个预分配 header map 的 benchWriter。
func newBenchWriter() *benchWriter {
	return &benchWriter{header: make(http.Header, 8)}
}

// reset 把 writer 恢复到「本轮尚未写过任何内容」的干净状态。
func (w *benchWriter) reset() {
	clear(w.header)
	w.status = 0
	w.bytes = 0
}

// Header 返回可变的响应头 map（每轮由 reset 清空，不跨轮残留）。
func (w *benchWriter) Header() http.Header { return w.header }

// Write 只累计字节数，不缓冲响应体内容。
func (w *benchWriter) Write(p []byte) (int, error) {
	w.bytes += len(p)
	return len(p), nil
}

// WriteHeader 记录状态码；与 http.ResponseWriter 一致，首次写入后不再改变。
func (w *benchWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}

// SetWriteDeadline 让 benchWriter 支持写截止调整，对齐生产中 *http.response 的能力。
//
// 必须实现：autoindex 文件下载路径会调用
// http.NewResponseController(c.Writer).SetWriteDeadline(time.Time{}) 清除写截止。
// 若 writer 不支持该方法，stdlib 会走 net/http.errNotSupported()，其实现为
// fmt.Errorf("%w", ErrNotSupported)——每次调用都新建一个包装 error（实测占文件
// 下载路径 allocs 的 10.91%、约 2.3 次分配/请求），而该 error 被 autoindex 以
// `_ =` 直接丢弃。生产中裸 net/http 的 *http.response 支持该方法、不产生此开销，
// 故这是纯粹的基准脚手架假象，须消除以免把 harness 成本计入被测对象。
//
// 注：仅当 autoindex 被不支持调截止的 writer 包裹时（如 timeout 中间件的缓冲
// writer）生产才会真正走到该错误路径，属实现注释中声明的 best-effort 预期行为。
func (w *benchWriter) SetWriteDeadline(time.Time) error { return nil }

// benchLoop 以并行方式跑 target 请求，每轮使用干净的 benchWriter。
//
// reqMutate 用于在构造请求时注入 Range / If-Modified-Since 等头（每 goroutine 只
// 构造一次请求并复用——被测 handler 只读不改请求，复用可把 httptest.NewRequest 的
// 分配排除在测量之外）。返回的 bodyBytes 为每轮实际写入 writer 的字节数，作为
// 自定义指标上报，用于反证 Write 未被优化掉、writer 收到了完整响应体。
func benchLoop(b *testing.B, router *gin.Engine, target string, reqMutate func(*http.Request)) float64 {
	b.Helper()
	b.ResetTimer()

	var total int64
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		if reqMutate != nil {
			reqMutate(req)
		}
		w := newBenchWriter()
		var local int64
		for pb.Next() {
			w.reset()
			router.ServeHTTP(w, req)
			local += int64(w.bytes)
		}
		atomic.AddInt64(&total, local)
	})
	b.ReportMetric(float64(atomic.LoadInt64(&total))/float64(b.N), "body-B/op")
	return float64(atomic.LoadInt64(&total)) / float64(b.N)
}

// seedDir 在 dir 下构造 nFiles 个文件与 nDirs 个子目录，条目大小与修改时间按序号
// 确定性变化，使 N/M/S 三个排序列都有可区分的键（而非全等导致排序退化为空操作）。
func seedDir(b *testing.B, dir string, nFiles, nDirs int) {
	b.Helper()
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for i := 0; i < nDirs; i++ {
		if err := os.MkdirAll(filepath.Join(dir, fmt.Sprintf("dir-%03d", i)), 0o755); err != nil {
			b.Fatal(err)
		}
	}
	for i := 0; i < nFiles; i++ {
		name := filepath.Join(dir, fmt.Sprintf("entry-%03d.txt", i))
		// 大小在 64B..64KB 间确定性变化：既覆盖 humanSize 的「纯字节」分支
		//（<1024），也覆盖 K 级分支，避免全等大小让 ?C=S 排序无从比较。
		size := 64 + (i*997)%(64*1024)
		if err := os.WriteFile(name, make([]byte, size), 0o644); err != nil {
			b.Fatal(err)
		}
		// 修改时间按序号递增 1 分钟，使 ?C=M 排序与时间格式化输出有意义。
		mt := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(name, mt, mt); err != nil {
			b.Fatal(err)
		}
	}
}

// newRouter 按仓库既有风格构造挂载了 autoindex 的 NoRoute 路由。
func newRouter(cfg Config) *gin.Engine {
	r := gin.New()
	r.NoRoute(New(cfg))
	return r
}

// BenchmarkAutoIndexListing 基准目录列表主路径：os.ReadDir → IndexIgnore 过滤 →
// 每条目一次 DirEntry.Info()（stat）+ ModTime 格式化 + humanSize → dirs/files
// 分组 → sortRows ×2 → html/template 渲染。
//
// 覆盖子路径：默认排序、?C=S 降序、?C=M 升序、IndexIgnore 过滤，以及 1000 条目
// 规模点（用于观察排序 + 渲染对条目数的规模敏感度）。
func BenchmarkAutoIndexListing(b *testing.B) {
	// 100 条目（90 文件 + 10 子目录）：既走 dirs 也走 files 两次 sortRows。
	b.Run("default_name_asc", func(b *testing.B) {
		root := b.TempDir()
		seedDir(b, root, 90, 10)
		benchLoop(b, newRouter(Config{Root: root}), "/", nil)
	})

	// 按大小降序：命中 comparePrimary 的 cmp.Compare(sortSize) 分支。
	b.Run("sort_size_desc", func(b *testing.B) {
		root := b.TempDir()
		seedDir(b, root, 90, 10)
		benchLoop(b, newRouter(Config{Root: root}), "/?C=S;O=D", nil)
	})

	// 按修改时间升序：命中 time.Time.Compare 分支。
	b.Run("sort_mtime_asc", func(b *testing.B) {
		root := b.TempDir()
		seedDir(b, root, 90, 10)
		benchLoop(b, newRouter(Config{Root: root}), "/?C=M", nil)
	})

	// IndexIgnore：100 条目中 30 条命中（*.tmp 与 .hidden），度量 path.Match
	// 逐条目逐模式的过滤成本。注意 IndexIgnore 仅隐藏列表，不阻断直接访问。
	b.Run("index_ignore", func(b *testing.B) {
		root := b.TempDir()
		seedDir(b, root, 70, 10)
		for i := 0; i < 20; i++ {
			name := filepath.Join(root, fmt.Sprintf("scratch-%03d.tmp", i))
			if err := os.WriteFile(name, make([]byte, 512), 0o644); err != nil {
				b.Fatal(err)
			}
		}
		for i := 0; i < 10; i++ {
			name := filepath.Join(root, fmt.Sprintf(".hidden-%03d", i))
			if err := os.WriteFile(name, make([]byte, 512), 0o644); err != nil {
				b.Fatal(err)
			}
		}
		benchLoop(b, newRouter(Config{Root: root, IndexIgnore: []string{"*.tmp", ".hidden*"}}), "/", nil)
	})

	// 规模点：1000 条目（900 文件 + 100 子目录），条目数 10× 于常规用例，
	// 用于观察 sort.Slice（reflect swapper + entryRow 按值拷贝）与模板渲染的
	// 规模敏感度。文件体保持 0 字节以隔离「条目数」这一个变量。
	b.Run("large_1000", func(b *testing.B) {
		root := b.TempDir()
		for i := 0; i < 100; i++ {
			if err := os.MkdirAll(filepath.Join(root, fmt.Sprintf("dir-%03d", i)), 0o755); err != nil {
				b.Fatal(err)
			}
		}
		for i := 0; i < 900; i++ {
			name := filepath.Join(root, fmt.Sprintf("entry-%04d.txt", i))
			if err := os.WriteFile(name, nil, 0o644); err != nil {
				b.Fatal(err)
			}
			mt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Add(time.Duration(i) * time.Minute)
			if err := os.Chtimes(name, mt, mt); err != nil {
				b.Fatal(err)
			}
		}
		benchLoop(b, newRouter(Config{Root: root}), "/", nil)
	})
}

// BenchmarkAutoIndexFileServe 基准静态文件下载主路径：filepath.Join + 前缀校验 →
// path.Clean → os.Stat → SetWriteDeadline（best-effort）→ os.Open →
// http.ServeContent（含 Content-Type 推断、Content-Length、io.CopyN）。
//
// 覆盖子路径：200 全量、Range/206 部分内容、If-Modified-Since/304 未修改。
func BenchmarkAutoIndexFileServe(b *testing.B) {
	// 2432 字节载荷（约 2.4KB），扩展名 .bin 在本机有 mime 映射
	//（application/octet-stream），故不触发内容嗅探——与生产一致。
	newPayloadTree := func(b *testing.B) (string, time.Time) {
		root := b.TempDir()
		payload := bytes.Repeat([]byte("orbit-middlewares-autoindex-benchmark\n"), 64) // 38×64 = 2432 B
		if err := os.WriteFile(filepath.Join(root, "payload.bin"), payload, 0o644); err != nil {
			b.Fatal(err)
		}
		mt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		if err := os.Chtimes(filepath.Join(root, "payload.bin"), mt, mt); err != nil {
			b.Fatal(err)
		}
		return root, mt
	}

	// 200 全量：走完 serveContent 的完整 body 传输（io.CopyN 2432B）。
	b.Run("full_200", func(b *testing.B) {
		root, _ := newPayloadTree(b)
		benchLoop(b, newRouter(Config{Root: root}), "/payload.bin", nil)
	})

	// 206 部分内容：命中 parseRange + Seek(start) + Content-Range 头，
	// 只传输 5 字节，用于隔离「range 解析开销」与「body 传输开销」。
	b.Run("range_206", func(b *testing.B) {
		root, _ := newPayloadTree(b)
		benchLoop(b, newRouter(Config{Root: root}), "/payload.bin", func(req *http.Request) {
			req.Header.Set("Range", "bytes=0-4")
		})
	})

	// 304 未修改：命中 checkPreconditions 的 If-Modified-Since 短路，
	// serveContent 在写出 body 前即返回，是文件路径中最廉价的分支。
	b.Run("not_modified_304", func(b *testing.B) {
		root, mt := newPayloadTree(b)
		benchLoop(b, newRouter(Config{Root: root}), "/payload.bin", func(req *http.Request) {
			req.Header.Set("If-Modified-Since", mt.Format(http.TimeFormat))
		})
	})
}

// BenchmarkAutoIndexControlFlow 基准不产出内容的控制流分支：这三条路径都在
// os.Stat / 前缀校验附近就返回，度量的是 New 返回的 handler 的前置固定开销
// （Skipper 判定、filepath.Join、前缀校验、path.Clean、os.Stat）与 gin NoRoute
// 兜底链的成本，而非内容生成成本。
func BenchmarkAutoIndexControlFlow(b *testing.B) {
	root := b.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		b.Fatal(err)
	}
	router := newRouter(Config{Root: root})

	// 404：os.Stat 失败即返回，度量「不存在的条目」这条最常见的兜底路径。
	b.Run("not_found_404", func(b *testing.B) {
		benchLoop(b, router, "/missing.txt", nil)
	})

	// 403：filepath.Join 让 ".." 逃逸 root 后被前缀校验捕获，走 reject 分支。
	b.Run("path_traversal_403", func(b *testing.B) {
		benchLoop(b, router, "/../secret", nil)
	})

	// 301：目录 URL 缺尾斜杠时补斜杠重定向（保留原始查询参数）。
	b.Run("redirect_301", func(b *testing.B) {
		benchLoop(b, router, "/sub", nil)
	})
}
