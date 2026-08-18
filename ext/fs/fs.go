/*
Package fs 定义 ext 层共享的文件系统抽象：
offload 卸载、filetools 工具、skill 加载等中间件均依赖此接口，
便于注入内存 FS（测试）或受限 Local FS（生产）。
*/
package fs

import "context"

/* Entry 是目录项。 */
type Entry struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	IsDir bool   `json:"is_dir"`
}

/*
FileSystem 是最小文件系统接口。路径使用正斜杠风格，
由实现负责与具体平台路径互转及安全约束。
*/
type FileSystem interface {
	Read(ctx context.Context, path string) ([]byte, error)
	Write(ctx context.Context, path string, data []byte) error
	List(ctx context.Context, dir string) ([]Entry, error)
}

/* PatchOp 是一次查找替换修改。 */
type PatchOp struct {
	Path    string `json:"path"`
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

/*
Modifier 是可编辑文件系统：原子查找替换与多文件补丁。
实现此接口的文件系统会解锁 edit_file / apply_patch 工具。
*/
type Modifier interface {
	FileSystem
	// Edit 在单个文件内做查找替换，返回替换次数；oldText 不存在时返回错误
	//（防止调用方误以为修改成功）。
	Edit(ctx context.Context, path, oldText, newText string) (int, error)
	// ApplyPatch 原子应用多文件修改：任一 op 验证失败则全部不生效。
	ApplyPatch(ctx context.Context, ops []PatchOp) error
}

/* GrepRequest 是内容搜索请求。 */
type GrepRequest struct {
	// Pattern 是正则表达式。
	Pattern string
	// Path 是搜索起点（文件或目录）。
	Path string
	// Glob 是文件名过滤（如 "*.go"），空为不过滤。
	Glob string
}

/* GrepMatch 是一条内容命中。 */
type GrepMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

/* Searcher 是可搜索文件系统：解锁 grep / find 工具。 */
type Searcher interface {
	FileSystem
	// Grep 按正则搜索文件内容。
	Grep(ctx context.Context, req GrepRequest) ([]GrepMatch, error)
	// Find 在 root 下按文件名 glob 模式查找，返回匹配路径。
	Find(ctx context.Context, root, pattern string) ([]string, error)
}
