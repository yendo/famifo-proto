// Package imagefmt は famifo が扱う形式の表を持つ。
//
// 答えるのは拡張子だけで決まる問いに限られ、ファイルの中身は読まない
// （fsnotifyの大量イベントを軽く捌くため）。I/Oは一切行わない。
//
// 「インデックスに載せるか」（index の問い）と「自前でサムネイルを作れるか」
// （thumb の問い）と「動画か」（配信と画面の問い）は別の問いだが、対応形式を
// 増やすときに片方だけ増えることがないよう、1つの表にまとめてある。
//
// 「ブラウザが表示・再生できるか」は表に入れていない。HEVCを再生できるかは端末に
// ハードウェアデコーダがあるかで決まり、サーバには答えられないためである。その問いは
// thumb が「借りられる変換版があればそれを配る」に置き換えている。
package imagefmt

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
// 対応形式を増やすのはこの表に1行足すことである。
//
// HEIC/HEIFが decodable=false なのは、載せるが自前ではデコードしないため。原本は
// Safari以外では表示できないので、@eaDir から借りられる場合に限り、配信側が
// SynologyのJPEGへ差し替える。
var supportedExts = map[string]format{
	".jpg":  {"image/jpeg", true},
	".jpeg": {"image/jpeg", true},
	".png":  {"image/png", true},
	".gif":  {"image/gif", true},
	".webp": {"image/webp", true},
	".heic": {"image/heic", false},
	".heif": {"image/heif", false},
	// 動画は decodable=false。cgo無しでは1フレームも取り出せないので、一覧の絵は
	// @eaDir から借りるしかなく、借りられなければ配信側がプレースホルダに落とす。
	// 拡張子を2つに絞っているのは、実データがこの2つだからである。
	".mp4": {"video/mp4", false},
	".mov": {"video/quicktime", false},
}

// IsSupported はインデックス対象にすべきファイルかを報告する。
// 判定は拡張子だけに基づくので、ベース名でもフルパスでも渡せる。
func IsSupported(name string) bool {
	_, ok := supportedExts[ext(name)]
	return ok
}

// IsDecodable は famifo が自分でサムネイルを作れるファイルかを報告する。
// 偽のときサムネイルは @eaDir から借りるしかなく、借りられなければ一覧には
// 原本が出る（HEIC/HEIFが該当する）。対象外のファイルも偽になる。
func IsDecodable(name string) bool { return supportedExts[ext(name)].decodable }

// IsVideo は動画かを報告する。表示が <img> か <video> かを決め、タイルに再生の印を
// 出すかを決める。
//
// 列を足さずMIMEから導く。同じ事実を表の2箇所に置くと、片方だけ直す事故が起きる。
// 対象外の拡張子では mime が空文字なので偽になる。
func IsVideo(name string) bool {
	return strings.HasPrefix(supportedExts[ext(name)].mime, "video/")
}

// ContentType は name を配信するときのMIMEタイプを返す。
// 対象外の拡張子では "application/octet-stream" になる。
//
// HEIC/HEIFはGoの mime パッケージが知らないため自前の表で引く。
func ContentType(name string) string {
	if f, ok := supportedExts[ext(name)]; ok {
		return f.mime
	}
	return "application/octet-stream"
}

func ext(name string) string { return strings.ToLower(filepath.Ext(name)) }
