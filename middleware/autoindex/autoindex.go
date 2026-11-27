// Package autoindex 提供 Apache mod_autoindex 风格的目录列表与静态文件下载。
//
// 本包是终端内容生成器（自行产出响应体、不调用 c.Next()），与库中其余管道中间件语义
// 类别不同，必须以 NoRoute 兜底挂载——orbit 下 engine.GetGinEngine().NoRoute(h)；裸 gin
// r.NoRoute(h)。也可挂在非根前缀 catch-all 路由（如 /files/*filepath），但本 handler 以
// URL 全路径相对 Root 映射、不剥离挂载前缀（/files/x → Root/files/x），且 gin 的 r.GET
// 不路由 HEAD（前缀挂载须另注册 r.HEAD），故更推荐 NoRoute 挂载。勿放进
// Use/RegisterMiddleware 链，否则每个请求都会触发文件系统 stat 并遮蔽已注册路由；orbit
// 下也勿与根级 StaticFS("/") 共存——其 catch-all 路由与内建 /ping、/metrics 冲突会
// panic。与 compression/timeout/security 的冲突规避见 README「AutoIndex」节。
//
// 终止契约：仅拒绝路径（403）在写出响应后无条件 c.Abort()；404/405/500 与正常
// 200/206/304/301 响应仅 return 而不 Abort——若在同一 NoRoute 链上于本 handler 之后再
// 注册其他 handler，非拒绝路径仍会继续执行后续 handler（Skipper 分支本就依赖 c.Next()
// 放行）。
//
// 目录列表支持 Apache 风格 ?C=N|M|S（名称/修改时间/大小）与 ?O=A|D（升/降序）排序，
// IndexIgnore glob 隐藏条目；文件经 http.ServeContent 下载，保留 Range/206、
// If-Modified-Since/304 与 HEAD 语义。
//
// 列表页由本包手写渲染（骨架常量 + 逐段 append 进单个预分配缓冲），不使用
// html/template，分配成本动因（实测数据）见「目录列表页骨架」一节。转义责任由本包显式
// 承担：文本上下文 → appendTextEscape（转义集与 htmlReplacementTable 逐字节一致，
// 与 html.EscapeString 有两处分歧：'+' → &#43; 与 NUL → U+FFFD）；href 属性上下文 →
// appendPathSegmentEscape（严格百分号编码，语法上不可能构成任何 scheme）。完整论证与
// 「有意不转义」清单见各函数注释。
//
// 注意：IndexIgnore 模式采用 Go path.Match 语义，[abc] 字符类有效（Apache 将其视为
// 字面量），属已知语义差异。
package autoindex

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// ReasonForbidden 路径穿越拒绝的原因常量，作为 RejectHandler 的 reason 参数。
// 值遵循全局 "<domain>.<cause>" 拒绝原因命名方案（domain=autoindex 自证拒绝
// 来源，cause 为 snake_case 状态描述），规则详见 README。
const ReasonForbidden = "autoindex.forbidden"

// ReasonPermission 文件系统权限拒绝（EACCES：目录/文件不可读，或父目录缺搜索权限）
// 的原因常量，作为 RejectHandler 的 reason 参数；命名遵循全局 "<domain>.<cause>" 方案。
const ReasonPermission = "autoindex.permission"

// Config 目录列表与静态文件下载配置。无 DefaultConfig——Root 必填且无合理
// 默认值，须显式构造（对齐 ipfilter/requestsize 约定）。
type Config struct {
	// Root 必填，服务的根目录。New 构造期解析为绝对路径；
	// 为空/不存在/非目录时构造期 panic（fail-fast，对齐库约定）。
	Root string
	// IndexIgnore glob 模式列表（path.Match 语义，匹配条目 base name），
	// 如 ".git"、"*.tmp"。仅从目录列表隐藏命中条目，不阻断直接 URL 访问
	// ——不是访问控制，勿把敏感文件放进 Root。非法模式构造期 panic。
	IndexIgnore []string
	// Skipper 返回 true 时跳过本 handler，直接 c.Next() 后返回
	//（NoRoute 挂载下由 gin 落默认 404）。
	Skipper func(*gin.Context) bool
	// RejectHandler 自定义拒绝时的响应逻辑（可选），覆盖路径穿越与文件系统权限
	// 拒绝的 403 路径。
	//
	// 契约：reason 为本包导出的拒绝原因常量（路径穿越为 ReasonForbidden，权限
	// 拒绝为 ReasonPermission）；回调负责写入状态码与响应体；回调返回后框架无条件
	// 调用 c.Abort() 终止后续处理器，无论回调内部是否已自行 Abort。为 nil 时返回
	// 默认响应（c.String 文本格式 "[403] forbidden"）。
	RejectHandler func(c *gin.Context, reason string)
}

// ---------------------------------------------------------------------------
// 目录列表页骨架（Apache mod_autoindex 风格：白底黑字、等宽对齐的朴素表格）
//
// 渲染重写轮由 html/template 改为「包级常量 + 手写拼接」，唯一动机是分配成本——模板
// 反射在列表路径上占 81.81% 的 allocs（walkRange 单帧 80.37%，约 34.7 allocs/行）与
// 约 38% 的 alloc_space，而其 CPU 占比只有 8.82%：「CPU 廉价但分配密集」，只有整体替换
// 才能消除。骨架拆成下面的常量，逐字节等价于原模板文本（含 {{- ...}} 的空白裁剪结果）；
// 护栏测试以**内联 golden**（非模板现场渲染）钉住输出，故本处任何字节偏差都会 RED。
// ---------------------------------------------------------------------------

