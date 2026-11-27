package autoindex

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

// newTestTree 在 t.TempDir() 下构造测试目录树：files 为「相对路径 → 内容」，
// dirs 为需要额外创建的空目录。返回根目录绝对路径。
func newTestTree(t *testing.T, files map[string]string, dirs ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range dirs {
		require.NoError(t, os.MkdirAll(filepath.Join(root, d), 0o755))
	}
	for name, content := range files {
		p := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	return root
}

// newNoRouteRouter 按真实挂载契约把 autoindex 挂到 NoRoute 兜底。
func newNoRouteRouter(cfg Config) *gin.Engine {
	r := gin.New()
	r.NoRoute(New(cfg))
	return r
}

// do 发起一次请求并返回 recorder。
func do(r *gin.Engine, method, target string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	if mutate != nil {
		mutate(req)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// entryPos 返回目录列表中条目显示名（">name<" 锚点）在 body 中的位置。
func entryPos(t *testing.T, body, display string) int {
	t.Helper()
	pos := strings.Index(body, ">"+display+"<")
	require.GreaterOrEqual(t, pos, 0, "列表应包含条目 %q", display)
	return pos
}

// assertOrder 断言条目在列表中按给定顺序出现。
func assertOrder(t *testing.T, body string, displays ...string) {
	t.Helper()
	prev := -1
	for _, d := range displays {
		pos := entryPos(t, body, d)
		assert.Greater(t, pos, prev, "条目 %q 应排在 %v 之后（顺序 %+v）", d, displays, displays)
		prev = pos
	}
}

// sizeCellOf 返回列表中指定显示名条目的 Size 单元格文本（<td class="s">SIZE</td>）。
// 用于精确断言某条目的 Size 列，避免误命中其它行。
func sizeCellOf(t *testing.T, body, display string) string {
	t.Helper()
	i := strings.Index(body, ">"+display+"<")
	require.GreaterOrEqual(t, i, 0, "列表应包含条目 %q", display)
	const marker = `<td class="s">`
	j := strings.Index(body[i:], marker)
	require.GreaterOrEqual(t, j, 0, "条目 %q 应有 Size 单元格", display)
	start := i + j + len(marker)
	end := strings.Index(body[start:], "</td>")
	require.GreaterOrEqual(t, end, 0, "条目 %q 的 Size 单元格应闭合", display)
	return body[start : start+end]
}

func TestAutoIndex_DirListing_Root(t *testing.T) {
	root := newTestTree(t, map[string]string{
		"hello.txt":   strings.Repeat("x", 1536), // appendHumanSize → 1.5K
		"sub/note.md": "# note\n",
	}, "empty-dir")
	r := newNoRouteRouter(Config{Root: root})

	rec := do(r, http.MethodGet, "/", nil)

	assert.Equal(t, http.StatusOK, rec.Code, "NoRoute 链预置 404，handler 必须复位为 200")
	assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
	body := rec.Body.String()
	assert.Contains(t, body, "Index of /")
	assert.Contains(t, body, ">hello.txt<")
	assert.Contains(t, body, ">sub/<", "目录条目显示名带尾斜杠")
	assert.Contains(t, body, ">empty-dir/<")
	assert.Contains(t, body, "1.5K", "1536 字节应显示为 1.5K")
	assert.NotContains(t, body, "Parent Directory", "根目录列表不应有 Parent Directory 行")
	// FoldersFirst：目录在前、文件在后（尽管 "sub" 名称序在 "hello.txt" 之后）
	assert.Less(t, entryPos(t, body, "sub/"), entryPos(t, body, "hello.txt"))
	assertOrder(t, body, "empty-dir/", "sub/")
}

func TestAutoIndex_DirListing_Subdir(t *testing.T) {
	root := newTestTree(t, map[string]string{"sub/note.md": "# note\n"})
	r := newNoRouteRouter(Config{Root: root})

	rec := do(r, http.MethodGet, "/sub/", nil)

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "Index of /sub/", "标题应展示带尾斜杠的目录路径")
	assert.Contains(t, body, ">note.md<")
	assert.Contains(t, body, `href="../"`, "子目录应有 Parent Directory 行（相对链接 ../）")
	assert.Contains(t, body, "Parent Directory")
}

// TestAutoIndex_DirListing_EmptyDir 钉住空目录自身 GET 正确渲染列表页：
// 200 + 标题 + Parent Directory 行，且无任何条目行（空 Rows 情形实现已正确处理）。
func TestAutoIndex_DirListing_EmptyDir(t *testing.T) {
	root := newTestTree(t, nil, "emptydir")
	r := newNoRouteRouter(Config{Root: root})

	rec := do(r, http.MethodGet, "/emptydir/", nil)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "Index of /emptydir/")
	assert.Contains(t, body, "Parent Directory", "非根空目录应有 Parent Directory 行")
	// 表头(<tr><th>)之外，数据行仅 Parent Directory 一行(<tr><td>)，无任何条目行。
	assert.Equal(t, 1, strings.Count(body, "<tr><td>"), "空目录除 Parent Directory 外不应有条目行")
}

func TestAutoIndex_RedirectNoTrailingSlash(t *testing.T) {
	root := newTestTree(t, map[string]string{"sub/note.md": "# note\n"})
	r := newNoRouteRouter(Config{Root: root})

	rec := do(r, http.MethodGet, "/sub", nil)

	assert.Equal(t, http.StatusMovedPermanently, rec.Code)
	assert.Equal(t, "/sub/", rec.Header().Get("Location"))
}

func TestAutoIndex_RedirectPreservesQuery(t *testing.T) {
	root := newTestTree(t, map[string]string{"sub/note.md": "# note\n"})
	r := newNoRouteRouter(Config{Root: root})

	rec := do(r, http.MethodGet, "/sub?C=S;O=D", nil)

	assert.Equal(t, http.StatusMovedPermanently, rec.Code)
	assert.Equal(t, "/sub/?C=S;O=D", rec.Header().Get("Location"), "301 必须保留查询参数，避免丢失排序状态")
}

func TestAutoIndex_FileDownload(t *testing.T) {
	content := "Hello, autoindex!\n" + strings.Repeat("payload-", 32)
	root := newTestTree(t, map[string]string{"hello.txt": content})
	r := newNoRouteRouter(Config{Root: root})

	rec := do(r, http.MethodGet, "/hello.txt", nil)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, content, rec.Body.String())
}

