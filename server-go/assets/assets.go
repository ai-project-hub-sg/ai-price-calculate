// assets 嵌入前端静态文件。public/ 内容由 tools/syncassets 在构建时
// 从仓库根 ../public 同步（embed 不允许 ../ 路径，故走拷贝）。
package assets

import "embed"

//go:embed all:public
var Public embed.FS
