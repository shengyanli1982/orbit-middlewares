package autoindex

// 本文件是 autoindex 渲染重写轮两项优化的**前置安全与行为护栏**，由独立于实现者的角色
// 先行落地：
//
//	渲染重写 —— 用 strings.Builder 手写渲染替换 html/template（**已落地**；当时预期
//	     allocs/op 4210 → 700~900）。正文中的历史简写记号是 "P1"。
//	syscall 合并提案 —— os.Stat+os.ReadDir → os.Open+f.Stat()+f.ReadDir(-1)，
//	     **已实测评估并回退，勿重做**（历史简写记号 "P2"）：darwin/APFS 上
//	     open()+close() 是 stat() 的 30–60 倍，多数路径仅 −1 alloc，而目录无尾斜杠的
//	     301 路径反而 +2 allocs / +11.2% B/op / +2802% ns/op（依据见 autoindex.go 的
//	     Stat-first 注释）。
//
// ⚠️ 本文件正文出现的 "P1/P2" 一律指上述**优化轮次记号**（沿用当时工作区文档的历史
// 编号），与仓库通用的 P0/P1/P2 缺陷严重度词表无关，勿按严重度解读。
//
// 三条设计原则，违反任一条护栏即失去意义：
//
//  1. **golden 内联**：全部 byte-exact 期望值都是本文件内的字面量，取自 darwin/arm64 +
//     Go 1.26.8 的实测输出，**不由 html/template 现场渲染产生**。渲染重写已删除
//     dirListingTmpl；若期望值来自模板，测试将随实现一起失效（自证循环）。
//  2. **用 href 反查定位数据行，不用显示名**：显示名正是 G1 的被测对象；用被测值去定位
//     被测值时，转义一旦整体缺失，索引键与期望值会同步漂移，测试反而恒通过——这是
//     「弱测试假通过」的典型成因。
//  3. **性质断言与特征化断言分离**：G2/G3 是机制无关的安全性质（旧 net/url `./` 前缀
//     方案与已落地的「严格百分号编码」方案都必须满足）；G1/G4/G7 是特征化快照（重写后必须
//     逐字节保持，或按注释指示做**有意识的**改写）。
//
// 覆盖矩阵源自 HTML 输出安全实证与重写前置条件清单；载体工作区文档不随仓库分发，
// 但矩阵本身已在本文件内全量展开，可独立自证。