// listingStyle 是内联 <style> 块的内容（不含 <style>/</style> 标记本身）。必须保持
// 内联：autoindex_conflict_test.go 的 CSP 冲突测试依赖它，且护栏测试断言
// strings.Count(body, "<style>") == 1。
const listingStyle = `body { background: #fff; color: #000; font-family: monospace; margin: 2em; }
h1 { font-size: 1.2em; font-weight: bold; }
table { border-collapse: collapse; }
th { text-align: left; font-weight: bold; }
th.c, td.c { padding-left: 2em; }
td, th { white-space: pre; }
td.s, th.s { text-align: right; padding-left: 2em; }
hr { border: none; border-top: 1px solid #000; margin: 1em 0; }
a { color: #000; text-decoration: none; }
a:hover { text-decoration: underline; }
`

// 页面骨架常量。占位符位置即拼接顺序：
//
//	preTitle {Path} midStyle {Path} postH1 {NameLink} nameHdr {MtimeLink} mtimeHdr {SizeLink} sizeHdr
//	[parentRow]
//	(rowOpen {href} rowMid {name} rowCellC {mtime} rowCellS {size} rowClose) × N
//	tail
const (
	listingPreTitle = "<!DOCTYPE html>\n<html>\n<head>\n<meta charset=\"utf-8\">\n<title>Index of "
	listingMidStyle = "</title>\n<style>\n" + listingStyle + "</style>\n</head>\n<body>\n<h1>Index of "
	listingPostH1   = "</h1>\n<hr>\n<table>\n<tr><th><a href=\""
	listingNameHdr  = "\">Name</a></th><th class=\"c\"><a href=\""
	listingMtimeHdr = "\">Last modified</a></th><th class=\"s\"><a href=\""
	listingSizeHdr  = "\">Size</a></th></tr>\n"
	// listingParentRow 是原模板 {{if .Parent}} 分支的等价物：非根目录才写这一行。
	// href 恒为相对形式 "../"，使 NoRoute 根挂载与前缀挂载两种形态都可移植。
	listingParentRow = "<tr><td><a href=\"../\">Parent Directory</a></td><td class=\"c\">-</td><td class=\"s\">-</td></tr>\n"
	listingRowOpen   = "<tr><td><a href=\""
	listingRowMid    = "\">"
	listingRowCellC  = "</a></td><td class=\"c\">"
	listingRowCellS  = "</td><td class=\"s\">"
	listingRowClose  = "</td></tr>\n"
	listingTail      = "</table>\n<hr>\n</body>\n</html>\n"
)

// listingFixedLen 是不含路径、表头链接与数据行的骨架字节数，编译期求值，供页面缓冲
// 预分配使用。由被求和常量自身推导，与骨架内容不可能失配。
const listingFixedLen = len(listingPreTitle) + len(listingMidStyle) + len(listingPostH1) +
	len(listingNameHdr) + len(listingMtimeHdr) + len(listingSizeHdr) + len(listingTail)

// listingRowFixedLen 是单条数据行中「与条目无关」的字节数：行内标记 + 定长 mtime（宽度
// 16 由参考布局字面量 "2006-01-02 15:04" 在编译期推导）+ Size 的宽松上界 8 字节（最长
// 真实形态 "1023.9K" 为 7；8 是上界而非某字面量的宽度，无法由 len() 推导）。两个长度事实
// 由 TestRender_FixedWidthContracts 钉住：mtime 恒 16（年份直接展开区间 [0,9999]），
// appendHumanSize 在可达域 n ≥ 0 内恒 ≤ 8。
const listingRowFixedLen = len(listingRowOpen) + len(listingRowMid) + len(listingRowCellC) +
	len(listingRowCellS) + len(listingRowClose) + len("2006-01-02 15:04") + 8

// entryRow 是目录列表中的一行。渲染重写后**不再**预存展示字段（href / 显示名 / 时间串 /
// 大小串）：写出时由排序键现场推导并直接追加进页面缓冲——预存形态每条目产生约 4 次分配
// （时间格式化、大小格式化、name+"/"、URL 编码），现场推导零分配；结构体同时由 112 B
// 缩到 56 B，切片内存减半。
//
// 三个 sort* 字段既是排序键也是展示数据的唯一来源。排序数据结构保持值切片不变：alloc
// profile 中排序环节零帧，已否证「改索引排序 / []*entryRow」的收益，勿改。
type entryRow struct {
	sortName  string    // 原始 base name：排序主/次键，同时是显示名与 href 的来源
	sortSize  int64     // 原始字节大小：排序键 S；noSize 为真时不展示
	sortMtime time.Time // 原始修改时间：排序键 M，展示时格式化为 YYYY-MM-DD HH:MM

	isDir  bool // 目录：显示名与 href 追加未转义的 "/"，Size 展示 "-"
	noSize bool // 目录或符号链接：Size 展示 "-"（符号链接的 Size() 是目标路径长度而非内容大小）
}

// rejectFunc 是统一拒绝回调的签名（契约见 Config.RejectHandler）。命名类型使 reason/
// defaultMsg 两个 string 参数在闭包定义与签名处携带参数名语义（对齐 ipfilter 范式）。
type rejectFunc func(c *gin.Context, reason, defaultMsg string)

// ignoreFunc 是 IndexIgnore 命中判断的签名（仅影响列表渲染，不改变条目可达性）。
// 命名类型使 name 参数在闭包定义与 renderDirListing 签名处携带参数名语义（对齐
// rejectFunc 的处理）。
type ignoreFunc func(name string) bool

