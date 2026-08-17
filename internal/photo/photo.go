// Package photo は写真ファイルの分類を担う。判定は拡張子のみに基づき、
// ファイルの中身は読まない（fsnotifyの大量イベントを軽く捌くため）。
package photo

import (
	"path/filepath"
	"strings"
)

// Kind は写真ファイルの扱い方を表す。
type Kind int

const (
	// KindUnsupported はインデックス対象外のファイル。
	KindUnsupported Kind = iota
	// KindRaster はGoでデコードでき、サムネイルを生成するファイル。
	KindRaster
	// KindOpaque はインデックスはするがデコードせず、原本をそのまま配信するファイル。
	// HEIC/HEIFが該当する。Safari以外では表示できないが、設計上それを許容している。
	KindOpaque
)

var extKinds = map[string]Kind{
	".jpg":  KindRaster,
	".jpeg": KindRaster,
	".png":  KindRaster,
	".gif":  KindRaster,
	".webp": KindRaster,
	".heic": KindOpaque,
	".heif": KindOpaque,
}

var extTypes = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
	".heic": "image/heic",
	".heif": "image/heif",
}

func ext(name string) string { return strings.ToLower(filepath.Ext(name)) }

// KindOf はファイル名の拡張子から扱い方を判定する。
func KindOf(name string) Kind { return extKinds[ext(name)] }

// IsSupported はインデックス対象にすべきファイルかを報告する。
func IsSupported(name string) bool { return KindOf(name) != KindUnsupported }

// ContentType は原本配信時に使うMIMEタイプを返す。
// HEIC/HEIFはGoの mime パッケージが知らないため自前で持つ。
func ContentType(name string) string {
	if t, ok := extTypes[ext(name)]; ok {
		return t
	}
	return "application/octet-stream"
}
