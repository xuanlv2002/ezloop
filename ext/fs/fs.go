/*
Package fs 定义 ext 层共享的最小文件系统接口：filetools、offload、
skill、localsession 等中间件依赖此抽象，便于注入内存 FS（测试）
或本地实现（生产）。一个接口，四个方法，仅此而已。
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
FileSystem 是唯一的文件系统接口。路径使用正斜杠风格，
由实现负责与具体平台路径互转及安全约束。
*/
type FileSystem interface {
	Read(ctx context.Context, path string) ([]byte, error)
	Write(ctx context.Context, path string, data []byte) error
	List(ctx context.Context, dir string) ([]Entry, error)
	// Edit 在单个文件内做查找替换（全部命中都替换），返回替换次数；
	// oldText 不存在时返回错误（防止调用方误以为修改成功）。
	Edit(ctx context.Context, path, oldText, newText string) (int, error)
}