// New 创建目录列表/静态文件下载终端 handler。配置非法时构造期 panic（fail-fast，
// 触发条件见 Config 各字段文档）；挂载形态（NoRoute 兜底、勿进 Use 链）与终止契约
// 见包注释。
func New(cfg Config) gin.HandlerFunc {
	if cfg.Root == "" {
		panic("autoindex: Root must not be empty")
	}
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		panic(fmt.Errorf("autoindex: resolve Root %q failed: %w", cfg.Root, err))
	}
	// stat 失败与「不是目录」必须分消息：stat 失败的原因有 ENOENT/EACCES/ENOTDIR/EIO
	// 等，吞掉底层 err 会把父目录缺搜索权限（EACCES）误诊为「路径不存在」，削弱
	// fail-fast 可定位性。panic 值保持 string 形态（对齐 compression/timeout/ratelimiter/
	// requestsize/security 的纯字符串校验 panic），仅把 err 追加进消息尾部——代价是消息
	// 含 OS 相关文本，测试须断言稳定前缀 + 本机实时计算的 err oracle，不得精确匹配全文。
	info, err := os.Stat(root)
	if err != nil {
		panic(fmt.Sprintf("autoindex: Root %q must be an existing directory: %v", cfg.Root, err))
	}
	if !info.IsDir() {
		panic(fmt.Sprintf("autoindex: Root %q must be an existing directory: not a directory", cfg.Root))
	}
	for _, p := range cfg.IndexIgnore {
		if _, err := path.Match(p, ""); err != nil {
			panic(fmt.Errorf("autoindex: invalid IndexIgnore pattern %q: %w", p, err))
		}
	}

	// prefix 构造期一次性计算：root 未以分隔符结尾才补分隔符。root="/" 时补分隔符
	// 会拼成 "//"（Windows 盘符根为 "C:\\"），使任何子路径的 HasPrefix 校验都失败
	// 而被误拒 403；提到构造期同时消除每请求的拼接分配。root="/" 时 prefix 恒为
	// "/"，语义退化为「全盘可服务」，与用户显式配置的意图一致。
	prefix := root
	if !strings.HasSuffix(root, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}

	// reject 构造期捕获 cfg.RejectHandler，仅在拒绝路径（穿越/权限）调用，
	// 服务主路径零新增开销（统一拒绝契约，对齐 ipfilter/apikey 多 reason 范式）。
	reject := rejectFunc(func(c *gin.Context, reason, defaultMsg string) {
		if cfg.RejectHandler != nil {
			cfg.RejectHandler(c, reason)
		} else {
			c.String(http.StatusForbidden, defaultMsg)
		}
		c.Abort()
	})

	// ignored 判断条目 base name 是否命中任一 IndexIgnore 模式（仅影响列表渲染）。
	ignored := ignoreFunc(func(name string) bool {
		for _, p := range cfg.IndexIgnore {
			// 模式已在构造期经 path.Match 校验，此处不会返回错误
			if matched, _ := path.Match(p, name); matched {
				return true
			}
		}
		return false
	})

	return func(c *gin.Context) {
		// Skipper 判定先于状态复位：跳过路径不复位状态、不碰文件系统，
		// NoRoute 下保持 gin serveError 预置的 404，落回 gin 默认响应。
		if cfg.Skipper != nil && cfg.Skipper(c) {
			c.Next()
			return
		}

		// 方法门：仅 GET/HEAD 服务内容（对齐 Apache mod_autoindex 与 nginx autoindex）。
		// 置于 Skipper 判定之后是顺序不变式：Skipper 命中仍落回 gin 默认 404；
		// c.String 显式写 405 覆盖 gin NoRoute 链预置的 404。
		if m := c.Request.Method; m != http.MethodGet && m != http.MethodHead {
			c.Header("Allow", "GET, HEAD")
			c.String(http.StatusMethodNotAllowed, "[405] method not allowed")
			return
		}

		// gin 的 serveError 进入 NoRoute 链前已把 writer 状态预置为 404，
		// 而 http.ServeContent 对 200 全量响应不显式 WriteHeader，会带着
		// 预置 404 发文件体，故先复位为 200；206/304 路径由 ServeContent
		// 显式 WriteHeader 覆盖，不受影响。
		c.Status(http.StatusOK)

		// URL 路径 → 文件系统绝对路径。刻意不先 path.Clean 再 Join：
		// filepath.Join 自身的归一化让 ".." 得以逃逸 root，使下方前缀校验
		// 真正捕获路径穿越（403）；若先 Clean，穿越路径会被无声归一回
		// root 内，只能落到 404。
		absPath := filepath.Join(root, filepath.FromSlash(c.Request.URL.Path))
		if absPath != root && !strings.HasPrefix(absPath, prefix) {
			reject(c, ReasonForbidden, "[403] forbidden")
			return
		}

		// 展示路径：Clean 归一化（结果必以 / 开头且不含 ..），原路径以 /
		// 结尾时保留尾斜杠，标题展示为 Apache 风格的 Index of /xxx/。
		urlPath := path.Clean(c.Request.URL.Path)
		if strings.HasSuffix(c.Request.URL.Path, "/") && !strings.HasSuffix(urlPath, "/") {
			urlPath += "/"
		}

		// Stat-first，勿改回 Open-first：darwin/APFS 实测 open()+close() 是 stat() 的
		// 30–60 倍（StatSerial 1,606–1,642 ns vs OpenCloseSerial 97,748–105,253 ns；
		// StatParallel 1,344–1,410 ns vs OpenCloseParallel 41,983–43,470 ns）。「os.Open →
		// f.Stat() → f.ReadDir(-1)」的 syscall 合并提案已实测评估并回退：多数路径仅 −1
		// alloc，ns/op 收益（−0.4%/−2.9%）全落在本机噪声带内；而目录无尾斜杠的 301 路径在
		// Stat-first 下 stat 成功即重定向、**根本不开句柄**，Open-first 让它白开一次目录句柄
		// 再丢弃 → +2 allocs / +11.2% B/op / +2802% ns/op（1,607 → 46,647）。故回退，保住
		// 这条廉价的重定向路径。os.Stat 与 os.Open 一样跟随符号链接（未设 O_NOFOLLOW），故
		// 悬空链接仍在此处失败于 ENOENT → 404，语义不变（护栏测试钉住）。
		info, err := os.Stat(absPath)
		if err != nil {
			// EACCES/EPERM → 403 + ReasonPermission（父目录缺搜索权限等），对齐
			// Apache/nginx；其余错误 → 404。刻意不把「非 ENOENT 的其它错误」升级为
			// 500：实测 ENOTDIR（URL 形如 /file.txt/sub，路径分量是普通文件）与 ELOOP
			// 在 errors.Is(err, fs.ErrNotExist) 下均为 false，按「非 ENOENT → 500」分流
			// 会把纯客户端的路径错误回归成服务端错误。故 Stat 失败除权限外一律 404；
			// 500 只留给下面 Open/ReadDir 失败于「非 ENOENT、非权限」错误的真正
			// 异常（fd 耗尽、EIO 等；ENOENT 属 Stat→Open/ReadDir 的 TOCTOU 窗口，
			// 见各分支注释）。
			if errors.Is(err, fs.ErrPermission) {
				reject(c, ReasonPermission, "[403] forbidden")
				return
			}
			c.String(http.StatusNotFound, "[404] not found")
			return
		}

		if info.IsDir() {
			// 与 http.FileServer 一致：目录 URL 缺尾斜杠时 301 补上，保证页内相对链接
			// 正确。Location 须经 net/url 对解码后的 URL.Path 重新百分号编码，否则目录名
			// 含 ?/#/%/空格时会被当作 query/fragment 或非法转义；RawQuery 以原始未解码
			// 形式追加，避免丢排序状态。
			if !strings.HasSuffix(c.Request.URL.Path, "/") {
				target := (&url.URL{Path: c.Request.URL.Path + "/"}).String()
				if c.Request.URL.RawQuery != "" {
					target += "?" + c.Request.URL.RawQuery
				}
				c.Redirect(http.StatusMovedPermanently, target)
				return
			}
			renderDirListing(c, urlPath, absPath, ignored, reject)
			return
		}

		// orbit 默认 WriteTimeout=15s（WithHttpWriteTimeout(0) 会被其
		// isConfigValid 重置回 15s 而非「不设超时」），大文件下载会被连接
		// 写截止中途截断。对齐 orbit 自身服务 pprof 长响应的范式：按连接
		// 清除写截止。best-effort——被 timeout 中间件的缓冲 writer 或
		// httptest recorder 包裹时不支持调截止，返回错误属预期，直接忽略。
		_ = http.NewResponseController(c.Writer).SetWriteDeadline(time.Time{})

		f, err := os.Open(absPath)
		if err != nil {
			// ENOENT → 404：Stat 与 Open 之间存在合法的 TOCTOU 窗口——Stat 成功后、
			// Open 前条目被删除，属客户端可见的「路径缺失」而非服务端异常，与上方
			// Stat 失败的缺失语义一致（形态对齐：c.String 404、仅 return 不 Abort）。
			// EACCES/EPERM → 403 + ReasonPermission（文件存在但不可读），对齐
			// Apache/nginx；其余错误 → 500：Stat 已成功、Open 却失败（fd 耗尽、
			// I/O 错误等）属服务端异常。返回通用文本，不泄露内部路径。
			if errors.Is(err, fs.ErrNotExist) {
				c.String(http.StatusNotFound, "[404] not found")
				return
			}
			if errors.Is(err, fs.ErrPermission) {
				reject(c, ReasonPermission, "[403] forbidden")
				return
			}
			c.String(http.StatusInternalServerError, "[500] internal server error")
			return
		}
		// 句柄归属：本 defer 紧随 Open 成功之后注册（绝不对 err 非 nil 的 f 调
		// Close），是 f 的唯一关闭点，覆盖 ServeContent 之后的返回路径。
		defer f.Close()

		// ServeContent 保留 Range/206 断点续传、If-Modified-Since/304、
		// HEAD 语义，并按扩展名/内容嗅探设置 Content-Type。name/modtime 取自
		// 上方 os.Stat 的结果。
		http.ServeContent(c.Writer, c.Request, info.Name(), info.ModTime(), f)
	}
}

