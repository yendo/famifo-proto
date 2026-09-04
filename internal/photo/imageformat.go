package photo

import (
	"path/filepath"
	"strings"
)

// format は対応する拡張子1つ分の扱い。
type format struct {
	mime      string // 原本を配信するときのMIMEタイプ
	decodable bool   // famifoが自分でデコードしてサムネイルを作れるか
}

// supportedExts はインデックス対象にする拡張子。ここに無い拡張子は対象外である。
// 対応形式を増やすのはこの表に1行足すことであり、分類とMIMEが片方だけ増える
// ことがないよう1つにまとめてある。
//
// HEIC/HEIFが decodable=false なのは、載せるが自前ではデコードしないため。原本は
// Safari以外では表示できないので、@eaDir から借りられる場合に限り、配信時に
// SynologyのJPEGへ差し替える（FullPath）。
var supportedExts = map[string]format{
	".jpg":  {"image/jpeg", true},
	".jpeg": {"image/jpeg", true},
	".png":  {"image/png", true},
	".gif":  {"image/gif", true},
	".webp": {"image/webp", true},
	".heic": {"image/heic", false},
	".heif": {"image/heif", false},
}

func ext(name string) string { return strings.ToLower(filepath.Ext(name)) }

// IsSupportedFile はインデックス対象にすべきファイルかを報告する。
// 判定は拡張子だけに基づくので、ベース名でもフルパスでも渡せる。
func IsSupportedFile(name string) bool {
	_, ok := supportedExts[ext(name)]
	return ok
}

// IsDecodableFile は famifo が自分でサムネイルを作れるファイルかを報告する。
// 偽のときサムネイルは @eaDir から借りるしかなく、借りられなければ一覧には
// 原本が出る（HEIC/HEIFが該当する）。対象外のファイルも偽になる。
func IsDecodableFile(name string) bool { return supportedExts[ext(name)].decodable }