import (
	"fmt"
	"html"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// 共享常量：棘手文件名矩阵
// ---------------------------------------------------------------------------

// guardLongName 是 255 字节的文件名（APFS/ext4/NTFS 的单段上限），用于钉住超长名不被截断。
var guardLongName = strings.Repeat("L", 251) + ".txt"

// guardNameCase 是一个「棘手文件名」及其在**当前手写渲染（appendTextEscape）下**的逐字节 golden。
//
// wantText 的取值依据是 Go 1.26.8 html/template/html.go:55-69 的 htmlReplacementTable，
// 它只转义 7 个字节：NUL→U+FFFD、" & ' + < >。
//
// ⚠️ 这张表比 html.EscapeString **多转义一个 `+`（→ &#43;）**，而当时的渲染重写
// 优化建议曾提出「文本上下文 → html.EscapeString」。二者对含 `+` 的文件名（`c++ notes.txt`、
// `a+b.txt` 这类在真实静态树中极常见的名字）会产生**字节级差异**。wantText 刻意固化
// 原 html/template 的实际行为（`a&#43;b.txt`，P1 落地的 appendTextEscape 逐字节保持），
// 使**任何直接改用 html.EscapeString 的重写立刻 RED**。
// 注意 `=` 与反引号两者都**不**转义（已实测），故它们不是差异点。
type guardNameCase struct {
	name     string // 文件系统上的真实文件名字节
	wantText string // G1：<a …> 与 </a> 之间的文本上下文 golden（HTML 字符引用未解码）
	colon    bool   // 名字含 ':' → 归 TestRender_HrefSchemeLike_CurrentBehavior 专项处理
}

// guardNameMatrix 是可在 POSIX 文件系统上创建的棘手文件名全集（34 项）。
//
// 平台约束（实测）：
//   - darwin/APFS **大小写不敏感**：矩阵内不得出现仅大小写不同的两项，否则后写者覆盖先写者。
//     故 scheme 变体取 alert(1)/alert(2)/alert(4) 三种不同后缀而非仅换大小写。
//   - Windows 文件名禁用 ? * : " < > | \：这些项在 Windows 上会 Skip（见 guardCreateMatrix）。
var guardNameMatrix = []guardNameCase{
	// --- HTML 语法字符：标签注入面 ---
	// 闭合标签形态（</script>、</title>）含 '/'，无法作为单个文件名存在（'/' 是 POSIX
	// 路径分隔符），改由「路径上下文」用例 TestRender_PathTextEscaping 覆盖。
	{name: "<script>alert(1)", wantText: "&lt;script&gt;alert(1)"},
	{name: "<img src=x onerror=alert(1)>", wantText: "&lt;img src=x onerror=alert(1)&gt;"},

	// --- HTML 实体与引号：属性逃逸 / 实体歧义面 ---
	{name: "a&b.txt", wantText: "a&amp;b.txt"},
	{name: `q"uote.txt`, wantText: "q&#34;uote.txt"},
	{name: "q'uote.txt", wantText: "q&#39;uote.txt"},
	// 反引号在文本上下文**不**转义：htmlReplacementTable 无该项（它只出现在用于
	// 无引号属性的 htmlNospaceReplacementTable 中）。钉住以免重写时"顺手加严"造成 diff。
	{name: "back`tick.txt", wantText: "back`tick.txt"},

	// --- `+` 与 `=`：html/template 与 html.EscapeString 的唯一分歧点 ---
	{name: "a+b.txt", wantText: "a&#43;b.txt"},
	{name: "a=b.txt", wantText: "a=b.txt"},

	// --- 控制字符：href 必须百分号编码，文本上下文原样透传（C0 非 HTML 语法字符）---
	{name: "ctrl\x01char.txt", wantText: "ctrl\x01char.txt"},
	{name: "nl\nchar.txt", wantText: "nl\nchar.txt"},
	{name: "tab\tchar.txt", wantText: "tab\tchar.txt"},
	{name: "del\x7fchar.txt", wantText: "del\x7fchar.txt"},

	// --- 非 ASCII：多字节 UTF-8 必须整体透传，不得按字节截断成非法序列 ---
	{name: "测试.txt", wantText: "测试.txt"},
	{name: "🎉emoji.txt", wantText: "🎉emoji.txt"},
	// U+202E RIGHT-TO-LEFT OVERRIDE：视觉欺骗面（可把 "exe.txt" 显示成 "txt.exe"），
	// 但非 HTML 注入面。钉住原样透传，安全加固属挂载层职责（勿在包内过滤）。
	{name: "rtl\u202ename.txt", wantText: "rtl\u202ename.txt"},

	// --- 空白：首尾空格影响 href 解析与 URL 裁剪语义 ---
	{name: "a b.txt", wantText: "a b.txt"},
	{name: " lead.txt", wantText: " lead.txt"},
	{name: "trail.txt ", wantText: "trail.txt "},

	// --- 百分号：双重编码面（href 必须是 %2525 而非 %25，解码一次即还原）---
	{name: "100%.txt", wantText: "100%.txt"},
	{name: "%2e%2e.txt", wantText: "%2e%2e.txt"},

	// --- URL 保留字符：; 与 = 在路径段中合法，net/url 不编码 ---
	{name: "a;b.txt", wantText: "a;b.txt"},

	// --- 前导 '-'：命令行参数注入面（若名字被传给外部工具），渲染层须原样呈现 ---
	{name: "-dash.txt", wantText: "-dash.txt"},

	// --- 长度上限 ---
	{name: guardLongName, wantText: guardLongName},

	// --- URL 分隔符：href 百分号编码修复的原始触发面（? 会被当 query、# 会被当 fragment）---
	{name: "#h.txt", wantText: "#h.txt"},
	{name: "?q.txt", wantText: "?q.txt"},

	// --- 反斜杠：darwin 合法；Windows 是路径分隔符（该项在 Windows 上 Skip）---
	{name: `back\slash.txt`, wantText: `back\slash.txt`},

	// --- 含 ':' 的 scheme-like 名字：G2 安全性 + G3 专项现状钉住 ---
	{name: "a:b.txt", wantText: "a:b.txt", colon: true},
	{name: "javascript:alert(1)", wantText: "javascript:alert(1)", colon: true},
	{name: "JAVASCRIPT:alert(2)", wantText: "JAVASCRIPT:alert(2)", colon: true},
	{name: "JaVaScRiPt:alert(4)", wantText: "JaVaScRiPt:alert(4)", colon: true},
	// data: 的无斜杠形态（`data:text/html,x` 含 '/' 不可作为文件名，见 unreachable 表）
	{name: "data:text,x", wantText: "data:text,x", colon: true},
	{name: "vbscript:msgbox(1)", wantText: "vbscript:msgbox(1)", colon: true},
	// 前导空格 + scheme：浏览器会先裁剪首尾 C0-control-or-space 再解析 scheme
	{name: " javascript:x", wantText: " javascript:x", colon: true},
	// tab 内嵌 scheme：WHATWG URL 解析器会**删除全部 tab/LF/CR** 再解析 scheme，
	// 故 `java\tscript:` 在浏览器中等价于 `javascript:`。这是纯「百分号解码后比前缀」
	// 判据抓不到、必须做浏览器语义归一化才能抓到的绕过形态。
	{name: "javascript\t:x", wantText: "javascript\t:x", colon: true},
}

// guardUnreachableCase 是**结构上不可能**作为单个文件名存在的输入。
//
// 这些用例不得静默省略：它们在 -v 输出中以 SKIP 形式显式出现并附带原因，
// 使"未覆盖"与"已覆盖且通过"在测试报告里可区分。
var guardUnreachableCases = []struct {
	name   string
	reason string
}{
	{
		name: "<script>alert(1)</script>",
		reason: "含 '/'（POSIX 路径分隔符），任何 POSIX/NTFS 文件系统都不允许其作为单个文件名；" +
			"其 `</script>` 闭合标签的转义面由 TestRender_PathTextEscaping 的 " +
			"`x</title><script>y</script>` 嵌套目录路径用例覆盖",
	},
	{
		name:   "</title><h1>x</h1>",
		reason: "同上（含 '/'）；`</title>` 破标题面的覆盖同上",
	},
	{
		name:   "data:text/html,x",
		reason: "同上（含 '/'）；data: scheme 面由矩阵中的无斜杠形态 `data:text,x` 覆盖",
	},
	{
		name: "null\x00byte.txt",
		reason: "NUL 被 open(2) 以 EINVAL 拒绝（实测 darwin/arm64），且 Go net/http 亦拒绝 " +
			"URL 路径含 NUL，故该字节无法经 HTTP+文件系统任一路径抵达渲染层；" +
			"**对下一轮 P1 的要求**：html/template 把 NUL 映射为 U+FFFD" +
			"（htmlReplacementTable[0]），手写转义器须保持同等或更严格的行为，不得原样透传 NUL",
	},
}

// ---------------------------------------------------------------------------
// 共享工具：渲染行的精确解析
// ---------------------------------------------------------------------------

// guardRow 是从渲染 HTML 中按 <tr><td>…</tr> 切出的单个数据行。
//
// 全部字段保存**原始字节**（HTML 字符引用未解码），因为 G1 要断言的正是转义结果本身；
// 提前解码会让「转义缺失」与「转义正确」变得不可区分。
type guardRow struct {
	raw   string // 整行原始字节（含 <tr><td> 与 </tr>），用于结构性断言
	href  string // href="…" 属性内的原始字节
	text  string // <a …> 与 </a> 之间的原始字节
	mtime string // <td class="c"> 单元格内容
	size  string // <td class="s"> 单元格内容
}

// parseGuardRows 按 <tr><td>…</tr> 标记切分数据行。
//
// 刻意**不按 '\n' 切行**：文件名可含裸换行（矩阵中的 "nl\nchar.txt"），且文本上下文
// 会把它原样透传，按行切分会把一行截成两段从而静默漏掉断言对象。
// 表头行以 <tr><th> 开头，不会被本函数匹配；Parent 行同为 <tr><td> 开头、**会**被
// 匹配——实际由 guardMatrixBody 的 NotContains("Parent Directory") 前置校验兜住
// （矩阵夹具取根列表，根目录本就无 Parent 行）。
func parseGuardRows(t *testing.T, body string) []guardRow {
	t.Helper()
	var out []guardRow
	rest := body
	for {
		i := strings.Index(rest, "<tr><td>")
		if i < 0 {
			return out
		}
		j := strings.Index(rest[i:], "</tr>")
		require.GreaterOrEqualf(t, j, 0, "每个 <tr><td> 必须有配对的 </tr>，残缺行意味着渲染被截断：%q", rest[i:])
		seg := rest[i : i+j+len("</tr>")]
		out = append(out, parseOneGuardRow(t, seg))
		rest = rest[i+j+len("</tr>"):]
	}
}

func parseOneGuardRow(t *testing.T, seg string) guardRow {
	t.Helper()
	const open = `<a href="`
	a := strings.Index(seg, open)
	require.GreaterOrEqualf(t, a, 0, "数据行必须含 <a href=…>：%q", seg)
	start := a + len(open)

	// 属性终止符取字面 `">` 两字节序列：正确实现下 href 内不含裸 `"`，故首个 `">`
	// 必是真终止符。若实现把裸 `"` 写进属性（G8 变异 1），裸引号后面跟的是文件名字节
	// 而非 `>`，解析仍会落在真终止符上——随后由判据 A 的「属性完整性」断言判 FAIL，
	// 不会因解析错位而误判通过。
	e := strings.Index(seg[start:], `">`)
	require.GreaterOrEqualf(t, e, 0, `href 属性必须以 "> 收尾（否则说明属性被裸引号破坏）：%q`, seg)
	href := seg[start : start+e]

	textStart := start + e + len(`">`)
	textEnd := strings.Index(seg[textStart:], "</a>")
	require.GreaterOrEqualf(t, textEnd, 0, "显示名必须以 </a> 收尾：%q", seg)
	text := seg[textStart : textStart+textEnd]

	return guardRow{
		raw:   seg,
		href:  href,
		text:  text,
		mtime: guardCell(t, seg, `<td class="c">`),
		size:  guardCell(t, seg, `<td class="s">`),
	}
}

func guardCell(t *testing.T, seg, marker string) string {
	t.Helper()
	i := strings.Index(seg, marker)
	require.GreaterOrEqualf(t, i, 0, "行内应有 %s 单元格：%q", marker, seg)
	start := i + len(marker)
	end := strings.Index(seg[start:], "</td>")
	require.GreaterOrEqualf(t, end, 0, "%s 单元格应闭合：%q", marker, seg)
	return seg[start : start+end]
}

// indexGuardRows 以「href 解码并去掉 ./ 前缀与目录尾斜杠后的名字」为键索引数据行。
//
// 为什么用 href 反查而非显示名反查：显示名是 G1 的被测对象，用它做索引会形成自证循环
// （见文件头原则 2）。href 侧另有 G2/G3 独立把关；href 一旦损坏到无法解码，本函数直接
// require 失败，不会静默产出错误键让 G1 假通过。
//
// 前置条件：body 必须是**根目录**列表（无 Parent 行），否则 "../" 会被索引成键 ".."。
func indexGuardRows(t *testing.T, rows []guardRow) map[string]guardRow {
	t.Helper()
	out := make(map[string]guardRow, len(rows))
	for _, r := range rows {
		key := guardRowKey(t, r)
		if prev, dup := out[key]; dup {
			require.Failf(t, "href 反查出的条目名重复",
				"键 %q 同时来自 href %q 与 %q——说明 href 编码丢失了区分度", key, prev.href, r.href)
		}
		out[key] = r
	}
	return out
}

// guardMatrixBody 建立矩阵目录树并取回根列表，供 G1/G2/G3 共用一次渲染。
//
// 返回路由器是为了让 G3 能跟随 href 发第二次请求验证链接可用性——若每个用例各起一次
// 服务器，34 项矩阵会把测试时间放大一个数量级。
// 同时把「根目录无 Parent 行」（G4 不变式）作为前置校验：Parent 行的 href 是 "../"，
// 会污染 indexGuardRows 的键空间。
func guardMatrixBody(t *testing.T) (body string, created map[string]bool, r *gin.Engine) {
	t.Helper()
	root := t.TempDir()
	created = guardCreateMatrix(t, root)
	r = newNoRouteRouter(Config{Root: root})
	rec := do(r, http.MethodGet, "/", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	body = rec.Body.String()
	require.NotContains(t, body, "Parent Directory", "根目录列表不应含 Parent 行")
	return body, created, r
}

// guardRowKey 从单行 href 反查出它对应的条目名（解码 + 去 './' 前缀 + 去目录尾斜杠）。
func guardRowKey(t *testing.T, row guardRow) string {
	t.Helper()
	dec, err := url.PathUnescape(html.UnescapeString(row.href))
	require.NoErrorf(t, err, "href %q 必须是合法的百分号编码", row.href)
	return strings.TrimSuffix(strings.TrimPrefix(dec, "./"), "/")
}

// guardSub 把文件名转成可安全用于 t.Run 的子测试名。
//
// t.Run 的名字含 '/' 会被解释为嵌套层级；控制字符（\n、\t、\x01）会让 -v 输出错乱甚至
// 破坏 CI 日志解析，故统一转为 %q 可打印形态并把 '/' 换成可见替代符。
func guardSub(name string) string {
	return strings.ReplaceAll(strconv.Quote(name), "/", "∕")
}

// guardCreateMatrix 在 root 下创建矩阵文件，返回成功创建的名字集合。
//
// 创建失败的处理分两档，避免用 Skip 掩盖真实缺陷：
//   - Windows：文件名禁用 ? * : " < > | \，矩阵中大量项不可创建 → Skip 并注明
//   - 其他平台：矩阵项均经 darwin/arm64 实测可创建，失败即为环境/实现异常 → 硬失败
func guardCreateMatrix(t *testing.T, root string) map[string]bool {
	t.Helper()
	created := make(map[string]bool, len(guardNameMatrix))
	for _, c := range guardNameMatrix {
		err := os.WriteFile(filepath.Join(root, c.name), []byte("X"), 0o644)
		if err != nil {
			if runtime.GOOS == "windows" {
				t.Logf("SKIP-WINDOWS %q: %v", c.name, err)
				continue
			}
			require.NoErrorf(t, err, "在非 Windows 平台上矩阵项 %q 应可创建", c.name)
		}
		created[c.name] = true
	}
	return created
}

// ---------------------------------------------------------------------------
// G1 —— 文本上下文转义特征化（byte-exact）
// ---------------------------------------------------------------------------

// TestRender_NameTextEscaping_Characterization 钉住 `<td>` 名称单元格的文本上下文转义。
//
// P1 重写（已落地）让本测试**逐字节**保持了 GREEN，任何后续渲染重写同样必须：
// golden 是内联字面量而非模板现渲染，因此删除 dirListingTmpl 不会让期望值一起消失。
func TestRender_NameTextEscaping_Characterization(t *testing.T) {
	body, created, _ := guardMatrixBody(t)
	rows := indexGuardRows(t, parseGuardRows(t, body))

	// 前置：解析出的行数必须等于成功创建的条目数，否则说明有行被漏解析或重复解析，
	// 后续按键断言会静默跳过未解析到的条目（假通过）。
	require.Lenf(t, rows, len(created), "解析出的数据行数应等于创建的条目数（created=%d）", len(created))

	for _, c := range guardNameMatrix {
		c := c
		t.Run(guardSub(c.name), func(t *testing.T) {
			if !created[c.name] {
				t.Skipf("当前平台不接受该文件名（Windows 禁用 ? * : \" < > | \\）: %q", c.name)
			}
			row, ok := rows[c.name]
			require.Truef(t, ok, "应能通过 href 反查到条目 %q（已解析键：%v）", c.name, keysOf(rows))

			// 核心断言：文本上下文逐字节一致。
			// 这是 P1 的主护栏——手写渲染只要漏掉任一字节（尤其是 html.EscapeString
			// 不覆盖的 `+` → &#43;），此处立即 RED。
			assert.Equalf(t, c.wantText, row.text,
				"名称单元格文本上下文转义必须与 html/template 逐字节一致（name=%q）", c.name)

			// 结构性断言：转义后的文本不得含裸 HTML 语法字符。
			// wantText 已保证这点，此处独立复检以防 golden 本身被误改。
			assert.NotContainsf(t, row.text, "<", "转义后的显示名不得含裸 <（name=%q）", c.name)
			assert.NotContainsf(t, row.text, ">", "转义后的显示名不得含裸 >（name=%q）", c.name)

			// 每行必须恰好三个单元格，防止转义缺失把内容溢出到相邻单元格。
			assert.Equalf(t, 3, strings.Count(row.raw, "</td>"), "行应有 3 个 </td>：%q", row.raw)
		})
	}

	// 整页级断言：即使单个用例被 Skip，这些断言仍能捕获全局性的转义缺失。
	t.Run("no-raw-script-tag", func(t *testing.T) {
		assert.NotContains(t, body, "<script>", "输出中不得出现原始 <script>")
		assert.NotContains(t, body, "<img", "输出中不得出现原始 <img>")
	})
	t.Run("no-title-breakout", func(t *testing.T) {
		// 页面自身只有 1 个 <title>/</title>；出现第 2 个即说明目录名成功破出了标题上下文。
		assert.Equal(t, 1, strings.Count(body, "<title>"), "只应有页面自身的 1 个 <title>")
		assert.Equal(t, 1, strings.Count(body, "</title>"), "只应有页面自身的 1 个 </title>")
		assert.Equal(t, 1, strings.Count(body, "<h1>"), "只应有页面自身的 1 个 <h1>")
		assert.Equal(t, 1, strings.Count(body, "</h1>"), "只应有页面自身的 1 个 </h1>")
	})
	t.Run("no-attribute-quote-escape", func(t *testing.T) {
		// 属性引号逃逸会破坏 <a> 标签配对：每个 <a 必须有配对的 </a>，且都必须带 href。
		nOpen := strings.Count(body, "<a ")
		assert.Equal(t, nOpen, strings.Count(body, "</a>"), "<a 与 </a> 必须配对")
		assert.Equal(t, nOpen, strings.Count(body, `href="`), "每个 <a 必须有且仅有一个 href=")
	})
}

// TestRender_PathTextEscaping_Characterization 钉住 `<title>` 与 `<h1>` 中路径的文本转义。
//
// 路径上下文是**唯一**能让 `</title>`、`</script>` 这类闭合标签抵达渲染器的通道：
// 单个文件名不可能含 '/'，但多级目录的 URL 路径天然含 '/'，故用嵌套目录构造。
// 这正是当时 HTML 输出安全实证探针使用的形态，此处把它固化为可回归的 golden。
func TestRender_PathTextEscaping_Characterization(t *testing.T) {
	cases := []struct {
		dirPath   string // 相对 Root 的目录路径（可含多级）
		wantTitle string // <title> 整行 golden
		wantH1    string // <h1> 整行 golden
	}{
		{
			// 最关键用例：路径中同时含 </title> 与 </script> 闭合标签。
			// MkdirAll 会把它拆成 4 级目录（x< / title>< / script>y< / script>），
			// 拼回的 urlPath 仍是完整的 /x</title><script>y</script>/。
			dirPath:   "x</title><script>y</script>",
			wantTitle: "<title>Index of /x&lt;/title&gt;&lt;script&gt;y&lt;/script&gt;/</title>",
			wantH1:    "<h1>Index of /x&lt;/title&gt;&lt;script&gt;y&lt;/script&gt;/</h1>",
		},
		{
			dirPath:   "a&b",
			wantTitle: "<title>Index of /a&amp;b/</title>",
			wantH1:    "<h1>Index of /a&amp;b/</h1>",
		},
		{
			dirPath:   `d"q`,
			wantTitle: "<title>Index of /d&#34;q/</title>",
			wantH1:    "<h1>Index of /d&#34;q/</h1>",
		},
		{
			dirPath:   "d'q",
			wantTitle: "<title>Index of /d&#39;q/</title>",
			wantH1:    "<h1>Index of /d&#39;q/</h1>",
		},
		{
			// 与名称单元格同源的分歧点：路径上下文的 `+` 同样被 html/template 转义为 &#43;。
			dirPath:   "a+b",
			wantTitle: "<title>Index of /a&#43;b/</title>",
			wantH1:    "<h1>Index of /a&#43;b/</h1>",
		},
		{
			dirPath:   "a=b",
			wantTitle: "<title>Index of /a=b/</title>",
			wantH1:    "<h1>Index of /a=b/</h1>",
		},
		{
			dirPath:   "<h1>",
			wantTitle: "<title>Index of /&lt;h1&gt;/</title>",
			wantH1:    "<h1>Index of /&lt;h1&gt;/</h1>",
		},
		{
			dirPath:   "<script>alert(1)",
			wantTitle: "<title>Index of /&lt;script&gt;alert(1)/</title>",
			wantH1:    "<h1>Index of /&lt;script&gt;alert(1)/</h1>",
		},
		{
			// 多字节 UTF-8 在文本上下文原样透传（不做百分号编码），钉住 charset=utf-8 语义。
			dirPath:   "测试目录",
			wantTitle: "<title>Index of /测试目录/</title>",
			wantH1:    "<h1>Index of /测试目录/</h1>",
		},
	}

	for _, c := range cases {
		c := c
		t.Run(guardSub(c.dirPath), func(t *testing.T) {
			root := t.TempDir()
			sub := filepath.Join(root, c.dirPath)
			// MkdirAll 会把路径中的 '/' 解释为层级分隔符——这正是我们需要的：
			// 单个目录名不可能含 '/'，但 URL 路径可以。
			if err := os.MkdirAll(sub, 0o755); err != nil {
				t.Skipf("当前平台无法构造该多级目录路径 %q: %v", c.dirPath, err)
			}
			require.NoError(t, os.WriteFile(filepath.Join(sub, "inner.txt"), []byte("i"), 0o644))

			r := newNoRouteRouter(Config{Root: root})
			// 请求目标须对路径分段做百分号编码；net/url 是 stdlib、非被测代码，
			// 用它构造请求不构成自证循环。
			target := (&url.URL{Path: "/" + c.dirPath + "/"}).String()
			rec := do(r, http.MethodGet, target, nil)
			require.Equalf(t, http.StatusOK, rec.Code, "GET %s 应 200", target)
			body := rec.Body.String()

			assert.Equal(t, c.wantTitle, guardLine(t, body, "<title>"), "<title> 路径转义必须逐字节一致")
			assert.Equal(t, c.wantH1, guardLine(t, body, "<h1>"), "<h1> 路径转义必须逐字节一致")

			// 破标题/破 h1 的独立判据：整页只允许页面自身的那一个闭合标签。
			assert.Equal(t, 1, strings.Count(body, "</title>"), "目录名不得破出 <title> 上下文")
			assert.Equal(t, 1, strings.Count(body, "</h1>"), "目录名不得破出 <h1> 上下文")
			assert.NotContains(t, body, "<script>", "输出中不得出现原始 <script>")
			assert.Equal(t, 1, strings.Count(body, "<style>"), "内联 <style> 块必须存在（CSP 冲突测试依赖）")
		})
	}
}

// TestRender_UnreachableNames_DocumentedGaps 显式记录结构上不可能抵达渲染层的输入。
//
// 存在的唯一理由是让"未覆盖"在 -v 输出中可见（SKIP + 原因），而不是从矩阵里悄悄删掉。
// 每条 reason 都写明了替代覆盖点或对手写转义器（P1 已落地）的具体要求。
func TestRender_UnreachableNames_DocumentedGaps(t *testing.T) {
	for _, c := range guardUnreachableCases {
		c := c
		t.Run(guardSub(c.name), func(t *testing.T) {
			t.Skipf("结构上不可达：%s", c.reason)
		})
	}
}

func guardLine(t *testing.T, body, marker string) string {
	t.Helper()
	i := strings.Index(body, marker)
	require.GreaterOrEqualf(t, i, 0, "输出应含 %s：%q", marker, body)
	j := strings.Index(body[i:], "\n")
	require.GreaterOrEqualf(t, j, 0, "%s 所在行应以换行结束", marker)
	return body[i : i+j]
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ---------------------------------------------------------------------------
// G2 —— href 安全属性（property-based）
// ---------------------------------------------------------------------------

// guardCharRefRe 匹配一个合法的 HTML 字符引用（命名实体 / 十进制 / 十六进制）。
//
// 用途：判断 href 中的 `&` 是否**全部**处于字符引用起始位置。当前实现会输出
// `&amp;`（来自 &）与 `&#43;`（来自 +），故 href 内含 `&` 是合法的；合法与否的
// 判据不是"有没有 &"，而是"剥掉全部合法字符引用后还有没有残留的裸 &"。
var guardCharRefRe = regexp.MustCompile(`&(?:#[0-9]+;|#[xX][0-9a-fA-F]+;|[a-zA-Z][a-zA-Z0-9]{1,31};)`)

// guardDangerousSchemes 是不得作为 href 字面 scheme 出现的协议（已含尾随 ':'）。
var guardDangerousSchemes = []string{
	"javascript:", "data:", "vbscript:", "file:", "about:", "blob:",
}

// guardWHATWGNormalize 复现 WHATWG URL 解析器对输入串的预处理：
// 删除全部 ASCII tab/LF/CR，再裁剪首尾 C0-control-or-space。
//
// 为什么必须做这一步：浏览器在解析 scheme **之前**就会执行上述归一化，因此
// `java\tscript:alert(1)` 在浏览器中等价于 `javascript:alert(1)`。任何"直接对
// 原始属性值做前缀匹配"或"仅做百分号解码后做前缀匹配"的判据都会漏掉这一绕过形态
// （矩阵中的 "javascript\t:x" 即为此而设）。
func guardWHATWGNormalize(s string) string {
	s = strings.NewReplacer("\t", "", "\n", "", "\r", "").Replace(s)
	const c0AndSpace = "\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x0e\x0f" +
		"\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f\x20"
	return strings.Trim(s, c0AndSpace)
}

// guardAssertHrefSafety 实施 G2 的四条**机制无关**安全判据。
//
// 「机制无关」的含义：P1 之前实现靠 net/url 依 RFC 3986 §4.2 对首段含 ':' 的路径前置 `./`
// 来消除 scheme 歧义；P1 落地后改为「严格百分号编码（':' → %3A）」。两种机制
// 产出的 href 字节完全不同，但都必须满足下列判据——这正是把安全性钉在性质而非快照上的
// 原因，否则护栏会阻挡它本应保护的那次重写。
func guardAssertHrefSafety(t *testing.T, name, href string) {
	t.Helper()

	// 判据 A：属性完整性。
	// 裸 `"` 会提前闭合属性，把后续字节推进标签上下文（属性逃逸 → 任意属性/标签注入）；
	// 裸 `<`/`>` 直接构成标签；CR/LF 在部分客户端可造成 HTML 结构错乱。
	assert.NotContainsf(t, href, `"`, "href 不得含裸双引号（属性逃逸）：name=%q", name)
	assert.NotContainsf(t, href, "<", "href 不得含裸 <（标签注入）：name=%q", name)
	assert.NotContainsf(t, href, ">", "href 不得含裸 >（标签注入）：name=%q", name)
	assert.NotContainsf(t, href, "\r", "href 不得含裸 CR：name=%q", name)
	assert.NotContainsf(t, href, "\n", "href 不得含裸 LF：name=%q", name)
	// 裸 & 会与后续字节形成实体歧义（如 &copy.txt 被解析成 ©.txt）。
	residual := guardCharRefRe.ReplaceAllString(href, "")
	assert.NotContainsf(t, residual, "&",
		"href 中的 & 必须全部是合法字符引用的起始（残留裸 &）：name=%q href=%q", name, href)

	// 判据 B：浏览器语义归一化后不得是危险 scheme 的字面前缀。
	norm := strings.ToLower(guardWHATWGNormalize(href))
	for _, s := range guardDangerousSchemes {
		assert.Falsef(t, strings.HasPrefix(norm, s),
			"href 归一化后不得以危险 scheme %q 开头：name=%q href=%q", s, name, href)
	}

	// 判据 C：按 RFC 3986 解析，scheme 必须为空（相对引用）或在白名单内。
	//
	// 这是浏览器实际采用的判据，也是本文件对「任务书要求的百分号解码判据」的精确化：
	// RFC 3986 §3.1 规定 scheme 分隔符是**字面** ':'，`%3A` 只是路径段内的普通字符，
	// 任何 URL 解析器（WHATWG、net/url）都不会把它当作 scheme 分隔符。因此
	// 「先百分号解码再比前缀」会对 P1 已落地的严格百分号编码产出的 `%3A` 产生**假失败**，
	// 把正确的实现误判为绕过；此处改用解析器语义，既抓住真绕过又不误伤。
	u, err := url.Parse(href)
	require.NoErrorf(t, err, "href %q 必须是可解析的 URL（name=%q）", href, name)
	switch strings.ToLower(u.Scheme) {
	case "", "http", "https", "mailto":
	default:
		t.Errorf("href 的 scheme 必须为空或在 {http,https,mailto} 内，实得 %q：name=%q href=%q",
			u.Scheme, name, href)
	}

	// 判据 D：其余危险形态。
	// 协议相对 URL（//host/path）会继承页面 scheme 指向外部主机，构成开放重定向/数据外泄面。
	assert.Falsef(t, strings.HasPrefix(norm, "//"),
		"href 不得为协议相对 URL：name=%q href=%q", name, href)
	assert.Falsef(t, strings.HasPrefix(norm, `\\`),
		"href 不得以反斜杠开头（部分浏览器把 \\ 当 / 解析）：name=%q href=%q", name, href)
	// NUL 在任何 URL 中都是非法字节。
	assert.NotContainsf(t, strings.ToUpper(href), "%00", "href 不得编码 NUL：name=%q", name)
	// '#' 开头意味着页内锚点（点击不发请求 = 静默死链）。历史上唯一可接受形态是原
	// html/template urlFilter 的 failsafe #ZgotmplZ；P1 落地的严格百分号编码把 '#' 编码
	// 为 %23，当前实现不可能产出 '#' 开头的 href，此分支成为常真 failsafe（若放宽编码立即 RED）。
	if strings.HasPrefix(norm, "#") {
		assert.Equalf(t, "#ZgotmplZ", href,
			"'#' 开头的 href 只允许是 html/template 的 failsafe：name=%q href=%q", name, href)
	}
}

// TestRender_HrefSafety_PropertyBased 对全矩阵实施 G2 的 href 安全性质断言。
//
// 断言是**属性级**的：先用 parseOneGuardRow 精确提取 href="…" 的属性值，再逐条判据校验。
// 刻意不使用 assert.Contains(body, …) —— 整页 Contains 无法区分"该值出现在 href 属性里"
// 与"该值出现在别处"，转义缺失时极易假通过。
func TestRender_HrefSafety_PropertyBased(t *testing.T) {
	body, created, _ := guardMatrixBody(t)
	rows := parseGuardRows(t, body)
	require.Lenf(t, rows, len(created), "解析出的数据行数应等于创建的条目数（created=%d）", len(created))

	for _, row := range rows {
		row := row
		key := guardRowKey(t, row)
		t.Run(guardSub(key), func(t *testing.T) {
			guardAssertHrefSafety(t, key, row.href)
		})
	}

	// 表头三个自链接与 Parent 链接（非根目录）同样走安全判据：
	// P1 已把这些常量/拼接值一并搬进 strings.Builder，故它们也在护栏范围内。
	t.Run("header-self-links", func(t *testing.T) {
		links := guardHeaderLinks(t, body)
		require.Len(t, links, 3, "表头应恰有 3 个自链接（Name / Last modified / Size）")
		for _, link := range links {
			guardAssertHrefSafety(t, "<header>", link)
		}
	})
	t.Run("parent-link", func(t *testing.T) {
		root2 := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root2, "sub"), 0o755))
		r2 := newNoRouteRouter(Config{Root: root2})
		rec2 := do(r2, http.MethodGet, "/sub/", nil)
		require.Equal(t, http.StatusOK, rec2.Code)
		for _, link := range guardHeaderLinks(t, rec2.Body.String()) {
			guardAssertHrefSafety(t, "<header>", link)
		}
		guardAssertHrefSafety(t, "<parent>", guardParentHref(t, rec2.Body.String()))
	})
}