// renderDirListing 读取目录内容并渲染 Apache 风格 HTML 列表：
// 目录在前、文件在后（FoldersFirst），组内按排序参数排、次级键恒为名称升序；
// IndexIgnore 命中的条目不渲染。
func renderDirListing(c *gin.Context, urlPath, absPath string, ignored ignoreFunc, reject rejectFunc) {
	// os.ReadDir 一次读尽目录项（并按名称排序）。该排序对正确性并非必需：sortRows
	// 的输出与输入顺序无关（完全定序论证与守护测试见 sortRows 注释）。
	entries, err := os.ReadDir(absPath)
	if err != nil {
		// ENOENT → 404：Stat 与 ReadDir 之间存在合法的 TOCTOU 窗口——Stat 成功后、
		// ReadDir 前目录被删除，属「路径缺失」而非服务端异常，与 Open 分支的缺失
		// 语义一致。ReadDir EACCES（目录不可读/不可进入，如 0o000 目录带尾斜杠）→
		// 403，对齐 Apache/nginx；其余错误 → 500。走统一拒绝契约，RejectHandler
		// 可感知 ReasonPermission。
		if errors.Is(err, fs.ErrNotExist) {
			c.String(http.StatusNotFound, "[404] not found")
			return
		}
		if errors.Is(err, fs.ErrPermission) {
			reject(c, ReasonPermission, "[403] forbidden")
			return
		}
		c.String(http.StatusInternalServerError, "[500] internal server error")
		return
	}

	col, desc := parseSortParams(c.Request.URL.RawQuery)

	// 预扫描 dirent 的类型位（ReadDir 已一并取回，**零 syscall、零 glob 匹配**）以精确
	// 预分配两个分组切片。靠 append 自然增长时，100 条目要经历 13 次扩容、多分配约 10.4 KB
	// （渲染重写后实测占列表路径 alloc_space 的 19.08%，是仅次于不可压缩 lstat 的第二大
	// 分配源），1000 条目多分配约 173 KB；精确预留把两者都压到 1 次分配。命中 IndexIgnore
	// 的目录会让 dirs 略微超额预留，这是有意的取舍：省掉在预扫描里重复执行 glob 匹配
	// （index_ignore 基准度量的正是逐条目逐模式的 path.Match 成本）。cap 为 0 时 make 不
	// 产生堆分配，故纯文件/纯空目录不会多出无用分配。
	nd := 0
	for _, e := range entries {
		if e.IsDir() {
			nd++
		}
	}
	dirs := make([]entryRow, 0, nd)
	files := make([]entryRow, 0, len(entries)-nd)

	// nameBytes 累加条目名字节数，供 buildListingPage 估算输出缓冲容量（见其注释）；
	// 在本次遍历里顺带累加，避免为估算再多走一遍 O(n)。
	nameBytes := 0
	for _, e := range entries {
		name := e.Name()
		if ignored(name) {
			continue // 仅从列表隐藏，直接 URL 访问不受影响
		}
		info, err := e.Info()
		if err != nil {
			// 读取元数据失败的条目跳过 ⇒ 用户可见后果：该条目从列表中静默缺席，
			// 无日志无指标（本包无日志依赖，与仓库其余中间件一致）；不因此让整页
			// 失败——单条目元数据异常时整页 500 的代价更大。
			continue
		}
		nameBytes += len(name)
		row := entryRow{
			sortName:  name,
			sortSize:  info.Size(),
			sortMtime: info.ModTime(),
			isDir:     e.IsDir(),
			// e.Info() 为 lstat 语义：符号链接的 Size() 是目标路径字符串的字节长度
			// 而非内容大小（如 "76"），直接展示属事实性错误，与目录一致显示 "-"。
			// 不改分组逻辑（symlink 仍归 files 组、不带尾斜杠），也不引入 realpath
			// 解析、不改 os.Stat 的跟随语义（既有有意不修的安全边界）。
			noSize: e.IsDir() || info.Mode()&fs.ModeSymlink != 0,
		}
		if row.isDir {
			dirs = append(dirs, row)
		} else {
			files = append(files, row)
		}
	}
	sortRows(dirs, col, desc)
	sortRows(files, col, desc)

	// 表头自链接由固定字母表拼出（"?C=" + N|M|S + ";O=" + A|D），不含任何用户输入，
	// 故无需转义即可直接写入 href——与原 html/template 的 urlProcessor 输出逐字节一致。
	// ';' 分隔形态是双向契约：parseSortParams 依赖把 ";" 归一为 "&" 来解析它；护栏
	// 测试钉住其必须用 ';' 分隔且不含 '&'。
	page := buildListingPage(listingPage{
		urlPath:   urlPath, // 目录路径必以 / 结尾，标题展示为 Index of /xxx/
		nameLink:  sortLink('N', col, desc),
		mtimeLink: sortLink('M', col, desc),
		sizeLink:  sortLink('S', col, desc),
		// 相对链接 "../"，NoRoute 根挂载与前缀挂载皆可移植；根目录无处可回退故省略该行
		parent: urlPath != "/",
		dirs:   dirs,
		files:  files,
		// 容量估算的名字字节数（见 buildListingPage 注释）
		nameBytes: nameBytes,
	})

	c.Header("Content-Type", "text/html; charset=utf-8")
	// 列表页由 buildListingPage 一次性在内存中拼完（page 即完整缓冲），故字节数此刻已
	// 精确可知，得以声明定长 Content-Length，让响应走定长分帧而非 Transfer-Encoding:
	// chunked。必须声明的原因：net/http 只在 body ≤2048 B（http.serverChunkThreshold）时
	// 才自动回填 Content-Length，而列表页恒大于该阈值（实测 100 条目 12,497 B、1000 条目
	// 116,585 B），不声明即恒以 chunked 发出——多出分帧开销、客户端无法预知大小、HEAD 打
	// 目录拿不到准确 CL，且与 Apache mod_autoindex / nginx autoindex 行为不一致。
	// 必须在首次 Write 之前设置：net/http 在 WriteHeader 时固化响应头，之后设置不上线路。
	// 与 compression 叠加安全：其快速决策路径按本头判定，压缩前先 Del("Content-Length")、
	// 不压缩则原样保留（准确的）本头，两条路径都不会出现「声明未压缩长度」的头-body 撕裂。
	c.Header("Content-Length", strconv.Itoa(len(page)))
	// 刻意用 Write([]byte) 而非 WriteString(string)：gin 的 responseWriter.WriteString
	// 在被包裹的 writer 未实现 io.StringWriter 时会退化成 w.Write([]byte(s))，即**整页
	// 再拷一份**（bench 的 benchWriter、部分中间件的包装 writer 都没有该方法）。
	// Write(page) 在两类 writer 下都是零额外拷贝，且 page 本身就是逐段 append 出来的
	// 缓冲区，不存在 string↔[]byte 转换成本。响应头与部分 body 一经写出便无法再改状态
	// 码；本包无日志依赖，写失败仅忽略（与原模板 Execute 的错误处理一致）。
	_, _ = c.Writer.Write(page)
}

