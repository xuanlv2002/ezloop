/*
binary.go 是 read_file 的二进制兜底：特殊类型文件（可执行/压缩包/
办公文档/音视频等）文本化进上下文是乱码且体量巨大，直接拒读。
*/
package filetools

import (
	"bytes"
	"path/filepath"
	"strings"
)

/* binaryKind 判定文件是否二进制：常见容器魔数优先，NUL 启发与扩展名兜底。 */
func binaryKind(path string, data []byte) (string, bool) {
	if len(data) == 0 {
		return "", false
	}
	kind := ""
	switch {
	case bytes.HasPrefix(data, []byte("%PDF-")):
		kind = "pdf"
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		kind = "zip 容器"
	case bytes.HasPrefix(data, []byte("\xd0\xcf\x11\xe0")):
		kind = "OLE 容器"
	case bytes.HasPrefix(data, []byte("MZ")):
		kind = "PE 可执行"
	case bytes.HasPrefix(data, []byte("\x7fELF")):
		kind = "ELF 可执行"
	case bytes.HasPrefix(data, []byte("\x1f\x8b")):
		kind = "gzip"
	case bytes.HasPrefix(data, []byte("Rar!\x1a\x07")):
		kind = "rar"
	case bytes.HasPrefix(data, []byte("7z\xbc\xaf\x27\x1c")):
		kind = "7z"
	}
	if kind != "" {
		return kind, true
	}
	// NUL 启发（git 同款）：文本文件前段不该出现 NUL 字节
	for _, b := range data[:min(len(data), 8000)] {
		if b == 0x00 {
			return "未知二进制", true
		}
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf", ".exe", ".dll", ".zip", ".rar", ".7z", ".gz", ".xz", ".bz2",
		".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".jar", ".class",
		".so", ".dylib", ".o", ".a", ".bin", ".dat", ".ttf", ".otf",
		".woff", ".woff2", ".eot", ".mp3", ".wav", ".flac", ".aac", ".ogg",
		".mp4", ".avi", ".mkv", ".mov", ".wmv", ".flv", ".pyc", ".sqlite", ".db",
		".bmp", ".ico", ".tar":
		return "二进制", true
	}
	return "", false
}