// guardHeaderLinks 提取表头三列自链接的 href 原值。
func guardHeaderLinks(t *testing.T, body string) []string {
	t.Helper()
	i := strings.Index(body, "<tr><th>")
	require.GreaterOrEqual(t, i, 0, "应有表头行")
	j := strings.Index(body[i:], "</tr>")
	require.GreaterOrEqual(t, j, 0, "表头行应闭合")
	seg := body[i : i+j]
	var out []string
	rest := seg
	for {
		k := strings.Index(rest, `href="`)
		if k < 0 {
			return out
		}
		rest = rest[k+len(`href="`):]
		e := strings.Index(rest, `"`)
		require.GreaterOrEqual(t, e, 0, "表头 href 应闭合")
		out = append(out, rest[:e])
		rest = rest[e:]
	}
}

// guardParentHref 提取 Parent Directory 行的 href 原值。
func guardParentHref(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, "Parent Directory")
	require.GreaterOrEqual(t, i, 0, "非根目录列表应含 Parent Directory 行")
	k := strings.LastIndex(body[:i], `href="`)
	require.GreaterOrEqual(t, k, 0, "Parent 行应有 href")
	rest := body[k+len(`href="`):]
	e := strings.Index(rest, `"`)
	require.GreaterOrEqual(t, e, 0, "Parent href 应闭合")
	return rest[:e]
}

