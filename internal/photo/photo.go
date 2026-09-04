// Package photo は写真1枚について答えられることをまとめる。
// インデックス上の型、拡張子による分類、安定ID、撮影日時の決め方（takenat.go）、
// サムネイルの出どころ、そして配信する画像ファイルのパス。
//
// パスから導ける値の規則はここにしかない。組み立ては New を通す。
// 呼び出し側が同じ式を書き直すと規則が二重化するため。
//
// 分類は拡張子のみに基づき、ファイルの中身は読まない
// （fsnotifyの大量イベントを軽く捌くため）。I/Oは一切行わない。
package photo

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/yendo/famifo-proto/internal/synology"
)

// Photo はインデックス上の1枚の写真。
type Photo struct {
	ID      string    // パスから導出した安定ID。URLに露出させる
	Path    string    // ディスク上の絶対パス
	TakenAt time.Time // EXIF撮影日時、無ければmtime
	ModTime time.Time // ファイルのmtime。再スキャン時の変更検知に使う
	Size    int64
	// ThumbSource はサムネイルの出どころ。「あるか」ではなく「どこにあるか」を持つ。
	// 出どころによって配信するパスも消してよいかも変わるため。
	//
	// 書き換えは AdoptSynoThumb / AdoptFamifoThumb を通す。公開しているのは
	// store が永続化するためで、外から直接代入する場所ではない。
	ThumbSource ThumbSource
}

// ThumbSource はサムネイルの出どころ。
type ThumbSource string

const (
	ThumbNone   ThumbSource = ""       // サムネイルが無い
	ThumbFamifo ThumbSource = "famifo" // famifoが生成し、自分の置き場に持っているもの
	ThumbSyno   ThumbSource = "eadir"  // Synologyが @eaDir に持っているもの。読むだけで書き換えない
)

// IDFor はパスから安定したIDを導出する。
// URLにファイルシステムのパスを露出させないためと、
// 未インデックスのパスを配信させないための両方の役割を持つ。
func IDFor(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])[:32]
}

// New はインデックスに載せる1枚を組み立てる。
// IDと撮影日時はパスとファイル情報から導く。
//
// exifTakenAt は internal/index/exif が読んだEXIFの撮影日時で、ゼロ値は
// 「EXIFに無い」ことを表す。
//
// ThumbSource は受け取らない。どちらを採用するかは調達を試みた結果であって、
// 構築の時点ではまだ決まっていない。AdoptSynoThumb / AdoptFamifoThumb で
// 後から記録する（internal/index/thumb の ResolveSource が呼ぶ）。
func New(path string, fi fs.FileInfo, exifTakenAt time.Time) Photo {
	return Photo{
		ID:      IDFor(path),
		Path:    path,
		TakenAt: resolveTakenAt(exifTakenAt, fi.ModTime()),
		ModTime: fi.ModTime(),
		Size:    fi.Size(),
	}
}

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

// FamifoThumbPath は famifo が自分で生成したサムネイルのパスを返す。
// 1ディレクトリにファイルが集中しないようIDの先頭2文字で分割する。
func FamifoThumbPath(thumbDir, id string) string {
	return filepath.Join(thumbDir, id[:2], id+".jpg")
}

// ThumbPath は一覧に出すサムネイルのパスを返す。無ければ ok=false。
// ok=false のとき、一覧は原本のURLにフォールバックし、/thumb/ エンドポイントは404を返す。
//
// thumbDir は -data から来る配置設定で、写真そのものの属性ではないため引数で受ける。
func (p Photo) ThumbPath(thumbDir string) (string, bool) {
	switch p.ThumbSource {
	case ThumbFamifo:
		return FamifoThumbPath(thumbDir, p.ID), true
	case ThumbSyno:
		return synology.ThumbPath(p.Path), true
	}
	return "", false
}

// HasThumb は一覧に出せるサムネイルがあるかを報告する。
// パスを組み立てずに判定できるので、一覧の組み立てではこちらを使う。
func (p Photo) HasThumb() bool {
	return p.ThumbSource == ThumbFamifo || p.ThumbSource == ThumbSyno
}

// HasFamifoThumb は famifo が作ったサムネイルを採用しているかを報告する。
// 消してよいのはこれだけで、@eaDir から借りたものは読むだけ。
func (p Photo) HasFamifoThumb() bool { return p.ThumbSource == ThumbFamifo }

// AdoptSynoThumb は @eaDir のサムネイルを採用する。読むだけで書き換えない。
func (p *Photo) AdoptSynoThumb() { p.ThumbSource = ThumbSyno }

// AdoptFamifoThumb は自前で生成し、置き場に収めたサムネイルを採用する。
func (p *Photo) AdoptFamifoThumb() { p.ThumbSource = ThumbFamifo }

// FullPath は拡大表示に配信するファイルのパスを返す。
//
// HEICはSafari以外のブラウザが表示できない。@eaDir から借りているなら原本ではなく
// SynologyのXL（長辺1707px）を返す。thumb_source が eadir であればMがあり、MとXLは
// 同じ生成器が一緒に書くので、XLの存在はそこから導ける。
func (p Photo) FullPath() string {
	f, supported := supportedExts[ext(p.Path)]
	if supported && !f.decodable && p.ThumbSource == ThumbSyno {
		return synology.LargePath(p.Path)
	}
	return p.Path
}

// ContentType は FullPath が返すファイルのMIMEタイプを返す。
// 借りたXLは .jpg なので、原本がHEICでも image/jpeg になる。
//
// HEIC/HEIFはGoの mime パッケージが知らないため自前の表で引く。
func (p Photo) ContentType() string {
	if f, ok := supportedExts[ext(p.FullPath())]; ok {
		return f.mime
	}
	return "application/octet-stream"
}
