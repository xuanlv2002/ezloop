package fs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

var _ Modifier = Local{}
var _ Searcher = Local{}

var (
	errFileTooLarge = errors.New("file too large to scan")
	errBinaryFile   = errors.New("binary file")
)

const (
	// grep/find 结果上限，防止超大仓库撑爆上下文。
	MaxGrepResults = 500
	MaxFindResults = 500
)

/* Edit 单文件查找替换：全部命中都替换，返回次数。 */
func (l Local) Edit(ctx context.Context, p, oldText, newText string) (int, error) {
	data, err := l.Read(ctx, p)
	if err != nil {
		return 0, err
	}
	if oldText == "" {
		return 0, fmt.Errorf("fs: empty old_text")
	}
	n := strings.Count(string(data), oldText)
	if n == 0 {
		return 0, fmt.Errorf("fs: old_text not found in %s", p)
	}
	replaced := strings.ReplaceAll(string(data), oldText, newText)
	return n, l.Write(ctx, p, []byte(replaced))
}

/*
ApplyPatch 原子多文件修改：先全量预检（可读 + old 命中），
任一失败则什么都不写；应用过程中若发生错误，回滚已应用的文件。
*/
func (l Local) ApplyPatch(ctx context.Context, ops []PatchOp) error {
	if len(ops) == 0 {
		return fmt.Errorf("fs: empty patch")
	}
	originals := make([][]byte, len(ops))
	for i, op := range ops {
		if op.Path == "" || op.OldText == "" {
			return fmt.Errorf("fs: op[%d] requires path and old_text", i)
		}
		data, err := l.Read(ctx, op.Path)
		if err != nil {
			return fmt.Errorf("fs: patch op[%d] %s: %w", i, op.Path, err)
		}
		if !bytes.Contains(data, []byte(op.OldText)) {
			return fmt.Errorf("fs: patch op[%d] %s: old_text not found", i, op.Path)
		}
		originals[i] = data
	}
	// 应用（预检已保证必然命中，此阶段错误属 IO 异常）。
	for i, op := range ops {
		replaced := strings.ReplaceAll(string(originals[i]), op.OldText, op.NewText)
		if err := l.Write(ctx, op.Path, []byte(replaced)); err != nil {
			// 回滚已应用部分。
			for j := 0; j < i; j++ {
				_ = l.Write(ctx, ops[j].Path, originals[j])
			}
			return fmt.Errorf("fs: patch apply %s: %w (rolled back)", op.Path, err)
		}
	}
	return nil
}

/* Grep 按正则搜索文件内容。 */
func (l Local) Grep(ctx context.Context, req GrepRequest) ([]GrepMatch, error) {
	if req.Pattern == "" {
		return nil, fmt.Errorf("fs: empty pattern")
	}
	re, err := regexp.Compile(req.Pattern)
	if err != nil {
		return nil, fmt.Errorf("fs: bad pattern: %w", err)
	}
	var matches []GrepMatch
	err = l.walk(ctx, req.Path, func(rel string, data []byte) bool {
		if req.Glob != "" && !globMatch(req.Glob, path.Base(rel)) {
			return true
		}
		for lineNo, line := range strings.Split(string(data), "\n") {
			if len(matches) >= MaxGrepResults {
				return false
			}
			if re.MatchString(line) {
				matches = append(matches, GrepMatch{
					Path: rel, Line: lineNo + 1,
					Text: strings.TrimRight(line, "\r"),
				})
			}
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	return matches, nil
}

/* Find 按文件名 glob 模式查找。 */
func (l Local) Find(ctx context.Context, root, pattern string) ([]string, error) {
	if pattern == "" {
		return nil, fmt.Errorf("fs: empty pattern")
	}
	var found []string
	err := l.walk(ctx, root, func(rel string, _ []byte) bool {
		if len(found) >= MaxFindResults {
			return false
		}
		if globMatch(pattern, path.Base(rel)) {
			found = append(found, rel)
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

/*
walk 遍历 root（文件或目录），对每个文件以 FS 相对路径调用 fn；
fn 返回 false 时停止。自动跳过二进制文件与常见噪声目录。
*/
func (l Local) walk(ctx context.Context, root string, fn func(rel string, data []byte) bool) error {
	abs, err := l.resolve(root)
	if err != nil {
		return err
	}
	skipDirs := map[string]bool{
		".git": true, "node_modules": true, ".idea": true, "vendor": true, ".ezloop": true,
	}
	stop := false
	err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, werr error) error {
		if stop || werr != nil {
			return werr
		}
		rel, rerr := filepath.Rel(l.Root, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, derr := readFileLimited(p)
		if derr != nil {
			return nil // 不可读/超大文件跳过
		}
		if !fn(rel, data) {
			stop = true
		}
		return nil
	})
	if err != nil && stop {
		return nil // 因上限停止不算错误
	}
	return err
}

func globMatch(pattern, name string) bool {
	ok, err := path.Match(pattern, name)
	return err == nil && ok
}

/*
readFileLimited 读取文件内容，超大文件（>2MB）或疑似二进制（含 NUL 字节）
返回错误，由调用方跳过。
*/
func readFileLimited(p string) ([]byte, error) {
	info, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if info.Size() > 2<<20 {
		return nil, errFileTooLarge
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return nil, errBinaryFile
	}
	return data, nil
}