// ---------------------------------------------------------------------------
// G3 —— href 往返正确性（round-trip）
// ---------------------------------------------------------------------------

// TestRender_HrefRoundTrip_PropertyBased 断言 href 可反解出原始文件名。
//
// 解码链必须与浏览器一致：先解 HTML 字符引用（属性值里的 &amp; / &#43;），
// 再解百分号（%20 / %3F / %E6%B5%8B）。顺序颠倒会把 `%26amp;` 之类解错。
//
// 该断言同时钉住「无双重编码」：`?q.txt` 的 href 必须是 `%3Fq.txt`，若是 `%253Fq.txt`
// 则解码一次得到 `%3Fq.txt` ≠ 原名，立即 RED。
//
// 范围：仅**不含 ':'** 的名字。含 ':' 者 P1 前 href 带 `./` 前缀（net/url 的 RFC 3986 §4.2
// 处理）、P1 落地后为严格百分号编码，其往返语义由
// TestRender_HrefSchemeLike_CurrentBehavior 专项钉住。
func TestRender_HrefRoundTrip_PropertyBased(t *testing.T) {
	body, created, _ := guardMatrixBody(t)
	rows := indexGuardRows(t, parseGuardRows(t, body))

	checked := 0
	for _, c := range guardNameMatrix {
		if c.colon {
			continue
		}
		c := c
		t.Run(guardSub(c.name), func(t *testing.T) {
			if !created[c.name] {
				t.Skipf("当前平台不接受该文件名: %q", c.name)
			}
			row, ok := rows[c.name]
			require.Truef(t, ok, "应能通过 href 反查到条目 %q", c.name)

			got, err := url.PathUnescape(html.UnescapeString(row.href))
			require.NoErrorf(t, err, "href %q 应可解码", row.href)
			assert.Equalf(t, c.name, got,
				"href 解码后必须等于原始文件名（链接可用性 + 无双重编码）：href=%q", row.href)

			// 双重编码检测：上面的 round-trip 相等已完整覆盖（多编码一次必然解不还原名）。
			// 这里补一条更直观的判据——**原名不含 '%'** 时 href 就不该出现 '%25'（'%25' 是
			// '%' 的编码）。原名本身含 '%' 的情况（"%2e%2e.txt"、"100%.txt"）出现 '%25'
			// 恰恰是正确的，故必须按原名是否含 '%' 分流，否则会把正确行为误判为缺陷。
			if !strings.Contains(c.name, "%") {
				assert.NotContainsf(t, row.href, "%25",
					"原名不含 '%%' 时 href 不应出现 '%%25'（双重编码）：name=%q href=%q", c.name, row.href)
			}
			checked++
		})
	}
	assert.Greater(t, checked, 0, "至少应有一个 round-trip 子用例实际执行（防止整体被 Skip 而假通过）")

	// 任务书点名的具体反例：'?' 必须是 %3F 而非 %253F。
	t.Run("question-mark-single-encoding", func(t *testing.T) {
		row, ok := rows["?q.txt"]
		if !ok {
			t.Skip("当前平台不接受 '?' 文件名（Windows 禁用字符）")
		}
		assert.Contains(t, row.href, "%3F", "'?' 应编码为 %3F")
		assert.NotContains(t, row.href, "%253F", "不得与 html/template 双重编码为 %253F")
	})
}