func TestAutoIndex_RangeRequest(t *testing.T) {
	content := strings.Repeat("0123456789", 100) // 1000 字节
	root := newTestTree(t, map[string]string{"data.bin": content})
	r := newNoRouteRouter(Config{Root: root})

	rec := do(r, http.MethodGet, "/data.bin", func(req *http.Request) {
		req.Header.Set("Range", "bytes=0-4")
	})
	assert.Equal(t, http.StatusPartialContent, rec.Code)
	assert.Equal(t, "bytes 0-4/1000", rec.Header().Get("Content-Range"))
	assert.Equal(t, "01234", rec.Body.String())

	rec = do(r, http.MethodGet, "/data.bin", func(req *http.Request) {
		req.Header.Set("Range", "bytes=995-999")
	})
	assert.Equal(t, http.StatusPartialContent, rec.Code)
	assert.Equal(t, "bytes 995-999/1000", rec.Header().Get("Content-Range"))
	assert.Equal(t, "56789", rec.Body.String())
}

func TestAutoIndex_NotModified(t *testing.T) {
	root := newTestTree(t, map[string]string{"hello.txt": "content"})
	mtime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, os.Chtimes(filepath.Join(root, "hello.txt"), mtime, mtime))
	r := newNoRouteRouter(Config{Root: root})

	rec := do(r, http.MethodGet, "/hello.txt", func(req *http.Request) {
		req.Header.Set("If-Modified-Since", mtime.Format(http.TimeFormat))
	})

	assert.Equal(t, http.StatusNotModified, rec.Code)
	assert.Empty(t, rec.Body.String())
}

func TestAutoIndex_HEAD(t *testing.T) {
	content := strings.Repeat("y", 256)
	root := newTestTree(t, map[string]string{"hello.txt": content})
	r := newNoRouteRouter(Config{Root: root})

	rec := do(r, http.MethodHead, "/hello.txt", nil)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Body.String(), "HEAD 响应不应有 body")
	assert.Equal(t, "256", rec.Header().Get("Content-Length"))
}

func TestAutoIndex_PathTraversal(t *testing.T) {
	root := newTestTree(t, map[string]string{"sub/note.md": "# note\n"})
	r := newNoRouteRouter(Config{Root: root})

	tests := []struct {
		name   string
		target string
	}{
		{"dotdot-escape", "/../secret"},
		{"dotdot-double-escape", "/../../etc/passwd"},
		{"dotdot-through-subdir", "/sub/../../secret"},
		{"encoded-dotdot", "/%2e%2e/secret"},
		{"encoded-slash-variant", "/..%2fsecret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(r, http.MethodGet, tt.target, nil)
			assert.Equal(t, http.StatusForbidden, rec.Code)
			assert.Equal(t, "[403] forbidden", rec.Body.String())
		})
	}
}

func TestAutoIndex_NotFound(t *testing.T) {
	root := newTestTree(t, map[string]string{"sub/note.md": "# note\n"})
	r := newNoRouteRouter(Config{Root: root})

	rec := do(r, http.MethodGet, "/nope.txt", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "[404] not found", rec.Body.String())

	rec = do(r, http.MethodGet, "/sub/nope.txt", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "[404] not found", rec.Body.String())
}

