// Package photo は写真1枚について答えられることをまとめる。
// インデックス上の型、拡張子による分類、安定ID、撮影日時の決め方（takenat.go）、
// そして配信する画像ファイルのパス。
//
// パスから導けるフィールドの規則はすべてここにある。組み立ては New を通す。
// 呼び出し側が同じ式を書き直すと規則が二重化するため。
//
// 分類は拡張子のみに基づき、ファイルの中身は読まない
// （fsnotifyの大量イベントを軽く捌くため）。
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
	// ThumbSource はサムネイルの出どころ。消してよいのは自前で作ったものだけなので、
	// 「あるか」ではなく「どこにあるか」を持つ。
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
// ID・拡張子・撮影日時はパスとファイル情報から導く。
//
// exifTakenAt は internal/index/exif が読んだEXIFの撮影日時で、ゼロ値は
// 「EXIFに無い」ことを表す。
//
// ThumbSource は受け取らない。借りるか作るかの判定にはIDが要るため構築の
// あとにしか決まらず、呼び出し側が後から入れる。
func New(path string, fi fs.FileInfo, exifTakenAt time.Time) Photo {
	return Photo{
		ID:      IDFor(path),
		Path:    path,
		TakenAt: resolveTakenAt(exifTakenAt, fi.ModTime()),
		ModTime: fi.ModTime(),
		Size:    fi.Size(),
	}
}

// Kind は写真ファイルの扱い方を表す。
type Kind int

const (
	// KindUnsupported はインデックス対象外のファイル。
	KindUnsupported Kind = iota
	// KindRaster はGoでデコードでき、サムネイルを生成するファイル。
	KindRaster
	// KindOpaque はインデックスはするがデコードせず、原本をそのまま配信するファイル。
	// HEIC/HEIFが該当する。原本はSafari以外では表示できないため、@eaDir から
	// 借りられる場合に限り、配信時にSynologyのJPEGへ差し替える（FullPath）。
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

// FamifoThumbPath は famifo が自分で生成したサムネイルのパスを返す。
// 1ディレクトリにファイルが集中しないようIDの先頭2文字で分割する。
func FamifoThumbPath(thumbDir, id string) string {
	return filepath.Join(thumbDir, id[:2], id+".jpg")
}

// ThumbPath は一覧に出すサムネイルのパスを返す。無ければ ok=false。
// ok=false のとき、一覧は原本のURLにフォールバックし、/thumb/ エンドポイントは404を返す。
//
// thumbDir は -data から来る配置設定で、写真そのものの属性ではないため引数で受ける。
func ThumbPath(p Photo, thumbDir string) (string, bool) {
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
func HasThumb(p Photo) bool {
	return p.ThumbSource == ThumbFamifo || p.ThumbSource == ThumbSyno
}

// FullPath は拡大表示に配信するファイルのパスを返す。
//
// HEICはSafari以外のブラウザが表示できない。@eaDir から借りているなら原本ではなく
// SynologyのXL（長辺1707px）を返す。thumb_source が eadir であればMがあり、MとXLは
// 同じ生成器が一緒に書くので、XLの存在はそこから導ける。
//
// 戻り値がパスだけで済むのは、XLのファイル名が .jpg で終わるためである。
// 呼び出し側は ContentType(FullPath(p)) でMIMEを引けばよく、分岐を持たなくてよい。
func FullPath(p Photo) string {
	if KindOf(p.Path) == KindOpaque && p.ThumbSource == ThumbSyno {
		return synology.LargePath(p.Path)
	}
	return p.Path
}