// TestRender_HrefSchemeLike_CurrentBehavior 钉住含 ':' 文件名的 href **当前实际行为**。
//
// ── 对任务书前提的实证修正（重要，后续 Worker 必读；以下为 P1 落地前形态的实证记录）──
//
// 任务书假设「含 ':' 的名字当前 href 被 html/template 的 urlFilter 替换为 #ZgotmplZ，
// 即链接失效（该假设曾被当作已知缺陷记录）」。**实测证伪**，证据链如下：
//
//  1. href 百分号编码修复已把 href 改为 `(&url.URL{Path: name}).String()`。net/url 的 URL.String()
//     实现了 RFC 3986 §4.2：当相对引用的首个路径段含 ':' 时前置 "./"，以免被误读为 scheme。
//     实测：`(&url.URL{Path:"javascript:alert(1)"}).String()` == "./javascript:alert%281%29"
//     `(&url.URL{Path:"a:b.txt"}).String()`     == "./a:b.txt"
//  2. html/template 的 isSafeURL（url.go:47-54）判定逻辑是
//     `protocol, _, ok := strings.Cut(s, ":"); ok && !strings.Contains(protocol, "/")`。
//     对 "./javascript:alert%281%29"，Cut 得到 protocol="./javascript"，**含 '/'** →
//     条件不成立 → isSafeURL 返回 true → **urlFilter 从未被触发**，#ZgotmplZ 不会出现。
//     对照实验：把未经 net/url 处理的裸 "javascript:alert(1)" 喂给同一模板，
//     确实得到 `href="#ZgotmplZ"`——证明 urlFilter 仍在，只是被 `./` 前缀正确地短路了。
//  3. 链接可用性实测：GET "/a:b.txt" → 200 + 正确内容；GET "/javascript:alert(1)" → 200。
//     即 **`a:b.txt` 不是死链，「scheme-like 名字会成死链」的假设不成立**。
//
// 结论（P1 落地前实测当时）：真正阻断 scheme 注入的防线是 **net/url 的 `./` 前缀**，不是 urlFilter。
// 这一结论曾对 P1 有直接影响——`./` 前缀是 `url.URL.String()` 的副产品，P1 换成手写百分号
// 编码后该副产品随之消失，安全性改由「':' 被编码为 %3A」承担（%3A 不构成 scheme
// 分隔符，见 RFC 3986 §3.1；已落地，见 autoindex.go 的 appendPathSegmentEscape 安全论证）。
// G2 的判据 B/C 正是按这个等价关系设计的，两种机制都能过。
//
// ── 本测试须如何被改写（**已于 P1 落地时按此指示完成**，以下保留为改写依据）──
//
// P1 落地后，本测试中原标注 `// NEXT-ROUND-REWRITE:` 的断言须改写为：
//
//	a:b.txt              → href "a%3Ab.txt"（或等价的严格编码形态），解码后等于原名
//	javascript:alert(1)  → href "javascript%3Aalert%281%29"，url.Parse(...).Scheme == ""，
//	                       且 WHATWG 归一化后不以 "javascript:" 开头
//
// 未标注的断言（安全性判据、链接可用性）在两种机制下都必须保持 GREEN，不得放宽。
func TestRender_HrefSchemeLike_CurrentBehavior(t *testing.T) {
	body, created, r := guardMatrixBody(t)
	rows := indexGuardRows(t, parseGuardRows(t, body))

	// 严格百分号编码后的 href 快照（darwin/arm64 + Go 1.26.8 实测，P1 落地后重采）。
	// 这张表**只在非 Windows 平台**成立（Windows 禁用 ':' 等字符，矩阵项创建失败即 Skip）。
	// 与 P1 前的 net/url 形态相比：':' → %3A、',' → %2C、'(' ')' → %28 %29、
	// 空格 → %20、tab → %09，且不再有 "./" 前缀（scheme 消歧改由 %3A 承担，见下方断言）。
	currentHref := map[string]string{
		"a:b.txt":             "a%3Ab.txt",
		"javascript:alert(1)": "javascript%3Aalert%281%29",
		"JAVASCRIPT:alert(2)": "JAVASCRIPT%3Aalert%282%29",
		"JaVaScRiPt:alert(4)": "JaVaScRiPt%3Aalert%284%29",
		"data:text,x":         "data%3Atext%2Cx",
		"vbscript:msgbox(1)":  "vbscript%3Amsgbox%281%29",
		" javascript:x":       "%20javascript%3Ax",
		"javascript\t:x":      "javascript%09%3Ax",
	}

	checked := 0
	for _, c := range guardNameMatrix {
		if !c.colon {
			continue
		}
		c := c
		t.Run(guardSub(c.name), func(t *testing.T) {
			if !created[c.name] {
				t.Skipf("当前平台不接受该文件名: %q", c.name)
			}
			row, ok := rows[c.name]
			require.Truef(t, ok, "应能通过 href 反查到条目 %q", c.name)

			// 安全判据：与 G2 同源，两种实现机制都必须满足。
			guardAssertHrefSafety(t, c.name, row.href)

			// P1 已落地，本块按上方「本测试须如何被改写」的指示完成改写：
			//   ① href 快照由「net/url 的 './' 前缀 + 保留字面 ':'」改为严格百分号编码形态；
			//   ② 「'./' 前缀」这一**副产品**判据被替换为它原本要保护的**真实性质**——
			//      href 内不得存在字面 ':'（RFC 3986 §3.1 的 scheme 分隔符），
			//      且 url.Parse 得到的 Scheme 必须为空（即只能是相对引用）。
			//      这比原判据更强：原判据只检查前缀，本判据检查整串不含 scheme 分隔符。
			// 下方未标注的安全判据（guardAssertHrefSafety）、链接可用性与 round-trip
			// 断言一律未改动、未放宽。
			if runtime.GOOS != "windows" {
				assert.Equalf(t, currentHref[c.name], row.href,
					"含 ':' 文件名的 href 应为严格百分号编码形态（':' → %%3A）：name=%q", c.name)
				assert.NotContainsf(t, row.href, ":",
					"严格编码后 href 内不得残留字面 ':'（否则构成 scheme 分隔符）：name=%q href=%q", c.name, row.href)
				u, uerr := url.Parse(row.href)
				require.NoErrorf(t, uerr, "href %q 必须是可解析的 URL（name=%q）", row.href, c.name)
				assert.Emptyf(t, u.Scheme,
					"href 必须是纯相对引用（Scheme 为空）：name=%q href=%q", c.name, row.href)
			}

			// 链接可用性：这是「scheme-like 名字不成死链」的直接证据，两种机制下都必须保持。
			//
			// 忠实模拟浏览器的解析链，而不是自己拼一个"看起来对"的 target：
			//   1. HTML 解析器先解属性值里的字符引用（&amp; → &）；
			//   2. 按 base URL（此处为 "/"）解析相对引用，"./x" → "/x"；
			//   3. 把**仍带百分号编码**的路径原样写进 request-URI。
			// 第 3 步是关键：若在此处提前解码，含空格/tab 的名字会造出非法 request-URI
			// （裸空格会让 httptest.NewRequest 报 malformed HTTP version），
			// 而浏览器上线的从来是编码后的形态。
			attrValue := html.UnescapeString(row.href)
			target := "/" + strings.TrimPrefix(attrValue, "./")
			rec2 := do(r, http.MethodGet, target, nil)
			assert.Equalf(t, http.StatusOK, rec2.Code,
				"含 ':' 的合法文件名必须可点击下载（不得是死链）：name=%q href=%q GET %s", c.name, row.href, target)

			// 往返正确性：完整解码（字符引用 + 百分号）并去 './' 前缀后必须还原原名。
			assert.Equalf(t, c.name, strings.TrimPrefix(mustPathUnescape(t, row.href), "./"),
				"href 解码后必须还原原始文件名：name=%q href=%q", c.name, row.href)
			checked++
		})
	}
	assert.Greater(t, checked, 0, "至少应有一个 scheme-like 子用例实际执行")

	// 原有的 stdlib-urlfilter-still-active 对照子测试与 guardProbeURLFilter 辅助函数
	// 已按本节头部注释的交接说明删除：它们是「证明 urlFilter 从未被本包触发、真正的
	// 防线是 net/url 的 './' 前缀」的对照实验，而 P1 已删除 dirListingTmpl，
	// 本包不再依赖 html/template，该对照随之失去意义（其结论已固化进上方
	// 「href 内不得有字面 ':'」+「Scheme 必须为空」两条更强的性质断言，
	// 以及 autoindex.go 中 appendPathSegmentEscape 的安全论证注释）。
}

func mustPathUnescape(t *testing.T, s string) string {
	t.Helper()
	out, err := url.PathUnescape(html.UnescapeString(s))
	require.NoErrorf(t, err, "%q 应可解码", s)
	return out
}

// guardProbeURLFilter 已删除（见 TestRender_HrefSchemeLike_CurrentBehavior 末尾说明）：
// 它曾是本文件唯一依赖 html/template 的地方，P1 已删除 dirListingTmpl，其对照实验随之失去意义。

// ---------------------------------------------------------------------------
// G4 —— 渲染结构不变式
// ---------------------------------------------------------------------------