// listingPage 是 buildListingPage 的入参集合（避免 8 个位置参数造成的调用点不可读）。
type listingPage struct {
	urlPath   string     // 展示的目录路径（必以 / 结尾）
	nameLink  string     // Name 列头排序自链接
	mtimeLink string     // Last modified 列头排序自链接
	sizeLink  string     // Size 列头排序自链接
	parent    bool       // 是否渲染 Parent Directory 行（根目录为 false）
	dirs      []entryRow // 已排序的目录组（FoldersFirst：整体在 files 之前）
	files     []entryRow // 已排序的文件组
	nameBytes int        // 全部条目名的字节数之和，用于容量估算
}

// buildListingPage 渲染整页目录列表并返回新建的页面缓冲。**一次预分配是达成 allocs/B
// 目标的关键**：页面在 100 条目时约 12.5 KB、1000 条目时约 117 KB，靠 append 自然增长
// 会经历多次倍增扩容，每次都是「新分配 + 整页拷贝」。容量按「骨架 + 路径 + 三个表头链接
// + 每行固定标记 + 名字字节 ×4」估算；实测校准（100 条目 = 90 文件 + 10 目录，ASCII 名）：
// 页面 12,497 B，本式给出 15,549 B，落 size class 16,384 B，不触发扩容。名字因子推导与
// CJK 实测见 make 处注释。
func buildListingPage(p listingPage) []byte {
	n := len(p.dirs) + len(p.files)
	// 6*len(urlPath)：文本上下文膨胀上界——真实最坏 5×（最长字符引用 &#34;/&#39;/&#43;
	// 均 5 字节，不存在 6 字节字符引用），取 6 为宽松上界；+2 是目录条目在 href 与显示名
	// 上各多出的 '/'。4*nameBytes：名字膨胀 = href 侧最坏 3×（每字节 → %XX）+ 显示名侧
	// 1×~5×（文本转义）。CJK 名 = 3×+1×（多字节按字节透传）= 4×；纯 ASCII unreserved 名
	// = 2×；全需转义的 ASCII 名最坏 = 8×。取 4× 覆盖全 CJK 名（本项目语境的常态）：实测
	// 1000 条目 CJK 夹具 actual 174,785 / est 198,789（+13.7%），不扩容；旧值 2× 对同一
	// 夹具低估 −14.9%，触发整页 realloc+memmove（每请求 TotalAlloc 实测多 ~154 KB）。
	// 代价：ASCII 页每请求多预留 ~2.8 KB（bench 实测 B/op +3.2%~3.9%、allocs/op 不变、
	// body-B/op 逐位不变）。全转义名在 4× 下仍有余量缺口，由 append 优雅扩容兜底（多一次
	// realloc+memmove，不影响正确性——预估是优化而非契约）。
	dst := make([]byte, 0, listingFixedLen+6*len(p.urlPath)+
		len(p.nameLink)+len(p.mtimeLink)+len(p.sizeLink)+
		n*(listingRowFixedLen+2)+4*p.nameBytes)

	dst = append(dst, listingPreTitle...)
	dst = appendTextEscape(dst, p.urlPath)
	dst = append(dst, listingMidStyle...)
	dst = appendTextEscape(dst, p.urlPath)
	dst = append(dst, listingPostH1...)
	dst = append(dst, p.nameLink...)
	dst = append(dst, listingNameHdr...)
	dst = append(dst, p.mtimeLink...)
	dst = append(dst, listingMtimeHdr...)
	dst = append(dst, p.sizeLink...)
	dst = append(dst, listingSizeHdr...)
	if p.parent {
		dst = append(dst, listingParentRow...)
	}
	dst = appendRows(dst, p.dirs)
	dst = appendRows(dst, p.files)
	return append(dst, listingTail...)
}