func TestAutoIndex_IndexIgnore(t *testing.T) {
	root := newTestTree(t, map[string]string{
		"keep.txt":    "keep\n",
		"x.tmp":       "tmp\n",
		".git/config": "[core]\n",
	})
	r := newNoRouteRouter(Config{Root: root, IndexIgnore: []string{".git", "*.tmp"}})

	rec := do(r, http.MethodGet, "/", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, ">keep.txt<")
	assert.NotContains(t, body, ".git", "IndexIgnore 命中的目录不应出现在列表中")
	assert.NotContains(t, body, "x.tmp", "IndexIgnore 命中的文件不应出现在列表中")

	// IndexIgnore 仅隐藏列表条目，不阻断直接 URL 访问（非访问控制）
	rec = do(r, http.MethodGet, "/x.tmp", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "tmp\n", rec.Body.String())

	rec = do(r, http.MethodGet, "/.git/config", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "[core]\n", rec.Body.String())
}

func TestAutoIndex_SortDefault_NameAsc(t *testing.T) {
	root := newTestTree(t, map[string]string{
		"c.txt": strings.Repeat("c", 200),
		"a.txt": strings.Repeat("a", 300),
		"b.txt": strings.Repeat("b", 100),
		"d.txt": strings.Repeat("d", 200),
	})
	r := newNoRouteRouter(Config{Root: root})

	rec := do(r, http.MethodGet, "/", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assertOrder(t, rec.Body.String(), "a.txt", "b.txt", "c.txt", "d.txt")
}

func TestAutoIndex_SortBySize(t *testing.T) {
	// a=300B, b=100B, c=200B, d=200B（c/d 同大小，验证次级键名称升序）
	root := newTestTree(t, map[string]string{
		"c.txt": strings.Repeat("c", 200),
		"a.txt": strings.Repeat("a", 300),
		"b.txt": strings.Repeat("b", 100),
		"d.txt": strings.Repeat("d", 200),
	})
	r := newNoRouteRouter(Config{Root: root})

	rec := do(r, http.MethodGet, "/?C=S;O=A", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assertOrder(t, rec.Body.String(), "b.txt", "c.txt", "d.txt", "a.txt")

	// 降序：主键字节数降序，同大小时次级键仍为名称升序（c 在 d 前）
	rec = do(r, http.MethodGet, "/?C=S;O=D", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assertOrder(t, rec.Body.String(), "a.txt", "c.txt", "d.txt", "b.txt")
}

func TestAutoIndex_SortByMtime(t *testing.T) {
	root := newTestTree(t, map[string]string{
		"f1.txt": "1",
		"f2.txt": "2",
		"f3.txt": "3",
	})
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	times := map[string]time.Duration{
		"f1.txt": 2 * time.Hour, // 最新
		"f2.txt": 0,             // 最旧
		"f3.txt": time.Hour,     // 居中
	}
	for name, offset := range times {
		mt := base.Add(offset)
		require.NoError(t, os.Chtimes(filepath.Join(root, name), mt, mt))
	}
	r := newNoRouteRouter(Config{Root: root})

	rec := do(r, http.MethodGet, "/?C=M;O=A", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assertOrder(t, rec.Body.String(), "f2.txt", "f3.txt", "f1.txt")

	rec = do(r, http.MethodGet, "/?C=M;O=D", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assertOrder(t, rec.Body.String(), "f1.txt", "f3.txt", "f2.txt")
}

func TestAutoIndex_SortInvalidCFallsBackDefault(t *testing.T) {
	root := newTestTree(t, map[string]string{
		"c.txt": strings.Repeat("c", 200),
		"a.txt": strings.Repeat("a", 300),
		"b.txt": strings.Repeat("b", 100),
	})
	r := newNoRouteRouter(Config{Root: root})

	// 非法 C 回退默认名称列，O 缺省为升序
	rec := do(r, http.MethodGet, "/?C=X", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assertOrder(t, rec.Body.String(), "a.txt", "b.txt", "c.txt")

	// C/O 独立解析：非法 C 回退名称列，O=D 仍生效 → 名称降序
	rec = do(r, http.MethodGet, "/?C=X;O=D", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assertOrder(t, rec.Body.String(), "c.txt", "b.txt", "a.txt")
}

func TestAutoIndex_HeaderSortLinks(t *testing.T) {
	root := newTestTree(t, map[string]string{"a.txt": "a"})
	r := newNoRouteRouter(Config{Root: root})

	// 默认 C=N;O=A：当前列 Name 的链接切换为降序，其余列为升序
	rec := do(r, http.MethodGet, "/", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `href="?C=N;O=D"`)
	assert.Contains(t, body, `href="?C=M;O=A"`)
	assert.Contains(t, body, `href="?C=S;O=A"`)

	// C=S;O=A：Size 列切换为降序
	rec = do(r, http.MethodGet, "/?C=S;O=A", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	body = rec.Body.String()
	assert.Contains(t, body, `href="?C=S;O=D"`, "当前列且升序时链接应切换为降序")
	assert.Contains(t, body, `href="?C=N;O=A"`)
	assert.Contains(t, body, `href="?C=M;O=A"`)

	// C=S;O=D：Size 列回到升序
	rec = do(r, http.MethodGet, "/?C=S;O=D", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `href="?C=S;O=A"`)
}

func TestAutoIndex_Skipper(t *testing.T) {
	root := newTestTree(t, map[string]string{"hello.txt": "content"})

	// Skipper 命中：不复位状态、不做文件系统探测，NoRoute 下由 gin 落默认 404
	r := newNoRouteRouter(Config{
		Root:    root,
		Skipper: func(c *gin.Context) bool { return true },
	})
	rec := do(r, http.MethodGet, "/hello.txt", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "404 page not found", rec.Body.String())

	// Skipper 未命中：正常服务
	r = newNoRouteRouter(Config{
		Root:    root,
		Skipper: func(c *gin.Context) bool { return false },
	})
	rec = do(r, http.MethodGet, "/hello.txt", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "content", rec.Body.String())
}

func TestAutoIndex_RejectHandler(t *testing.T) {
	root := newTestTree(t, map[string]string{"hello.txt": "content"})

	var gotReason string
	r := newNoRouteRouter(Config{
		Root: root,
		RejectHandler: func(c *gin.Context, reason string) {
			gotReason = reason
			c.JSON(http.StatusForbidden, gin.H{"reason": reason})
		},
	})

	rec := do(r, http.MethodGet, "/../secret", nil)
	assert.Equal(t, http.StatusForbidden, rec.Code, "回调写入的状态码应生效")
	assert.JSONEq(t, `{"reason":"autoindex.forbidden"}`, rec.Body.String())
	assert.Equal(t, ReasonForbidden, gotReason, "回调应收到 ReasonForbidden")
}

func TestNew_Panics(t *testing.T) {
	root := newTestTree(t, map[string]string{"hello.txt": "content"})
	filePath := filepath.Join(root, "hello.txt")
	nonexistent := filepath.Join(root, "nonexistent")

	// 无底层错误的纯字符串 panic：Root 为空 / 存在但不是目录，消息平台无关，
	// 用 PanicsWithValue 钉住精确值；消息必须含具体 Root 路径，便于定位配置笔误。
	assert.PanicsWithValue(t, "autoindex: Root must not be empty",
		func() { New(Config{}) }, "Root 为空必须 panic")
	assert.PanicsWithValue(t,
		fmt.Sprintf("autoindex: Root %q must be an existing directory: not a directory", filePath),
		func() { New(Config{Root: filePath}) }, "Root 是文件必须 panic")

	// Root stat 失败：消息纳入底层 err（ENOENT/EACCES/ENOTDIR/EIO 不再压成同一文案，
	// 权限问题不再被误诊为「路径不存在」）。err 文本随 OS 变化（darwin/Linux/Windows
	// 各异），无法跨平台精确匹配，故改为 recover + 分要素断言：① panic 值仍为 string
	// （形态契约，对齐 compression/timeout 等）；② 稳定前缀与契约主体；③ 具体路径；
	// ④ 含同一路径 os.Stat 的底层错误——oracle 由本机实时计算，判据不依赖任何硬编码
	// errno 文本，原精确匹配消息的全部语义要素仍被逐一钉住，且新增了 err 尾部断言。
	func() {
		defer func() {
			r := recover()
			if !assert.NotNil(t, r, "Root stat 失败必须 panic") {
				return
			}
			msg, ok := r.(string)
			if !assert.Truef(t, ok, "panic 值应为 string 形态（对齐 compression/timeout），实际为 %T", r) {
				return
			}
			assert.Contains(t, msg, "autoindex: Root ", "应含稳定消息前缀")
			assert.Contains(t, msg, "must be an existing directory", "应含契约主体")
			assert.Containsf(t, msg, nonexistent, "应含具体 Root 路径: %q", msg)
			_, statErr := os.Stat(nonexistent)
			require.Error(t, statErr, "前提：不存在路径的 stat 必须失败")
			assert.Containsf(t, msg, statErr.Error(),
				"应含底层 err 以区分 ENOENT/EACCES 等故障类别: %q", msg)
		}()
		New(Config{Root: nonexistent})
	}()

	// error panic：IndexIgnore 非法模式属「带底层 err 的配置条目校验」，对齐 ipfilter
	// 的 fmt.Errorf(%w) 契约——panic 值必须是 error（PanicsWithValue 对 error panic 会
	// 失败，反之亦然），须能用 errors.Is 解包出 path.ErrBadPattern，且消息含具体模式。
	func() {
		defer func() {
			r := recover()
			if !assert.NotNil(t, r, "非法 glob 模式必须 panic") {
				return
			}
			err, ok := r.(error)
			if assert.Truef(t, ok, "panic 值应为 error 类型，实际为 %T", r) {
				assert.ErrorIs(t, err, path.ErrBadPattern, "应包裹 path.ErrBadPattern")
				assert.Contains(t, err.Error(), `"[":`, "panic 消息应包含具体的 IndexIgnore 模式")
			}
		}()
		New(Config{Root: root, IndexIgnore: []string{"["}})
	}()

	assert.NotPanics(t, func() { New(Config{Root: root, IndexIgnore: []string{".git", "*.tmp"}}) })
}

func TestNew_RelativeRootResolvedToAbs(t *testing.T) {
	// New 构造期把 Root 解析为绝对路径：相对路径经 t.Chdir 后仍可正确服务
	root := newTestTree(t, map[string]string{"hello.txt": "content"})
	parent := filepath.Dir(root)
	base := filepath.Base(root)
	t.Chdir(parent)

	r := newNoRouteRouter(Config{Root: base})
	rec := do(r, http.MethodGet, "/hello.txt", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "content", rec.Body.String())
}

func TestHumanSize(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{511, "511"},
		{1023, "1023"},
		{1024, "1K"},
		{1536, "1.5K"},
		{2048, "2K"},
		{1024 * 1024, "1M"},
		{1536 * 1024, "1.5M"},
		{1024 * 1024 * 1024, "1G"},
		{1536 * 1024 * 1024, "1.5G"},
		{3 * 1024 * 1024 * 1024, "3G"},
		// 舍入进位边界：val 落在 [1023.95,1024) 时 'f',1 会舍入为 "1024.0"，
		// 须进位到下一档而非显示 "1024K"/"1024M"。
		{1048575, "1M"},    // 1024*1024-1：进位边界
		{1048576, "1M"},    // 1024*1024
		{1073741823, "1G"}, // 1024*1024*1024-1：进位边界
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d", tt.n), func(t *testing.T) {
			assert.Equal(t, tt.want, string(appendHumanSize(nil, tt.n)))
		})
	}
}

// TestAutoIndex_RootSlashBoundary 钉住 Root="/" 的 prefix 边界：Root="/" 是唯一经 filepath.Abs 后仍以
// 分隔符结尾的路径，若直接拼 prefix 会得到 "//"（Windows 盘符根为 "C:\\"），导致所有子路径
// 被误拒 403 而 GET / 却能列出顶层（矛盾态）。t.TempDir() 无法构造 "/"，只能用真实文件
// 系统根；断言选取跨系统稳定属性，且在 "/" 不可访问时跳过。
func TestAutoIndex_RootSlashBoundary(t *testing.T) {
	if _, err := os.Stat("/"); err != nil {
		t.Skipf("文件系统根不可访问: %v", err)
	}
	r := newNoRouteRouter(Config{Root: "/"})

	// 根本身可列出（200）
	rec := do(r, http.MethodGet, "/", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Index of /")

	// 穿越在 root 处被 clamp：/../../ 归一化回 /（无法向上逃逸），仍 200 列根而非 403
	rec = do(r, http.MethodGet, "/../../", nil)
	assert.Equal(t, http.StatusOK, rec.Code, "穿越无法逃逸到 root 之上，归一化为 /")
	assert.Contains(t, rec.Body.String(), "Index of /")

	// 核心回归：prefix 不得拼成 "<sep><sep>" 使子路径被误拒 403。
	// 该断言不读目录内容（prefix 校验先于 stat），Unix/Windows 均稳定钉住 bug。
	rec = do(r, http.MethodGet, "/etc/", nil)
	assert.NotEqual(t, http.StatusForbidden, rec.Code, "Root=\"/\" 时子路径不应再被误拒 403")
	// /etc 真实存在的系统（Unix）上应正常列出 200
	if info, err := os.Stat("/etc"); err == nil && info.IsDir() {
		assert.Equal(t, http.StatusOK, rec.Code)
	}
}

// TestAutoIndex_RedirectEncodesLocation 钉住 301 Location 重编码：目录名含 %/#/空格（`?` 仅非 Windows）时，
// 无尾斜杠请求触发的 301 Location 必须重新百分号编码，且跟随 Location 能正确列出该目录
// （若用解码后路径直接拼接，? 会污染 query、# 截断 fragment、% 形成非法转义）。
func TestAutoIndex_RedirectEncodesLocation(t *testing.T) {
	dirs := []string{"50%off", "tag#1", "my dir"}
	tests := []struct{ urlPath, wantLoc string }{
		{"/50%off", "/50%25off/"},
		{"/tag#1", "/tag%231/"},
		{"/my dir", "/my%20dir/"},
	}
	if runtime.GOOS != "windows" { // `?` 在 Windows 文件名中非法
		dirs = append(dirs, "what?")
		tests = append(tests, struct{ urlPath, wantLoc string }{"/what?", "/what%3F/"})
	}
	root := newTestTree(t, nil, dirs...)
	r := newNoRouteRouter(Config{Root: root})

	for _, tt := range tests {
		t.Run(tt.urlPath, func(t *testing.T) {
			// URL.Path 为解码后形态（net/http 预解码），模拟无尾斜杠的目录请求
			rec := do(r, http.MethodGet, "/", func(req *http.Request) { req.URL.Path = tt.urlPath })
			require.Equal(t, http.StatusMovedPermanently, rec.Code)
			assert.Equal(t, tt.wantLoc, rec.Header().Get("Location"), "Location 必须重新百分号编码")

			// 跟随 Location：按编码路径请求应正确列出该目录
			follow := do(r, http.MethodGet, tt.wantLoc, nil)
			assert.Equal(t, http.StatusOK, follow.Code, "跟随 Location 应能正确列出目录")
			assert.Contains(t, follow.Body.String(), "Index of")
		})
	}
}

// TestAutoIndex_MethodNotAllowed 钉住 405 方法门：仅 GET/HEAD 服务内容，其余方法（打目录或文件）
// 返回 405 + Allow 头 + 约定纯文本 body（对齐 Apache mod_autoindex / nginx autoindex）。
func TestAutoIndex_MethodNotAllowed(t *testing.T) {
	root := newTestTree(t, map[string]string{"hello.txt": "content"}, "sub")
	r := newNoRouteRouter(Config{Root: root})

	methods := []string{
		http.MethodPost, http.MethodPut, http.MethodDelete,
		http.MethodOptions, http.MethodPatch,
	}
	// 打「目录」（根 /、子目录 /sub/）与「文件」（/hello.txt）两类目标
	targets := []string{"/", "/sub/", "/hello.txt"}
	for _, m := range methods {
		for _, target := range targets {
			t.Run(m+" "+target, func(t *testing.T) {
				rec := do(r, m, target, nil)
				assert.Equal(t, http.StatusMethodNotAllowed, rec.Code, "非 GET/HEAD 应 405")
				assert.Equal(t, "GET, HEAD", rec.Header().Get("Allow"), "405 应带 Allow 头")
				assert.Equal(t, "[405] method not allowed", rec.Body.String())
			})
		}
	}

	// GET/HEAD 不受方法门影响
	rec := do(r, http.MethodGet, "/hello.txt", nil)
	assert.Equal(t, http.StatusOK, rec.Code, "GET 仍正常服务")
	rec = do(r, http.MethodHead, "/hello.txt", nil)
	assert.Equal(t, http.StatusOK, rec.Code, "HEAD 仍正常服务")
}

// TestAutoIndex_SkipperPrecedesMethodGate 钉住方法门的位置契约：方法门必须置于 Skipper
// 判定之后——Skipper 命中时即使非 GET/HEAD 也应 c.Next() 落回 gin 默认 404（而非被方法门
// 拦成 405），从而保持「Skipper 命中不复位状态、不碰文件系统」的契约。
func TestAutoIndex_SkipperPrecedesMethodGate(t *testing.T) {
	root := newTestTree(t, map[string]string{"hello.txt": "content"})
	r := newNoRouteRouter(Config{
		Root:    root,
		Skipper: func(c *gin.Context) bool { return true },
	})
	// Skipper 命中：POST 也应 c.Next() 落回 gin 默认 404，而非方法门的 405
	rec := do(r, http.MethodPost, "/hello.txt", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code, "Skipper 命中先于方法门，落回 gin 默认 404")
	assert.Equal(t, "404 page not found", rec.Body.String())
	assert.Empty(t, rec.Header().Get("Allow"), "未经过方法门，不应写 Allow 头")
}

// TestAutoIndex_HrefEncoding 钉住 href 百分号编码：文件名以 ?/# 开头或含空格/% 时，列表 href 须为正确
// 百分号编码（点击可达），显示名保持原始名；并验证不与 html/template 的 href 上下文转义
// 产生双重编码（出现 %3F 而非 %253F）。
func TestAutoIndex_HrefEncoding(t *testing.T) {
	files := map[string]string{
		"#h.txt":   "h",
		"a b.txt":  "ab",
		"100%.txt": "pct",
	}
	if runtime.GOOS != "windows" { // `?` 在 Windows 文件名中非法
		files["?q.txt"] = "q"
	}
	root := newTestTree(t, files)
	r := newNoRouteRouter(Config{Root: root})

	rec := do(r, http.MethodGet, "/", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	// 显示名保持原始未编码名
	assert.Contains(t, body, ">#h.txt<")
	assert.Contains(t, body, ">a b.txt<")
	assert.Contains(t, body, ">100%.txt<")
	// href 为正确百分号编码
	assert.Contains(t, body, `href="%23h.txt"`, "# 应编码为 %23")
	assert.Contains(t, body, `href="a%20b.txt"`, "空格应编码为 %20")
	assert.Contains(t, body, `href="100%25.txt"`, "% 应编码为 %25")

	if runtime.GOOS != "windows" {
		assert.Contains(t, body, ">?q.txt<")
		assert.Contains(t, body, `href="%3Fq.txt"`, "? 应编码为 %3F")
		assert.NotContains(t, body, "%253F", "不应与 html/template 双重编码为 %253F")
	}
}

// TestAutoIndex_PermissionDenied 钉住权限错误映射：文件系统权限错误（EACCES）映射为 403 而非
// 500/404（对齐 Apache/nginx）。覆盖三条错误分支：Open（不可读文件）、ReadDir（不可读
// 目录）、Stat（父目录缺 x 权限）。root 身份或 Windows 权限模型下无法稳定构造 EACCES，跳过。
func TestAutoIndex_PermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 权限模型不同，无法稳定构造 EACCES")
	}
	if os.Geteuid() == 0 {
		t.Skip("以 root 运行会绕过文件权限检查，无法构造 EACCES")
	}

	root := t.TempDir()
	// 不可读文件（0o000）：Open 触发 EACCES → 403
	secretFile := filepath.Join(root, "secret.txt")
	require.NoError(t, os.WriteFile(secretFile, []byte("secret"), 0o000))
	// 不可读且不可进入的目录（0o000，内含文件）：
	//   GET /secretdir/          → ReadDir EACCES → 403
	//   GET /secretdir/inner.txt → stat 需 secretdir 的 x 权限 → EACCES → 403
	secretDir := filepath.Join(root, "secretdir")
	require.NoError(t, os.MkdirAll(secretDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(secretDir, "inner.txt"), []byte("x"), 0o644))
	require.NoError(t, os.Chmod(secretDir, 0o000))
	// t.TempDir 清理需删除 0o000 条目，先恢复权限（Cleanup LIFO，先于 TempDir 的 RemoveAll 运行）
	t.Cleanup(func() {
		_ = os.Chmod(secretFile, 0o644)
		_ = os.Chmod(secretDir, 0o755)
	})

	r := newNoRouteRouter(Config{Root: root})

	t.Run("unreadable file Open → 403", func(t *testing.T) {
		rec := do(r, http.MethodGet, "/secret.txt", nil)
		assert.Equal(t, http.StatusForbidden, rec.Code, "Open EACCES 应映射 403")
		assert.Equal(t, "[403] forbidden", rec.Body.String())
	})
	t.Run("unreadable dir ReadDir → 403", func(t *testing.T) {
		rec := do(r, http.MethodGet, "/secretdir/", nil)
		assert.Equal(t, http.StatusForbidden, rec.Code, "ReadDir EACCES 应映射 403")
		assert.Equal(t, "[403] forbidden", rec.Body.String())
	})
	t.Run("stat through no-x dir → 403", func(t *testing.T) {
		rec := do(r, http.MethodGet, "/secretdir/inner.txt", nil)
		assert.Equal(t, http.StatusForbidden, rec.Code, "stat EACCES 应映射 403")
		assert.Equal(t, "[403] forbidden", rec.Body.String())
	})
}

// TestAutoIndex_PermissionDenied_RejectHandler 钉住权限 403 的回调覆盖：权限 403 与穿越 403 一样经统一拒绝
// 契约走 cfg.RejectHandler，且 reason 为 ReasonPermission（新增导出常量，ReasonForbidden 不变）。
func TestAutoIndex_PermissionDenied_RejectHandler(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 权限模型不同，无法稳定构造 EACCES")
	}
	if os.Geteuid() == 0 {
		t.Skip("以 root 运行会绕过文件权限检查，无法构造 EACCES")
	}

	root := t.TempDir()
	secretFile := filepath.Join(root, "secret.txt")
	require.NoError(t, os.WriteFile(secretFile, []byte("secret"), 0o000))
	t.Cleanup(func() { _ = os.Chmod(secretFile, 0o644) })

	var gotReason string
	r := newNoRouteRouter(Config{
		Root: root,
		RejectHandler: func(c *gin.Context, reason string) {
			gotReason = reason
			c.String(http.StatusForbidden, "custom: "+reason)
		},
	})
	rec := do(r, http.MethodGet, "/secret.txt", nil)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, "custom: "+ReasonPermission, rec.Body.String())
	assert.Equal(t, ReasonPermission, gotReason, "权限拒绝应以 ReasonPermission 走 RejectHandler")
}

// removeOnSetWriteDeadline 包装 gin.ResponseWriter，在 autoindex 清除写截止
// （Stat 成功后、os.Open 前的固定调用点）删除目标文件，用以在单线程测试里
// 确定性构造「Stat 成功 + Open ENOENT」的 TOCTOU 窗口。
type removeOnSetWriteDeadline struct {
	gin.ResponseWriter
	remove func()
}

// SetWriteDeadline 实现 http.ResponseController 探测的接口；autoindex 对该调用
// best-effort 忽略错误，返回 nil 即可。
func (w *removeOnSetWriteDeadline) SetWriteDeadline(time.Time) error {
	w.remove()
	return nil
}

// TestAutoIndex_StatThenRemovedENOENT 钉住 Stat→Open/ReadDir 之间条目被删除
// （TOCTOU 窗口）的错误映射：ENOENT → 404 而非 500（该窗口内的删除属
// 客户端可见的路径缺失）。窗口无法在单线程请求中经由完整 handler 流程确定性构造
// （Stat 会先命中 ENOENT 走自己的 404 分支），按两个受控入口钉住：
//   - ReadDir 分支：删除目录后直接调用内部渲染路径 renderDirListing；
//   - Open 分支：借 SetWriteDeadline 侧门在 Stat 成功后、Open 前删除文件。
//
// fd 耗尽/EIO 等其余错误保持 500，由注释契约与既有 500 分支语义承载。
func TestAutoIndex_StatThenRemovedENOENT(t *testing.T) {
	t.Run("ReadDir ENOENT → 404", func(t *testing.T) {
		root := newTestTree(t, map[string]string{"doomed/x.txt": "x"})
		r := newNoRouteRouter(Config{Root: root})
		// 前置：目录存在时正常列出（Stat 与 ReadDir 均成功）
		rec := do(r, http.MethodGet, "/doomed/", nil)
		require.Equal(t, http.StatusOK, rec.Code)

		// 删除目录后完整请求会先在 Stat 处 ENOENT（404，既有行为），无法确定性
		// 进入 ReadDir 分支；直调内部渲染路径以钉住该分支的错误映射。
		require.NoError(t, os.RemoveAll(filepath.Join(root, "doomed")))

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/doomed/", nil)
		renderDirListing(c, "/doomed/", filepath.Join(root, "doomed"),
			ignoreFunc(func(string) bool { return false }),
			rejectFunc(func(c *gin.Context, reason, defaultMsg string) {
				c.String(http.StatusForbidden, defaultMsg)
				c.Abort()
			}))

		assert.Equal(t, http.StatusNotFound, w.Code,
			"目录在 Stat 成功后被删除（ReadDir ENOENT）应映射 404 而非 500")
		assert.Equal(t, "[404] not found", w.Body.String())
	})

	t.Run("Open ENOENT → 404", func(t *testing.T) {
		root := newTestTree(t, map[string]string{"doomed.txt": "content"})
		doomed := filepath.Join(root, "doomed.txt")

		// 前置中间件包一层 writer：autoindex 在 Stat 成功后、Open 前调用
		// SetWriteDeadline 清除写截止（该调用点先于 os.Open），在此时删除文件
		// → Open 失败于 ENOENT，确定性命中修复的映射分支。
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Writer = &removeOnSetWriteDeadline{ResponseWriter: c.Writer, remove: func() {
				_ = os.Remove(doomed)
			}}
			c.Next()
		})
		r.NoRoute(New(Config{Root: root}))

		rec := do(r, http.MethodGet, "/doomed.txt", nil)
		assert.Equal(t, http.StatusNotFound, rec.Code,
			"文件在 Stat 成功后被删除（Open ENOENT）应映射 404 而非 500")
		assert.Equal(t, "[404] not found", rec.Body.String())
	})
}

// TestAutoIndex_SymlinkSize 钉住符号链接的 Size 显示语义：符号链接条目的 Size 显示为 "-"（与目录一致），而非
// 链接目标路径字符串的字节长度（如 "76"）；symlink 仍归 files 组、显示名不带尾斜杠。
func TestAutoIndex_SymlinkSize(t *testing.T) {
	root := newTestTree(t, map[string]string{"target.txt": "hello"}, "targetdir")
	// 环境不支持符号链接（如未授权限的 Windows）时跳过
	if err := os.Symlink(filepath.Join(root, "target.txt"), filepath.Join(root, "link-to-file")); err != nil {
		t.Skipf("当前环境不支持符号链接: %v", err)
	}
	require.NoError(t, os.Symlink(filepath.Join(root, "targetdir"), filepath.Join(root, "link-to-dir")))

	r := newNoRouteRouter(Config{Root: root})
	rec := do(r, http.MethodGet, "/", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	// symlink 归 files 组：显示名不带尾斜杠，且出现在列表
	assert.Contains(t, body, ">link-to-file<")
	assert.Contains(t, body, ">link-to-dir<")
	// Size 列显示 "-"（e.Info() 为 lstat 语义，符号链接的 Size() 是目标路径长度而非内容大小）
	assert.Equal(t, "-", sizeCellOf(t, body, "link-to-file"), "文件 symlink 的 Size 应为 -")
	assert.Equal(t, "-", sizeCellOf(t, body, "link-to-dir"), "目录 symlink 的 Size 应为 -")
	// 对照：真实文件显示字节大小、真实目录显示 -
	assert.Equal(t, "5", sizeCellOf(t, body, "target.txt"), "普通文件显示字节大小")
	assert.Equal(t, "-", sizeCellOf(t, body, "targetdir/"), "真实目录 Size 为 -")
}