// TestRender_StructuralInvariants 钉住重写后必须保持的页面骨架。
//
// 这些不是"美观"断言：
//   - 内联 <style> 块是 autoindex_conflict_test.go 中 CSP 冲突测试的依赖物，
//     若把样式改成外链会让那些测试静默失去意义；
//   - 表头自链接的 `;` 分隔形态与 parseSortParams 的 ReplaceAll(";"→"&") 归一化是
//     同一契约的两面，两者必须同时改动，单改一边即断链；
//   - 数据行数口径 `strings.Count(body, "<tr><td>")` 是既有测试与 bench 共用的计数方式。
func TestRender_StructuralInvariants(t *testing.T) {
	root := newTestTree(t, map[string]string{
		"aaa.txt":      "a",
		"zzz-last.txt": "z",
		"sub/inner.md": "# i\n",
	}, "zzz-dir", "sub")
	r := newNoRouteRouter(Config{Root: root})

	t.Run("root", func(t *testing.T) {
		rec := do(r, http.MethodGet, "/", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		body := rec.Body.String()

		assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"),
			"目录列表必须显式声明 charset，否则非 ASCII 文件名在旧客户端会乱码")
		assert.Truef(t, strings.HasPrefix(body, "<!DOCTYPE html>"), "必须以 <!DOCTYPE html> 开头（否则浏览器进入怪异模式）：%q", body[:min(40, len(body))])
		assert.Contains(t, body, "<title>Index of /</title>")
		assert.Contains(t, body, "<h1>Index of /</h1>")
		assert.NotContains(t, body, "Parent Directory", "根目录不得有 Parent 行（无处可回退）")
		assert.Contains(t, body, "<style>", "内联 <style> 块必须存在（CSP 冲突测试依赖）")

		// 数据行数口径：根目录无 Parent 行，故 Count("<tr><td>") 直接等于条目数。
		// 条目 = aaa.txt, zzz-last.txt, zzz-dir/, sub/ （sub/inner.md 不属本层）
		assert.Equal(t, 4, strings.Count(body, "<tr><td>"), "根目录数据行数应等于非 ignore 条目数")

		// FoldersFirst：目录组整体在文件组之前。
		//
		// 本断言具有判别力：若退化为「不分组的纯名称排序」，序列会变成
		// aaa.txt, sub/, zzz-dir/, zzz-last.txt —— 与期望不同，故能抓住分组逻辑被删。
		// 组内各自按当前排序键升序，故目录组是 sub/ 在 zzz-dir/ 之前（不是写入顺序）。
		assertOrder(t, body, "sub/", "zzz-dir/", "aaa.txt", "zzz-last.txt")
	})

	t.Run("subdir", func(t *testing.T) {
		rec := do(r, http.MethodGet, "/sub/", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		body := rec.Body.String()

		assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
		assert.True(t, strings.HasPrefix(body, "<!DOCTYPE html>"))
		assert.Contains(t, body, "<title>Index of /sub/</title>", "非根路径标题须带尾斜杠")
		assert.Contains(t, body, "<style>")

		// 非根目录：Parent 行存在，且 href 必须是相对形式 "../"
		// （绝对形式会让 NoRoute 根挂载与前缀挂载两种形态之一失效）。
		assert.Equal(t, "../", guardParentHref(t, body), "Parent 链接必须是相对的 ../")

		// 非根目录的 Count("<tr><td>") 含 Parent 行，故条目数 = Count - 1。
		assert.Equal(t, 2, strings.Count(body, "<tr><td>"), "Parent 行 + 1 个条目")
		assert.Equal(t, 1, strings.Count(body, "<tr><td>")-1, "扣除 Parent 行后应等于条目数")
	})

	// 表头自链接用 ';' 分隔（规避 HTML '&' 转义），且当前列升序时切换为 O=D。
	headerCases := []struct {
		query               string
		wantN, wantM, wantS string
	}{
		{"", "?C=N;O=D", "?C=M;O=A", "?C=S;O=A"},         // 默认 C=N;O=A → Name 切降序
		{"?C=N;O=D", "?C=N;O=A", "?C=M;O=A", "?C=S;O=A"}, // 当前列已降序 → 切回升序
		{"?C=S;O=A", "?C=N;O=A", "?C=M;O=A", "?C=S;O=D"}, // Size 升序 → Size 切降序
		{"?C=M;O=D", "?C=N;O=A", "?C=M;O=A", "?C=S;O=A"}, // Mtime 降序 → Mtime 切回升序
	}
	for _, hc := range headerCases {
		hc := hc
		t.Run("header-links"+hc.query, func(t *testing.T) {
			rec := do(r, http.MethodGet, "/"+hc.query, nil)
			require.Equal(t, http.StatusOK, rec.Code)
			links := guardHeaderLinks(t, rec.Body.String())
			require.Len(t, links, 3, "表头应恰有 3 个自链接")
			assert.Equal(t, hc.wantN, links[0], "Name 列自链接")
			assert.Equal(t, hc.wantM, links[1], "Last modified 列自链接")
			assert.Equal(t, hc.wantS, links[2], "Size 列自链接")
			// ';' 分隔与 parseSortParams 的 ReplaceAll(";","&") 归一化双向配对：
			// 表头若改用 '&' 会被 html 转义成 &amp; 从而破坏归一化前提。
			for _, l := range links {
				assert.Contains(t, l, ";", "表头链接必须用 ';' 分隔")
				assert.NotContains(t, l, "&", "表头链接不得含 '&'")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// G5 —— sortRows 输入顺序无关性（渲染顺序确定性的前置不变式 #1）
// ---------------------------------------------------------------------------

// 当前 renderDirListing 用 os.ReadDir 读目录（stdlib **按名排序**，autoindex.go:350），
// 但下面这条不变式让渲染输出**不依赖任何输入顺序** —— 它是改动目录读取方式（例如换成
// 不排序的 f.ReadDir(-1)）或改动排序器实现时仍然安全的前提：
//
//	渲染输出与 sortRows 的输入顺序无关。
//
// 证明要点（测试注释即证明记录，供后续 Worker 复核）：
//  1. 同一组内（dirs 组 / files 组）文件名唯一 —— 文件系统保证同目录内 base name 不重复；
//  2. 次级键 sortName 在 sortRows 中位于 desc 判断**之外**（autoindex.go 的
//     `return a.sortName < b.sortName`），故主键并列时比较结果恒由唯一的名字决定；
//  3. 由 1+2，比较器构成**完全定序**（任意两个不同元素都可比且不等）；
//  4. 完全定序下排序结果唯一，sort.Slice 的非稳定性因此无影响。
//
// 关键点：第 2 条是唯一支撑。若次级键被删除或移入 desc 分支，比较器退化为偏序，
// sort.Slice（pdqsort，非稳定）的输出将依赖输入顺序 —— G8 变异 3 即验证本测试能抓住它。

// guardSortSpec 是排序夹具的一行规格。
type guardSortSpec struct {
	name  string
	size  int64
	mtime time.Time
}

// guardSortFixture 构造「主键大量重复」的 12 条目夹具。
//
// size 只有 3 个不同值、mtime 只有 3 个不同值，12 个条目必然产生大量并列 —— 这是
// 「次级键恒为名称升序」唯一可被观测的条件。若主键全不相同，次级键永不触发，
// 删掉它也不会 RED，护栏即形同虚设。
func guardSortFixture() []guardSortSpec {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h := time.Hour
	return []guardSortSpec{
		{"a", 200, base},
		{"b", 100, base.Add(h)},
		{"c", 200, base},
		{"d", 100, base.Add(2 * h)},
		{"e", 300, base.Add(h)},
		{"f", 100, base},
		{"g", 200, base.Add(2 * h)},
		{"h", 300, base.Add(h)},
		{"i", 100, base.Add(2 * h)},
		{"j", 200, base},
		{"k", 300, base.Add(h)},
		{"l", 100, base.Add(2 * h)},
	}
}

// guardAllTiesFixture 构造「主键全部相同」的 16 条目夹具（size 与 mtime 均无差异）。
//
// 取 16 而非 12：Go 的 sort.Slice 对 n<=12 走 insertionSort、n>12 走完整 pdqsort，
// 两条代码路径对"比较器返回 false 的并列元素"的处理不同。删掉次级键后，insertionSort
// 会原样保留输入顺序（不交换），pdqsort 则可能部分置换 —— 两个规模都测才能保证
// 变异在任一路径下都被抓住。
func guardAllTiesFixture() []guardSortSpec {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]guardSortSpec, 0, 16)
	for i := 0; i < 16; i++ {
		out = append(out, guardSortSpec{fmt.Sprintf("n%02d", i), 100, base})
	}
	return out
}

// guardBuildRows 由规格构造 sortRows 的输入。
//
// **只设置三个排序键**：sortRows 的契约只依赖排序键；若夹具还设置展示字段，会让本
// 单元测试与展示层的类型变化产生虚假耦合。P1 之后 entryRow 已不再预存展示字段
// （展示串在写出时现场生成，见 autoindex.go 的 entryRow 注释），该耦合面已不存在。
func guardBuildRows(specs []guardSortSpec) []entryRow {
	rows := make([]entryRow, 0, len(specs))
	for _, s := range specs {
		rows = append(rows, entryRow{
			sortName:  s.name,
			sortSize:  s.size,
			sortMtime: s.mtime,
		})
	}
	return rows
}

func guardNames(rows []entryRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.sortName)
	}
	return out
}

// guardInputOrders 返回同一组条目的多种输入顺序：8 个随机种子 + 逆序 + 已排序 + 3 个轮转。
//
// 随机种子固定（非 time-based），保证测试可复现、不 flaky。
func guardInputOrders(n int) [][]int {
	orders := make([][]int, 0, 13)
	identity := make([]int, n)
	for i := range identity {
		identity[i] = i
	}
	orders = append(orders, identity)

	reversed := make([]int, n)
	for i := range reversed {
		reversed[i] = n - 1 - i
	}
	orders = append(orders, reversed)

	for _, seed := range []int64{1, 2, 3, 5, 8, 13, 21, 34} {
		idx := make([]int, n)
		copy(idx, identity)
		rng := rand.New(rand.NewSource(seed))
		rng.Shuffle(n, func(i, j int) { idx[i], idx[j] = idx[j], idx[i] })
		orders = append(orders, idx)
	}

	// 轮转：把尾部若干元素搬到头部，构造"几乎有序但首元素错位"的输入，
	// 这类输入最容易让偏序比较器暴露出不稳定性。
	for _, k := range []int{1, n / 2, n - 1} {
		idx := make([]int, 0, n)
		idx = append(idx, identity[k:]...)
		idx = append(idx, identity[:k]...)
		orders = append(orders, idx)
	}
	return orders
}

func guardPermute(specs []guardSortSpec, order []int) []guardSortSpec {
	out := make([]guardSortSpec, 0, len(specs))
	for _, i := range order {
		out = append(out, specs[i])
	}
	return out
}

// TestSortRows_InputOrderIndependence 单元级钉住「sortRows 输出与输入顺序无关」。
//
// 期望序列是**内联字面量**而非由 sortRows 自身产生：用被测函数给自己算期望值，
// 等价于没有期望值。6 种 (col, desc) 组合 × 13 种输入顺序 = 78 次比对。
func TestSortRows_InputOrderIndependence(t *testing.T) {
	specs := guardSortFixture()

	// 手工推导的期望序列（依据：主键排序 + 次级键恒为名称升序）。
	want := map[string][]string{
		"N-asc":  {"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l"},
		"N-desc": {"l", "k", "j", "i", "h", "g", "f", "e", "d", "c", "b", "a"},
		// size: 100={b,d,f,i,l} 200={a,c,g,j} 300={e,h,k}
		"S-asc":  {"b", "d", "f", "i", "l", "a", "c", "g", "j", "e", "h", "k"},
		"S-desc": {"e", "h", "k", "a", "c", "g", "j", "b", "d", "f", "i", "l"},
		// mtime: +0h={a,c,f,j} +1h={b,e,h,k} +2h={d,g,i,l}
		"M-asc":  {"a", "c", "f", "j", "b", "e", "h", "k", "d", "g", "i", "l"},
		"M-desc": {"d", "g", "i", "l", "b", "e", "h", "k", "a", "c", "f", "j"},
	}
	combos := []struct {
		label string
		col   byte
		desc  bool
	}{
		{"N-asc", 'N', false},
		{"N-desc", 'N', true},
		{"S-asc", 'S', false},
		{"S-desc", 'S', true},
		{"M-asc", 'M', false},
		{"M-desc", 'M', true},
	}

	orders := guardInputOrders(len(specs))
	require.GreaterOrEqual(t, len(orders), 13, "应至少覆盖 8 随机种子 + 逆序 + 已排序 + 3 轮转")

	for _, cb := range combos {
		t.Run(cb.label, func(t *testing.T) {
			for oi, order := range orders {
				rows := guardBuildRows(guardPermute(specs, order))
				sortRows(rows, cb.col, cb.desc)
				assert.Equalf(t, want[cb.label], guardNames(rows),
					"输入顺序 #%d 下 %s 的排序结果应与期望序列一致（顺序无关性）", oi, cb.label)
			}
		})
	}
}

// TestSortRows_SecondaryKeyAlwaysNameAsc 单元级钉住「次级键恒为名称升序，与主排序方向无关」。
//
// 使用主键**完全相同**的夹具：此时输出序列 100% 由次级键决定，是次级键存在性与方向
// 的最强判据。desc=true 时期望序列仍是名称**升序** —— 这条反直觉的语义是 Spec 明确
// 规定的（次级键在 desc 判断之外），也是任何排序/渲染改动后必须保持的行为。
func TestSortRows_SecondaryKeyAlwaysNameAsc(t *testing.T) {
	specs := guardAllTiesFixture()
	asc := make([]string, 0, len(specs))
	for i := 0; i < len(specs); i++ {
		asc = append(asc, fmt.Sprintf("n%02d", i))
	}
	desc := make([]string, 0, len(asc))
	for i := len(asc) - 1; i >= 0; i-- {
		desc = append(desc, asc[i])
	}

	// 主键全同 → S/M 的 4 种组合都必须输出名称升序（次级键不受 desc 影响）。
	for _, cb := range []struct {
		label string
		col   byte
		desc  bool
		want  []string
	}{
		{"S-asc-allTies", 'S', false, asc},
		{"S-desc-allTies", 'S', true, asc}, // desc 下仍升序：次级键在 desc 分支之外
		{"M-asc-allTies", 'M', false, asc},
		{"M-desc-allTies", 'M', true, asc}, // 同上
		{"N-asc-allTies", 'N', false, asc},
		{"N-desc-allTies", 'N', true, desc}, // 仅 col=N 时 desc 才反转（主键即名称）
	} {
		t.Run(cb.label, func(t *testing.T) {
			for oi, order := range guardInputOrders(len(specs)) {
				rows := guardBuildRows(guardPermute(specs, order))
				sortRows(rows, cb.col, cb.desc)
				assert.Equalf(t, cb.want, guardNames(rows),
					"输入顺序 #%d 下 %s 的次级键行为不符", oi, cb.label)
			}
		})
	}
}

// 此处原有集成级顺序无关性测试（双写入顺序 arm），已实证为**零判别力**并删除：
// APFS 的 readdir 顺序由名字哈希决定、与写入顺序无关，故正序/逆序两个 arm 拿到
// 完全相同的输入序列，退化为单 arm；「删除 sortRows 次级键」变异下它仍 PASS。
// 顺序无关性的承重验证由单元级**合成**置换承担：TestSortRows_InputOrderIndependence
// （13 种置换 × 6 组合）与 TestSortRows_SecondaryKeyAlwaysNameAsc（主键全同夹具），
// 二者均已被该变异证明会 RED。文件系统写入顺序无法构造出有判别力的输入置换。

// ---------------------------------------------------------------------------
// G6 —— 符号链接语义（路径解析与列表渲染的前置不变式 #2）
// ---------------------------------------------------------------------------

// TestSymlink_Semantics 钉住当前 os.Stat 的**跟随符号链接**语义。
//
// 当前实现形态（Stat-first）：handler 以 `os.Stat(absPath)`（autoindex.go:275）作为
// 唯一的路径解析入口，未设 O_NOFOLLOW，故**跟随**符号链接、返回目标的信息；悬空链接
// 在此失败于 ENOENT。本测试的作用是：若后续误引入 Lstat / O_NOFOLLOW / ReadDir 的
// dirent 类型位判目录，这里会立刻 RED。
//
// 注意区分两处**不同**的符号链接处理，它们语义相反且都是正确的：
//   - 直接 URL 访问（handler 里的 os.Stat）→ **跟随**，故 /link-dir/ 列出目标目录内容；
//   - 列表渲染（renderDirListing 里的 e.Info()）→ **lstat 语义**（ReadDir 的 dirent），
//     故 symlink 归 files 组、Size 显示 "-"，且指向目录的链接不带尾斜杠。
func TestSymlink_Semantics(t *testing.T) {
	root := newTestTree(t, map[string]string{
		"target.txt":          "TARGET_CONTENT",
		"targetdir/inner.txt": "INNER",
	})

	// 环境不支持符号链接（如未开启开发者模式的 Windows）时整体跳过，而非静默降级。
	if err := os.Symlink(filepath.Join(root, "target.txt"), filepath.Join(root, "link-file")); err != nil {
		t.Skipf("当前环境不支持符号链接，G6 全部用例跳过: %v", err)
	}
	require.NoError(t, os.Symlink(filepath.Join(root, "targetdir"), filepath.Join(root, "link-dir")))
	require.NoError(t, os.Symlink(filepath.Join(root, "does-not-exist"), filepath.Join(root, "link-dangling")))

	r := newNoRouteRouter(Config{Root: root})

	t.Run("link-to-file", func(t *testing.T) {
		rec := do(r, http.MethodGet, "/link-file", nil)
		assert.Equal(t, http.StatusOK, rec.Code, "指向文件的 symlink 应跟随并返回 200")
		assert.Equal(t, "TARGET_CONTENT", rec.Body.String(), "应返回**目标**内容而非链接路径串")
	})

	t.Run("link-to-dir-with-slash", func(t *testing.T) {
		rec := do(r, http.MethodGet, "/link-dir/", nil)
		require.Equal(t, http.StatusOK, rec.Code, "指向目录的 symlink（带尾斜杠）应 200")
		body := rec.Body.String()
		// 标题必须是**链接路径**（不是 realpath）：跟随发生在文件系统层，URL 语义不变。
		assert.Contains(t, body, "<title>Index of /link-dir/</title>")
		assert.Contains(t, body, ">inner.txt<", "应列出**目标目录**的内容")
		assert.Equal(t, "../", guardParentHref(t, body))
	})

	t.Run("link-to-dir-no-slash", func(t *testing.T) {
		rec := do(r, http.MethodGet, "/link-dir", nil)
		assert.Equal(t, http.StatusMovedPermanently, rec.Code, "目录（含 symlink 目录）缺尾斜杠应 301")
		assert.Equal(t, "/link-dir/", rec.Header().Get("Location"))
	})

	t.Run("dangling-link", func(t *testing.T) {
		rec := do(r, http.MethodGet, "/link-dangling", nil)
		// os.Stat 跟随失败（ENOENT）→ 404：Stat 失败除 ErrPermission（→ 403）外一律
		// 404，ENOTDIR/ELOOP 亦不升级为 500（依据见 autoindex.go:275-289）。
		assert.Equal(t, http.StatusNotFound, rec.Code, "悬空 symlink 应 404（跟随后目标不存在）")
		assert.Equal(t, "[404] not found", rec.Body.String())
	})

	t.Run("listing-presentation", func(t *testing.T) {
		rec := do(r, http.MethodGet, "/", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		body := rec.Body.String()

		// 三种 symlink 都仍以条目形式出现在列表中（列表不跟随，故 dangling 也可见）。
		for _, n := range []string{"link-file", "link-dir", "link-dangling"} {
			assert.Containsf(t, body, ">"+n+"<", "symlink %q 应出现在列表中", n)
			// lstat 语义下 symlink 的 Size() 是目标路径串的字节长度，直接展示属事实性
			// 错误，故统一显示 "-"（列表元数据取自 e.Info()，autoindex.go:389/399）。若改用
			// 跟随语义（os.Stat / f.Stat()）取列表元数据会破坏这条，必须 RED。
			assert.Equalf(t, "-", sizeCellOf(t, body, n), "symlink %q 的 Size 应为 -", n)
		}
		// symlink→目录归 **files** 组：显示名不带尾斜杠（分组用 dirent 类型位，不跟随）。
		assert.NotContains(t, body, ">link-dir/<", "指向目录的 symlink 不应带尾斜杠（不归 dirs 组）")
		// 对照：真实目录带尾斜杠、真实文件显示字节大小。
		assert.Contains(t, body, ">targetdir/<")
		assert.Equal(t, "-", sizeCellOf(t, body, "targetdir/"))
		assert.Equal(t, "14", sizeCellOf(t, body, "target.txt"))

		// 条目总数：target.txt, targetdir/, link-file, link-dir, link-dangling
		assert.Equal(t, 5, strings.Count(body, "<tr><td>"), "根目录数据行数")
	})
}

// ---------------------------------------------------------------------------
// G7 —— 错误状态码映射特征化
// ---------------------------------------------------------------------------

// TestErrorMapping_StatusCodes 逐条钉住当前错误分支的状态码。
//
// 当前形态为 Stat-first：`os.Stat`（autoindex.go:275）是唯一路径解析入口，其后按类型
// 分流到 301（:299-304）/ `os.ReadDir`（:307→:350），或 `os.Open`+ServeContent
// （:318-337）；三处 `errors.Is(err, fs.ErrPermission)` → 403（:285 / :323 / :355）。
// 本测试的用途是让任何状态码变化都**必须是有意识的**：后续 Worker 若改动了这里，必须
// 同时更新断言并在报告中说明（改写依据就地标注于对应用例上方）。
func TestErrorMapping_StatusCodes(t *testing.T) {
	t.Run("not-found", func(t *testing.T) {
		root := newTestTree(t, map[string]string{"exists.txt": "x"})
		r := newNoRouteRouter(Config{Root: root})
		for _, target := range []string{"/nope.txt", "/nope/", "/a/b/c.txt"} {
			rec := do(r, http.MethodGet, target, nil)
			assert.Equalf(t, http.StatusNotFound, rec.Code, "GET %s 应 404", target)
			assert.Equal(t, "[404] not found", rec.Body.String())
		}
	})

	t.Run("traversal-403", func(t *testing.T) {
		// 路径解析不变式：filepath.Join **不先** path.Clean。穿越路径依赖 Join 自身的
		// 归一化让 ".." 逃逸 root，从而被前缀校验捕获为 403；若先 Clean，穿越会被无声
		// 归一回 root 内而只能落到 404。改写路径解析时必须保持这一点。
		root := newTestTree(t, map[string]string{"sub/note.md": "# note\n"})
		var gotReason string
		r := newNoRouteRouter(Config{
			Root: root,
			RejectHandler: func(c *gin.Context, reason string) {
				gotReason = reason
				c.String(http.StatusForbidden, "custom: "+reason)
			},
		})
		variants := []string{
			"/../secret",
			"/../../etc/passwd",
			"/sub/../../secret",
			"/%2e%2e/secret",
			"/..%2fsecret",
		}
		for _, target := range variants {
			gotReason = ""
			rec := do(r, http.MethodGet, target, nil)
			assert.Equalf(t, http.StatusForbidden, rec.Code, "穿越变体 %s 应 403", target)
			assert.Equalf(t, "custom: "+ReasonForbidden, rec.Body.String(), "穿越变体 %s 的 body", target)
			assert.Equalf(t, ReasonForbidden, gotReason, "穿越变体 %s 应以 ReasonForbidden 走 RejectHandler", target)
		}
		assert.Len(t, variants, 5, "穿越编码变体应全覆盖 5 种")
	})

	t.Run("method-not-allowed", func(t *testing.T) {
		// 方法门：非 GET/HEAD → 405 + Allow 头。不得改变方法门位置
		//（顺序不变式：Skipper 判定先于方法门、先于 c.Status(200) 复位）。
		root := newTestTree(t, map[string]string{"hello.txt": "content"}, "sub")
		r := newNoRouteRouter(Config{Root: root})
		for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions, http.MethodPatch} {
			for _, target := range []string{"/", "/sub/", "/hello.txt"} {
				rec := do(r, m, target, nil)
				assert.Equalf(t, http.StatusMethodNotAllowed, rec.Code, "%s %s 应 405", m, target)
				assert.Equalf(t, "GET, HEAD", rec.Header().Get("Allow"), "%s %s 应带 Allow 头", m, target)
				assert.Equal(t, "[405] method not allowed", rec.Body.String())
			}
		}
	})

	t.Run("permission", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows 权限模型不同，无法稳定构造 EACCES")
		}
		if os.Geteuid() == 0 {
			t.Skip("以 root 运行会绕过文件权限检查，无法构造 EACCES")
		}

		root := t.TempDir()
		secretFile := filepath.Join(root, "secret.txt")
		require.NoError(t, os.WriteFile(secretFile, []byte("secret"), 0o000))
		lockedDir := filepath.Join(root, "locked")
		require.NoError(t, os.MkdirAll(lockedDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(lockedDir, "inner.txt"), []byte("x"), 0o644))
		require.NoError(t, os.Chmod(lockedDir, 0o000))
		// t.TempDir 的清理需要能删除 0o000 条目；Cleanup 是 LIFO，本条注册晚于 TempDir，
		// 故先于其 RemoveAll 运行。
		t.Cleanup(func() {
			_ = os.Chmod(secretFile, 0o644)
			_ = os.Chmod(lockedDir, 0o755)
		})

		var gotReason string
		r := newNoRouteRouter(Config{
			Root: root,
			RejectHandler: func(c *gin.Context, reason string) {
				gotReason = reason
				c.String(http.StatusForbidden, "custom: "+reason)
			},
		})

		t.Run("unreadable-file", func(t *testing.T) {
			gotReason = ""
			rec := do(r, http.MethodGet, "/secret.txt", nil)
			assert.Equal(t, http.StatusForbidden, rec.Code, "Open EACCES 应映射 403")
			assert.Equal(t, ReasonPermission, gotReason, "权限拒绝应走 ReasonPermission")
		})

		t.Run("unreadable-dir-with-slash", func(t *testing.T) {
			gotReason = ""
			rec := do(r, http.MethodGet, "/locked/", nil)
			assert.Equal(t, http.StatusForbidden, rec.Code, "ReadDir EACCES 应映射 403")
			assert.Equal(t, ReasonPermission, gotReason)
		})

		t.Run("stat-through-no-x-dir", func(t *testing.T) {
			gotReason = ""
			rec := do(r, http.MethodGet, "/locked/inner.txt", nil)
			assert.Equal(t, http.StatusForbidden, rec.Code, "stat 需父目录 x 权限，EACCES 应映射 403")
			assert.Equal(t, ReasonPermission, gotReason)
		})

		// Stat-first 下 `os.Stat` 只需**父目录的搜索(x)权限**即可成功（root 有），故
		// 0o000 目录无尾斜杠时先命中 301 分支、403 分支永远走不到。这是**有意保留**
		// 的行为，勿改为 Open-first：`os.Open` 需目录自身的 r 权限，会把本用例变成
		// 403；而 darwin/APFS 实测 open()+close() 是 stat() 的 30–60 倍，Open-first
		// 会让 301 路径白开一次目录句柄 → +2 allocs / +11.2% B/op / +2802% ns/op
		// （实测依据见 handler 内 Stat-first 注释）。
		t.Run("unreadable-dir-no-slash", func(t *testing.T) {
			gotReason = ""
			rec := do(r, http.MethodGet, "/locked", nil)
			assert.Equalf(t, http.StatusMovedPermanently, rec.Code,
				"Stat-first：0o000 目录无尾斜杠 → os.Stat 只需父目录搜索权限即成功 → 先给 301")
			assert.Equal(t, "/locked/", rec.Header().Get("Location"))
			assert.Empty(t, gotReason, "301 分支不拒绝，不应经过 RejectHandler")
		})

		t.Run("0o000-entries-still-listed", func(t *testing.T) {
			// 不可读条目仍出现在父目录列表中：os.ReadDir 只需父目录的 r 权限，
			// 条目自身的权限不影响其 dirent 可见性。不得改变这一点。
			rec := do(r, http.MethodGet, "/", nil)
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Contains(t, rec.Body.String(), ">secret.txt<")
			assert.Contains(t, rec.Body.String(), ">locked/<")
		})
	})
}