// appendRows 逐行追加数据行。取 &rows[i] 而非 range 值拷贝，避免每行复制 56 字节结构体。
func appendRows(dst []byte, rows []entryRow) []byte {
	for i := range rows {
		r := &rows[i]
		dst = append(dst, listingRowOpen...)
		// href：严格百分号编码条目名；目录分隔符在编码**之外**追加（段内/段间 '/'
		// 的编码取舍论证见 appendPathSegmentEscape 尾注）。
		dst = appendPathSegmentEscape(dst, r.sortName)
		if r.isDir {
			dst = append(dst, '/')
		}
		dst = append(dst, listingRowMid...)
		// 显示名：文本上下文转义。'/' 不在转义集内，故「先转义再补 /」与原模板
		// 对 name+"/" 整体转义的结果逐字节一致。
		dst = appendTextEscape(dst, r.sortName)
		if r.isDir {
			dst = append(dst, '/')
		}
		dst = append(dst, listingRowCellC...)
		dst = appendMtime(dst, r.sortMtime)
		dst = append(dst, listingRowCellS...)
		if r.noSize {
			dst = append(dst, '-')
		} else {
			dst = appendHumanSize(dst, r.sortSize)
		}
		dst = append(dst, listingRowClose...)
	}
	return dst
}

// appendTextEscape 以 HTML **文本上下文**转义追加 s，零分配。转义集与 html/template 的
// htmlReplacementTable（html.go:55-69）逐字节一致——它只处理 7 个字节：NUL→U+FFFD 与
// " & ' + < >。与 html.EscapeString（只转义 " & ' < > 五个字节）存在**两处分歧**：
// `+` → `&#43;` 与 NUL → U+FFFD，二者在 EscapeString 中均原样透传。照搬「文本上下文 →
// html.EscapeString」这类优化建议会同时破坏这两项：`+` 让 `a+b.txt`、`c++ notes.txt`
// 这类常见名字产生字节级回归（护栏测试已把 `a&#43;b.txt` 与 `/a&#43;b/` 内联固化为
// golden），NUL 见下方 documented gap 论述。故**严禁改用 html.EscapeString**。
//
// 同样**有意不转义**的字节（重写时不得"顺手加严"，否则特征化护栏会 RED）：
//   - '=' 与反引号：htmlReplacementTable 无此二项（反引号只出现在用于无引号属性的
//     htmlNospaceReplacementTable 中）；本页面所有属性都带双引号，故无需转义
//   - C0 控制字符（\x01 \n \t \x7f 等）：非 HTML 语法字符，原样透传（护栏测试钉住）
//   - 多字节 UTF-8：>=0x80 的字节不会命中任何 case，故按字节透传即整体透传，不存在把
//     多字节序列截断成非法 UTF-8 的风险
//
// NUL → U+FFFD：NUL 实际上无法经 HTTP + 文件系统抵达渲染层（open(2) 以 EINVAL 拒绝，
// net/http 亦拒绝含 NUL 的 URL 路径），护栏测试据此把它记为 documented gap，但明确要求
// 手写转义器「保持同等或更严格的行为，不得原样透传 NUL」，故此处照做。
func appendTextEscape(dst []byte, s string) []byte {
	start := 0
	for i := 0; i < len(s); i++ {
		var esc string
		switch s[i] {
		case '"':
			esc = "&#34;"
		case '&':
			esc = "&amp;"
		case '\'':
			esc = "&#39;"
		case '+':
			esc = "&#43;"
		case '<':
			esc = "&lt;"
		case '>':
			esc = "&gt;"
		case 0:
			esc = "\uFFFD"
		default:
			continue
		}
		dst = append(dst, s[start:i]...)
		dst = append(dst, esc...)
		start = i + 1
	}
	return append(dst, s[start:]...)
}

