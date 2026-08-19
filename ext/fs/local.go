/*
Local 是 FileSystem 的本地磁盘实现，所有路径被限制在 Root 内
（类似 chroot），路径穿越（如 ../）会被拒绝。
*/
package fs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

/* Local 以 Root 为沙箱根目录。 */
type Local struct{ Root string }

var _ FileSystem = Local{}

/* NewLocal 创建以 root 为边界的本地文件系统。 */
func NewLocal(root string) Local { return Local{Root: root} }

func (l Local) resolve(path string) (string, error) {
	cleaned := filepath.Clean(filepath.FromSlash(path))
	abs, err := filepath.Abs(filepath.Join(l.Root, cleaned))
	if err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(l.Root)
	if err != nil {
		return "", err
	}
	if abs != rootAbs && !strings.HasPrefix(abs, rootAbs+string(filepath.Separator)) {
		return "", fmt.Errorf("fs: path %q escapes root", path)
	}
	return abs, nil
}

func (l Local) Read(_ context.Context, path string) ([]byte, error) {
	p, err := l.resolve(path)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

func (l Local) Write(_ context.Context, path string, data []byte) error {
	p, err := l.resolve(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

func (l Local) List(_ context.Context, dir string) ([]Entry, error) {
	p, err := l.resolve(dir)
	if err != nil {
		return nil, err
	}
	items, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(items))
	for _, it := range items {
		info, ierr := it.Info()
		if ierr != nil {
			continue
		}
		out = append(out, Entry{Name: it.Name(), Size: info.Size(), IsDir: it.IsDir()})
	}
	return out, nil
}

/* Edit 查找替换：全部命中都替换，返回次数。 */
func (l Local) Edit(ctx context.Context, path, oldText, newText string) (int, error) {
	data, err := l.Read(ctx, path)
	if err != nil {
		return 0, err
	}
	if oldText == "" {
		return 0, fmt.Errorf("fs: empty old_text")
	}
	n := strings.Count(string(data), oldText)
	if n == 0 {
		return 0, fmt.Errorf("fs: old_text not found in %s", path)
	}
	return n, l.Write(ctx, path, []byte(strings.ReplaceAll(string(data), oldText, newText)))
}