// ---------------------------------------------------------------------------
// G9 —— 数值格式化面守护（mtime 年份展开 / 手工长度常量）
//
// 此前护栏预算集中在 HTML 注入面（转义/href），手写数值展开代码零覆盖：全部
// 时间夹具都是 time.Date(2026, …)，年份十位展开曾存在 byte(y/10)%10 形态的
// 真实缺陷（byte 转换先做 mod-256 截断、后取模，y ≥ 2560 时十位数字错误，
// [0,9999] 内 7440 个年份与 Format 失配，如 2560→"2500"）。本节把数值格式化
// 钉到与 HTML 面同等的强度。
// ---------------------------------------------------------------------------

// TestRender_MtimeYearExhaustive 穷举 [0,9999] 全部年份，钉住 appendMtime 输出与
// time.Format("2006-01-02 15:04") 逐字节一致（stdLongYear 语义，含前导零）。
// 穷举而非抽样：年份十位缺陷只在 y/10 > 255（即 y ≥ 2560）后显现，抽样夹具
// （2026 等现代年份）结构上不可能命中。
func TestRender_MtimeYearExhaustive(t *testing.T) {
	mismatches := 0
	var examples []string
	for y := 0; y <= 9999; y++ {
		tm := time.Date(y, 12, 31, 23, 59, 0, 0, time.UTC)
		want := tm.Format("2006-01-02 15:04")
		if got := string(appendMtime(nil, tm)); got != want {
			mismatches++
			if len(examples) < 5 {
				examples = append(examples, fmt.Sprintf("%d→%q(want %q)", y, got, want))
			}
		}
	}
	assert.Zerof(t, mismatches,
		"appendMtime 与 Format(\"2006-01-02 15:04\") 在 [0,9999] 内有 %d 个年份失配，反例: %v",
		mismatches, examples)
}