// upperHex 是百分号编码用的大写十六进制表。大写形态与 net/url 的 escape 一致
// （实测 %23/%20/%25/%3F/%09/%7F/%5C/%60/%E6%B5%8B 均为大写），从而使既有
// TestAutoIndex_HrefEncoding 的 %23/%20/%25/%3F 断言与护栏 round-trip 测试逐字节不变。
const upperHex = "0123456789ABCDEF"

// appendPathSegmentEscape 以**严格百分号编码**把单个路径段追加到 dst：仅保留 RFC 3986
// §2.3 的 unreserved 字符 A-Za-z0-9-_.~，其余每个字节一律编码为大写 %XX。零分配。
//
// ── 安全论证（把隐式依赖改为显式设计）──
//
// 渲染重写前 href 由 (&url.URL{Path: name}).String() 产生，阻断 `javascript:` 这类
// scheme 注入的**实际防线**是 net/url 依 RFC 3986 §4.2 给「首个路径段含 ':'」的相对引用
// 前置的 "./"——一个副产品，代码里没有任何注释声明它，一旦换掉 url.URL 构造就会静默消失。
// 本函数把该防线改成显式且更强的形式：':' 是保留字符、不在 unreserved 集合内，故**必被
// 编码为 %3A**。而 RFC 3986 §3.1 规定 scheme 的分隔符是**字面** ':'，任何 URL 解析器
// （WHATWG / net/url）都不会把 %3A 当作 scheme 分隔符——因此严格编码后的 href 在语法上
// **不可能构成任何 scheme**，相对引用是它唯一的解析结果，不再依赖 "./" 前缀这一副产品。
// 由此同时得到四条**机制无关**的安全性质（护栏的 property-based 断言逐条把关）：
//   - 判据 A：'"' '<' '>' CR LF 全部被编码 → 无法逃逸出 href="…" 属性、无法构成标签；
//     '&' 被编码为 %26 → 属性内不存在任何裸 '&'，也就没有字符引用歧义
//   - 判据 B：WHATWG 归一化（删 tab/LF/CR、裁剪首尾 C0-与空格）后仍是纯 %XX 序列，不会
//     以 javascript:/data:/vbscript:/file:/about:/blob: 开头；tab 内嵌形态
//     "javascript\t:x" 编码为 "javascript%09%3Ax"，删 tab 后仍为 "javascript%09%3Ax"
//     （%09 是字面三字符，不是 tab）→ 不含字面 ':'，绕过失效
//   - 判据 C：url.Parse 得到的 Scheme 恒为 ""
//   - 判据 D：'/' 被编码 → 单个文件名无法伪造多级路径，也无法构成 "//host" 协议相对 URL
//     或以 '\' 开头；'#' 被编码 → 不产生页内锚点死链；'%' 被编码为 %25 → 解码一次恰好
//     还原原名，不存在双重编码（护栏 round-trip 测试钉住）
//
// 目录的尾部分隔符由调用方在本函数**之外**追加未转义的 '/'（见 appendRows）：段内 '/' 必
// 须编码（否则文件名里的 '/' 会伪造层级），段间 '/' 必须不编码（否则浏览器把它当普通字符，
// 链接会指向一个名字里带 %2F 的不存在条目）。
func appendPathSegmentEscape(dst []byte, seg string) []byte {
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			dst = append(dst, c)
			continue
		}
		dst = append(dst, '%', upperHex[c>>4], upperHex[c&0x0f])
	}
	return dst
}

// appendMtime 以定长 "2006-01-02 15:04" 追加修改时间，零分配。替代 time.Time.Format：
// 后者每条目分配 1 次结果字符串（100 条目 = 100 allocs/op，占列表路径 allocs 的 2.15%、
// alloc_space 的 0.86%），而其 CPU 占比只有 0.05%——纯分配开销，手写定长输出即可归零。
//
// 各字段取值域由 time.Time 保证（月 1-12、日 1-31、时 0-23、分 0-59），故两位十进制可
// 直接展开；年份在 [0,9999] 内同样直接展开，与 Format 的 stdLongYear 逐字节一致（含前导
// 零；TestRender_MtimeYearExhaustive 穷举 [0,9999] 全部年份钉住该契约）。年份各分项必须
// **先取模再转 byte**：y/10 最大 999，byte(y/10)%10 的写法会先做 mod-256 截断丢掉百位
// （历史缺陷：y ≥ 2560 时十位错误，[0,9999] 内 7440 个年份失配，如 2560→"2500"）；其余
// 分项 y/1000≤9、y/100≤99、y%10≤9 均 <256，安全。区间外退回 strconv.AppendInt，其与
// Format("2006-01-02 15:04") 的实测关系（TestRender_MtimeYearOutOfRange 钉住）：>4 位
// 正年逐字节一致（y=10000/12345/99999/100000/275760 全 MATCH，Format 对 >4 位年份
// **不会**产出错误数字），|y| ≥ 1000 的负年亦一致（-1000/-9999/-12345/-100000 MATCH）。
// 唯一分歧域是位数不足 4 的负年（0 > y > -1000）：Format 的 appendInt(x,4) 在负号后
// 补零至宽 4（y=-5 → "-0005"），本实现不补零（→ "-5"）。文件系统 mtime 早于公元 1 年
// 在生产不可达，且该分歧仅影响显示、不影响排序（排序用原始 sortMtime），故有意不对齐
// Format（改动会引入新的未测路径），保持现行为 + 特征化断言即可。
func appendMtime(dst []byte, t time.Time) []byte {
	if y := t.Year(); y >= 0 && y <= 9999 {
		dst = append(dst, byte(y/1000)+'0', byte((y/100)%10)+'0', byte((y/10)%10)+'0', byte(y%10)+'0')
	} else {
		dst = strconv.AppendInt(dst, int64(y), 10)
	}
	dst = append(dst, '-')
	dst = append2Digits(dst, int(t.Month()))
	dst = append(dst, '-')
	dst = append2Digits(dst, t.Day())
	dst = append(dst, ' ')
	dst = append2Digits(dst, t.Hour())
	dst = append(dst, ':')
	return append2Digits(dst, t.Minute())
}

