// autoindex 示例：Apache mod_autoindex 风格目录列表 + 静态文件下载（直接使用 gin，不依赖 orbit）。
//
// autoindex 是终端内容生成器（自行产出 body、不调用 c.Next()），必须以 NoRoute 兜底挂载
// （orbit 下为 engine.GetGinEngine().NoRoute(...)），勿放进 Use 链；
// 与 compression/timeout/security 的冲突规避见 README「AutoIndex」小节。
//
// 运行: go run ./autoindex
// 测试:
//
//	curl -i http://localhost:8080/                     # 目录列表（目录在前、文件在后）
//	curl -i http://localhost:8080/docs/                # 子目录列表（含 Parent Directory 行）
//	curl -i http://localhost:8080/hello.txt            # 文件下载（200）
//	curl -i -r 0-99 http://localhost:8080/big.bin      # Range 断点续传（206 + Content-Range）
//	curl -i "http://localhost:8080/?C=S;O=D"           # 按大小降序（C=N|M|S, O=A|D）
//	curl -i http://localhost:8080/.git/config          # IndexIgnore 只隐藏列表，直接访问仍可用
package main

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"
	"github.com/shengyanli1982/orbit-middlewares/middleware/autoindex"
)

// setupDemoTree 在演示根目录下生成目录树：子目录、若干文件、一个大文件（Range 演示）、
// 一个被 IndexIgnore 隐藏的 .git 目录。
func setupDemoTree(root string) {
	must := func(err error) {
		if err != nil {
			log.Fatalf("setup demo tree failed: %v", err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "docs"), 0o755))
	must(os.WriteFile(filepath.Join(root, "hello.txt"), []byte("Hello, autoindex!\n"), 0o644))
	must(os.WriteFile(filepath.Join(root, "docs", "guide.md"), bytes.Repeat([]byte("# guide\n"), 64), 0o644))
	// 64KB 大文件，用于演示 Range 断点续传（curl -r 0-99 → 206）
	must(os.WriteFile(filepath.Join(root, "big.bin"), bytes.Repeat([]byte("0123456789abcdef"), 4096), 0o644))
	// .git 被 IndexIgnore 从列表隐藏，但直接 URL 访问仍可用——IndexIgnore 不是访问控制
	must(os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	must(os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[core]\n"), 0o644))
}

func main() {
	gin.SetMode(gin.ReleaseMode)

	const root = "./public"
	setupDemoTree(root)

	r := gin.New()

	// 终端 handler：以 NoRoute 兜底挂载（勿 r.Use）。
	// Root 必填（构造期解析为绝对路径，为空/不存在/非目录会 panic）；
	// IndexIgnore: glob 模式（path.Match 语义），仅从目录列表隐藏条目。
	r.NoRoute(autoindex.New(autoindex.Config{
		Root:        root,
		IndexIgnore: []string{".git"},
	}))

	fmt.Println("AutoIndex example server running on :8080")
	fmt.Println("Test with:")
	fmt.Println("  curl -i http://localhost:8080/                  # directory listing")
	fmt.Println("  curl -i http://localhost:8080/hello.txt         # file download")
	fmt.Println("  curl -i -r 0-99 http://localhost:8080/big.bin   # Range download (206)")
	fmt.Println("  curl -i 'http://localhost:8080/?C=S;O=D'        # sort by size descending")

	if err := r.Run(":8080"); err != nil {
		fmt.Printf("Server error: %v\n", err)
	}
}