// TestRender_MtimeYearOutOfRange 钉住 appendMtime 年份越出 [0,9999] 时回退分支
// （strconv.AppendInt）的**实际行为**（characterization test：断言当前行为而非
// 「正确」行为），使该生产不可达、但可经直接调用抵达的分支由未测变为已测。
// 实测事实（与 appendMtime 注释一致）：
//   - >4 位正年与 Format("2006-01-02 15:04") 逐字节一致——曾有注释断言「Format 对
//     >4 位年份产出错误数字」，经实测证伪；本测试把证伪结果固化为回归护栏，防止
//     同类错误论断再次出现；
//   - 唯一分歧域是位数不足 4 的负年（0 > y > -1000）：Format 的 appendInt(x,4) 在
//     负号后补零至宽 4（y=-5 → "-0005"），本实现不补零（→ "-5"）。文件系统 mtime
//     早于公元 1 年在生产不可达，且该分歧仅影响显示、不影响排序（排序用原始
//     sortMtime），故有意不对齐 Format（避免引入新的未测路径），仅钉住现行为。
func TestRender_MtimeYearOutOfRange(t *testing.T) {
	at := func(y int) time.Time { return time.Date(y, 3, 4, 5, 6, 0, 0, time.UTC) }

	// 分歧域（0 > y > -1000，-999 为边界）：钉住不补零的实际输出，并同时钉住
	// 「与 Format 分歧」这一事实本身。
	for _, tt := range []struct {
		y    int
		want string
	}{
		{-1, "-1-03-04 05:06"},
		{-5, "-5-03-04 05:06"},
		{-999, "-999-03-04 05:06"},
	} {
		tm := at(tt.y)
		got := string(appendMtime(nil, tm))
		assert.Equalf(t, tt.want, got, "负年 y=%d 回退分支应保持不补零的实际输出", tt.y)
		assert.NotEqualf(t, tm.Format("2006-01-02 15:04"), got,
			"y=%d 应位于与 Format 的分歧域（Format 在负号后补零至宽 4）", tt.y)
	}

	// 分歧域之外与 Format 逐字节一致：>4 位正年全一致（钉住被证伪论断的反例），
	// |y| ≥ 1000 的负年因位数已达 4、补零无操作，同样一致。
	for _, y := range []int{-100000, -12345, -9999, -1000, 10000, 12345, 99999, 100000, 275760} {
		tm := at(y)
		assert.Equalf(t, tm.Format("2006-01-02 15:04"), string(appendMtime(nil, tm)),
			"y=%d 应与 Format 逐字节一致", y)
	}
}

// TestRender_MtimeFieldBoundaries 钉住 append2Digits 的 [0,99] 契约：月/日/时/分
// 各维度的边界值（0、1、9、10、维度最大值）经 appendMtime 输出后必须与 Format
// 逐字节一致。年份边界另由穷举测试覆盖，此处只取少量代表年以控制用例规模。
func TestRender_MtimeFieldBoundaries(t *testing.T) {
	daysIn := func(y, m int) int {
		return time.Date(y, time.Month(m)+1, 0, 0, 0, 0, 0, time.UTC).Day()
	}
	mismatches := 0
	var examples []string
	for _, y := range []int{0, 1, 9, 10, 99, 100, 2559, 2560, 2561, 3000, 9999} {
		for m := 1; m <= 12; m++ {
			for d := 1; d <= daysIn(y, m); d++ {
				for _, hh := range []int{0, 1, 9, 10, 23} {
					for _, mm := range []int{0, 1, 9, 10, 59} {
						tm := time.Date(y, time.Month(m), d, hh, mm, 0, 0, time.UTC)
						want := tm.Format("2006-01-02 15:04")
						if got := string(appendMtime(nil, tm)); got != want {
							mismatches++
							if len(examples) < 5 {
								examples = append(examples,
									fmt.Sprintf("%04d-%02d-%02d %02d:%02d→%q(want %q)", y, m, d, hh, mm, got, want))
							}
						}
					}
				}
			}
		}
	}
	assert.Zerof(t, mismatches, "月/日/时/分边界有 %d 处与 Format 失配，反例: %v", mismatches, examples)
}

// TestRender_FixedWidthContracts 钉住 listingRowFixedLen 常量表达式中两个手写字面量
// 所依赖的长度事实（该常量仅用作 make 的容量提示，失配无正确性后果、但会让
// 「不触发扩容」的预分配目的静默落空，故必须有测试看守）：
//
//	① mtime 恒 16 字节 —— 常量式中以 len("2006-01-02 15:04") 推导；
//	   契约域为 appendMtime 直接展开的年份区间 [0,9999]（区间外走变长回退分支）。
//	② Size ≤ 8 字节 —— 可达域 n ≥ 0（info.Size() 非负）内的宽松上界，
//	   最长真实形态 "1023.9K" = 7。
func TestRender_FixedWidthContracts(t *testing.T) {
	t.Run("mtime-16-bytes", func(t *testing.T) {
		for _, tm := range []time.Time{
			time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 10, 8, 12, 30, 0, 0, time.UTC),
			time.Date(2560, 6, 15, 9, 5, 0, 0, time.UTC),
			time.Date(9999, 12, 31, 23, 59, 0, 0, time.UTC),
		} {
			assert.Equalf(t, 16, len(appendMtime(nil, tm)),
				"appendMtime 输出必须恒 16 字节（listingRowFixedLen 依赖此宽度）: %v", tm)
		}
	})

	t.Run("size-at-most-8-bytes", func(t *testing.T) {
		for _, n := range []int64{
			0, 1, 999, 1023, 1024, 1536,
			1<<20 - 1, 1048575, 1048576,
			1 << 30, 1 << 40, 1 << 50, 1 << 60,
			math.MaxInt64, // → "8192E"（5 字节），全 int64 非负域的最长输出档位
		} {
			assert.LessOrEqualf(t, len(appendHumanSize(nil, n)), 8,
				"appendHumanSize 输出必须 ≤8 字节（listingRowFixedLen 依赖此上界）: n=%d", n)
		}
	})

	t.Run("negative-size-unreachable", func(t *testing.T) {
		// n < 0 在生产路径不可达（info.Size() 非负），不受 8 字节上界约束：
		// 负数走 strconv.AppendInt 原样输出全部位数。此处仅钉住兜底行为本身。
		assert.Equal(t, "-9223372036854775808", string(appendHumanSize(nil, math.MinInt64)))
	})
}
