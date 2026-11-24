// syncassets 把仓库根 public/ 拷入 assets/public 供 embed。
// 用法（server-go 目录下）: go run ./tools/syncassets && go build ./cmd/server
package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func main() {
	root, err := filepath.Abs(filepath.Join(".."))
	if err != nil {
		panic(err)
	}
	src := filepath.Join(root, "public")
	dst := filepath.Join("assets", "public")
	if st, err := os.Stat(src); err != nil || !st.IsDir() {
		panic("找不到 ../public（请在 server-go 目录下运行）")
	}
	// 清掉旧产物；重建 .gitkeep 使裸 clone（无同步产物）也能编译
	if err := os.RemoveAll(dst); err != nil {
		panic(err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(dst, ".gitkeep"), nil, 0o644); err != nil {
		panic(err)
	}
	n := 0
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		to := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(to, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.WriteFile(to, b, 0o644); err != nil {
			return err
		}
		n++
		return nil
	})
	if err != nil {
		panic(err)
	}
	fmt.Printf("已同步 %d 个文件 → %s\n", n, dst)
}