// append2Digits 追加 [0,99] 区间整数的两位十进制（不足两位前补零）。取值域由调用方
// 保证（月/日/时/分）：v ≤ 99 时 v/10 与 v%10 均 ≤ 9，byte 转换无截断风险。
func append2Digits(dst []byte, v int) []byte {
	return append(dst, byte(v/10)+'0', byte(v%10)+'0')
}

// humanSizeChars 是 K..P 的单位后缀查表（循环耗尽后的兜底档为 'E'）。用 const
// 字符串而非包级 var 数组——与 upperHex 形式统一、语言层面不可变（var 数组可被
// 包内任意代码改写），同时消除原实现里 []string{"K",…} 字面量每次调用重建切片的分配。
const humanSizeChars = "KMGTP"

// appendHumanSize 把字节数格式化为 Apache 风格的 K/M/G 人类可读形式（一位小数，
// 末位 .0 省略）并追加到 dst，零分配。
func appendHumanSize(dst []byte, n int64) []byte {
	const unit = 1024
	if n < unit {
		return strconv.AppendInt(dst, n, 10)
	}
	val := float64(n)
	suffix := byte('E')
	for i := 0; i < len(humanSizeChars); i++ {
		val /= unit
		// 阈值取 unit-0.05 而非 unit：'f',1 在 val∈[1023.95,1024) 时会四舍五入进位为
		// "1024.0"，此时应进到下一档；用 val<unit 会误留本档而显示 "1024K"/"1024M"。
		// 0.05 是末位小数(0.1)的半个进位区间。
		if val < unit-0.05 {
			suffix = humanSizeChars[i]
			break
		}
	}
	// AppendFloat 直接写入 dst，替代原 FormatFloat + TrimSuffix + 字符串拼接的 3 次分配。
	dst = strconv.AppendFloat(dst, val, 'f', 1, 64)
	// 'f',1 的输出恒带 1 位小数，故「末位为 .0」等价于「最后两字节是 ".0"」。
	// n>=unit 时 val>=1，不会出现 "0.0" 被误截成空串的情形。
	if len(dst) >= 2 && dst[len(dst)-2] == '.' && dst[len(dst)-1] == '0' {
		dst = dst[:len(dst)-2]
	}
	return append(dst, suffix)
}

// parseSortParams 解析 Apache mod_autoindex 风格排序查询参数：
// C=N|M|S（名称/修改时间/大小，非法或缺省回退 N）、O=A|D（缺省 A）。
// Apache 列头链接以分号分隔（?C=S;O=D），而 Go 标准库 url.ParseQuery
// 自 1.17 起不再接受分号作分隔符，故先把 ";" 归一为 "&" 再解析。
func parseSortParams(rawQuery string) (col byte, desc bool) {
	// 错误刻意忽略：ParseQuery 出错仍返回已解析部分，非法/缺失参数自然回退默认排序 N/升序。
	q, _ := url.ParseQuery(strings.ReplaceAll(rawQuery, ";", "&"))
	col = 'N'
	switch q.Get("C") {
	case "M":
		col = 'M'
	case "S":
		col = 'S'
	}
	desc = q.Get("O") == "D"
	return col, desc
}

// sortLink 生成列头排序自链接：点击当前列且当前为升序时切换为降序（O=D），
// 否则回到升序（O=A）——与 Apache 列头「重复点击切换升降序」行为一致。
func sortLink(col, curCol byte, desc bool) string {
	order := "A"
	if col == curCol && !desc {
		order = "D"
	}
	return "?C=" + string(col) + ";O=" + order
}

// sortRows 按主键（col 列）与升降序排序；主键相同时次级键恒为名称升序（Apache 语义，
// 与主排序方向无关，且位于 desc 判断之外）。用 slices.SortFunc 而非 sort.Slice：后者经
// reflectlite.Swapper 反射式交换，同机 1000 行夹具实测 154.7k ns/op、3 allocs/op →
// 140.7k ns/op、0 allocs/op。desc 取反主键结果安全：comparePrimary 三分支
// （time.Compare / cmp.Compare / strings.Compare）均只返回 −1/0/+1。两个排序器均非
// 稳定，但同组内 base name 唯一 + 次级键恒名称升序 ⇒ 完全定序 ⇒ 结果唯一、与输入顺序
// 无关（护栏以 13 种合成置换 × 6 组合与主键全同夹具钉住，「删除次级键」变异已验证判别力）。
func sortRows(rows []entryRow, col byte, desc bool) {
	slices.SortFunc(rows, func(a, b entryRow) int {
		if p := comparePrimary(a, b, col); p != 0 {
			if desc {
				return -p
			}
			return p
		}
		// 排序语义不变式：次级键恒名称升序且位于 desc 判断之外，故输出与输入顺序无关。
		return strings.Compare(a.sortName, b.sortName)
	})
}

// comparePrimary 比较主排序键，返回负/零/正表示 a 在前/相等/在后。
func comparePrimary(a, b entryRow, col byte) int {
	switch col {
	case 'M':
		return a.sortMtime.Compare(b.sortMtime)
	case 'S':
		// 大小按原始字节比较（而非 appendHumanSize 的显示值）
		return cmp.Compare(a.sortSize, b.sortSize)
	default: // 'N'
		return strings.Compare(a.sortName, b.sortName)
	}
}
