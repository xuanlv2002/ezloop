/*
grep 工具：Go 原生递归搜索文件内容。不走 shell 的 grep/findstr——
Windows findstr 对 UTF-8 中文不可靠，也没有跨平台的跳过目录约定。
*/
package filetools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/xuanlv2002/ezloop/types"
)

const (
	grepMaxBytes     = 2 << 20 // 单文件上限：大产物/日志不扫，避免一次调用卡死
	grepDefaultLimit = 200
	grepMaxLimit     = 1000
)

/* grepSkipDirs 是递归时固定跳过的目录：仓库元数据、依赖与产物。 */
var grepSkipDirs = map[string]bool{".git": true, "node_modules": true, "dist": true, "build": true, ".next": true}

type grepArgs struct {
	Pattern      string `json:"pattern" desc:"要搜索的正则；fixed_string 时按字面量处理"`
	Path         string `json:"path,omitempty" desc:"搜索目录（缺省为工作目录）；可传绝对路径或单个文件"`
	Include      string `json:"include,omitempty" desc:"文件名过滤，逗号分隔 glob，如 *.ts,*.tsx；缺省搜索全部文件"`
	IgnoreCase   bool   `json:"ignore_case,omitempty" desc:"忽略大小写"`
	FixedString  bool   `json:"fixed_string,omitempty" desc:"pattern 按字面量匹配，免去正则转义"`
	ContextLines int    `json:"context_lines,omitempty" desc:"每条命中附带前后 N 行上下文，默认 0"`
	MaxResults   int    `json:"max_results,omitempty" desc:"命中上限，默认 200，最多 1000"`
}

func grepTool(h *Hook) types.Tool {
	return types.NewTool("grep",
		"在目录内递归搜索文件内容（跨平台，不依赖 findstr/grep）。返回 path:line: 内容（上下文行是 path-line- 内容），行号可用于 read_file 的 offset 定位或 #L 锚点引用。"+
			"自动跳过 .git/node_modules/dist/build/.next、二进制、非 UTF-8 与超过 2MB 的文件（摘要会报跳过数）；include 按文件名 glob 过滤（逗号分隔）。只读",
		func(ctx context.Context, in *grepArgs) (string, error) {
			if in.Pattern == "" {
				return "", errors.New("pattern is required")
			}
			pattern := in.Pattern
			if in.FixedString {
				pattern = regexp.QuoteMeta(pattern)
			}
			if in.IgnoreCase {
				pattern = "(?i)" + pattern
			}
			re, err := regexp.Compile(pattern)
			if err != nil {
				return "", fmt.Errorf("pattern: %w", err)
			}
			root := strings.TrimSpace(in.Path)
			if root == "" {
				root = h.workDir
			}
			if root == "" {
				root = "."
			}
			limit := in.MaxResults
			if limit <= 0 {
				limit = grepDefaultLimit
			}
			if limit > grepMaxLimit {
				limit = grepMaxLimit
			}
			ctxN := max(in.ContextLines, 0)
			var include []string
			for _, s := range strings.Split(in.Include, ",") {
				if s = strings.TrimSpace(s); s != "" {
					include = append(include, s)
				}
			}

			var out []string
			hits, files, scanned, skipped := 0, 0, 0, 0
			stop := false

			scan := func(file string, data []byte) {
				if len(data) > grepMaxBytes || bytes.IndexByte(data[:min(len(data), 8192)], 0) >= 0 || !utf8.Valid(data) {
					skipped++
					return
				}
				scanned++
				lines := strings.Split(string(data), "\n")
				// 先收集命中行再输出区间：相邻命中的重叠上下文只打印一次，
				// 且命中行始终带 ":"（不能因为先作为上下文出现就降级成 "-"）。
				var matched []int
				for i := range lines {
					if re.MatchString(strings.TrimSuffix(lines[i], "\r")) {
						matched = append(matched, i)
						hits++
						if hits >= limit {
							stop = true
							break
						}
					}
				}
				if len(matched) == 0 {
					return
				}
				files++
				isHit := make(map[int]bool, len(matched))
				for _, i := range matched {
					isHit[i] = true
				}
				last := -1
				for _, i := range matched {
					start := max(i-ctxN, 0)
					end := min(i+ctxN, len(lines)-1)
					if last >= 0 && start > last+1 {
						out = append(out, "--")
					}
					for j := start; j <= end; j++ {
						if j <= last {
							continue
						}
						text := strings.TrimSuffix(lines[j], "\r")
						if isHit[j] {
							out = append(out, fmt.Sprintf("%s:%d: %s", file, j+1, text))
						} else {
							out = append(out, fmt.Sprintf("%s-%d- %s", file, j+1, text))
						}
					}
					if end > last {
						last = end
					}
				}
			}

			var walk func(dir string)
			walk = func(dir string) {
				entries, err := h.fsys.List(ctx, dir)
				if err != nil {
					return
				}
				for _, e := range entries {
					if stop || ctx.Err() != nil {
						return
					}
					p := path.Join(dir, e.Name)
					if e.IsDir {
						if !grepSkipDirs[e.Name] {
							walk(p)
						}
						continue
					}
					if len(include) > 0 {
						ok := false
						for _, pat := range include {
							if m, _ := path.Match(pat, e.Name); m {
								ok = true
								break
							}
						}
						if !ok {
							continue
						}
					}
					if e.Size > grepMaxBytes {
						skipped++
						continue
					}
					data, err := h.fsys.Read(ctx, p)
					if err != nil {
						continue
					}
					scan(p, data)
				}
			}

			if _, err := h.fsys.List(ctx, root); err != nil {
				// path 指向单个文件：退化为只搜这个文件
				data, rerr := h.fsys.Read(ctx, root)
				if rerr != nil {
					return "", err
				}
				scan(root, data)
			} else {
				walk(root)
			}

			skipNote := ""
			if skipped > 0 {
				skipNote = fmt.Sprintf("；跳过 %d 个非 UTF-8/二进制/超 2MB 文件", skipped)
			}
			if hits == 0 {
				return fmt.Sprintf("未找到匹配（搜索了 %d 个文件%s）", scanned, skipNote), nil
			}
			summary := fmt.Sprintf("[命中 %d 处 / %d 个文件，搜索 %d 个文件%s]", hits, files, scanned, skipNote)
			if stop {
				summary = fmt.Sprintf("[已达 %d 条上限，可能还有更多；以上 %d 处 / %d 个文件%s]", limit, hits, files, skipNote)
			}
			return strings.Join(out, "\n") + "\n\n" + summary, nil
		})
}
