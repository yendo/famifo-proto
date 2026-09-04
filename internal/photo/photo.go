// Package photo は写真1枚について答えられることをまとめる。
// インデックス上の型と安定ID、サムネイルの出どころ、配信する画像ファイルのパス
// （photo.go）、対応する画像形式（imageformat.go）、撮影日時の決め方
// （takenat.go）。
//
// パスから導ける値の規則はここにしかない。組み立ては New と Restore を通す。
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
	"time"

	"github.com/yendo/famifo-proto/internal/synology"
)

// ThumbSource はサムネイルの出どころ。
type ThumbSource string

const (
	ThumbNone   ThumbSource = ""       // サムネイルが無い
	ThumbFamifo ThumbSource = "famifo" // famifoが生成し、自分の置き場に持っているもの
	ThumbSyno   ThumbSource = "eadir"  // Synologyが @eaDir に持っているもの。読むだけで書き換えない
)

// Photo はインデックス上の1枚の写真。インデックスの1行に対応し、ファイルが
// 差し替わって中身もmtimeも変わっても、同じパスであれば同じ1枚として追跡される。
//
// フィールドは非公開で、組み立ては New（新しく見つけた1枚）と
// Restore（インデックスからの復元）だけを通る。パスから導ける値の規則を
// このパッケージの外で組み直せないようにするため。
type Photo struct {
	id      string    // パスから導出した安定ID。URLに露出させる
	path    string    // ディスク上の絶対パス
	takenAt time.Time // EXIF撮影日時、無ければmtime
	modTime time.Time // ファイルのmtime。再スキャン時の変更検知に使う
	size    int64
	// thumbSource はサムネイルの出どころ。「あるか」ではなく「どこにあるか」を持つ。
	// 出どころによって配信するパスも消してよいかも変わるため。
	thumbSource ThumbSource
}

// New はインデックスに載せる1枚を組み立てる。
// IDと撮影日時はパスとファイル情報から導く。
//
// exifTakenAt は internal/index/exif が読んだEXIFの撮影日時で、ゼロ値は
// 「EXIFに無い」ことを表す。
//
// thumbSource はサムネイルの出どころで、internal/index/thumb の ResolveSource が
// 調達を試みた結果。調達にはパスと向きしか要らないので、組み立てより先に
// 決められる。ここで受け取ることで、Photoは全ての値が確定した状態で生まれる。
func New(path string, fi fs.FileInfo, exifTakenAt time.Time, thumbSource ThumbSource) Photo {
	return Photo{
		id:          IDFor(path),
		path:        path,
		takenAt:     resolveTakenAt(exifTakenAt, fi.ModTime()),
		modTime:     fi.ModTime(),
		size:        fi.Size(),
		thumbSource: thumbSource,
	}
}

// Restore はインデックスに保存済みの1枚を組み立て直す。store が読み出しに使う。
//
// IDは保存された値ではなくパスから導き直す。導出の規則はこのパッケージにしか
// なく、インデックスに入っていた値を信じると規則が二重化するため。
func Restore(path string, takenAt, modTime time.Time, size int64, thumbSource ThumbSource) Photo {
	return Photo{
		id:          IDFor(path),
		path:        path,
		takenAt:     takenAt,
		modTime:     modTime,
		size:        size,
		thumbSource: thumbSource,
	}
}

// ID はパスから導出した安定IDを返す。URLに露出させる。
func (p Photo) ID() string { return p.id }

// Path はディスク上の絶対パスを返す。
func (p Photo) Path() string { return p.path }

// TakenAt は撮影日時を返す。EXIFに無ければmtime。
func (p Photo) TakenAt() time.Time { return p.takenAt }

// ModTime はファイルのmtimeを返す。再スキャン時の変更検知に使う。
func (p Photo) ModTime() time.Time { return p.modTime }

// Size はファイルサイズを返す。
func (p Photo) Size() int64 { return p.size }

// ThumbSource はサムネイルの出どころを返す。store が永続化するために要る。
func (p Photo) ThumbSource() ThumbSource { return p.thumbSource }

// ThumbPath は一覧に出すサムネイルのパスを返す。無ければ ok=false。
// ok=false のとき、一覧は原本のURLにフォールバックし、/thumb/ エンドポイントは404を返す。
//
// thumbDir は -data から来る配置設定で、写真そのものの属性ではないため引数で受ける。
func (p Photo) ThumbPath(thumbDir string) (string, bool) {
	switch p.thumbSource {
	case ThumbFamifo:
		return FamifoThumbPath(thumbDir, p.id), true
	case ThumbSyno:
		return synology.ThumbMPath(p.path), true
	}
	return "", false
}

// HasThumb は一覧に出せるサムネイルがあるかを報告する。
// パスを組み立てずに判定できるので、一覧の組み立てではこちらを使う。
func (p Photo) HasThumb() bool {
	return p.thumbSource == ThumbFamifo || p.thumbSource == ThumbSyno
}

// HasFamifoThumb は famifo が作ったサムネイルを採用しているかを報告する。
// 消してよいのはこれだけで、@eaDir から借りたものは読むだけ。
func (p Photo) HasFamifoThumb() bool { return p.thumbSource == ThumbFamifo }

// FullPath は拡大表示に配信するファイルのパスを返す。
//
// HEICはSafari以外のブラウザが表示できない。@eaDir から借りているなら原本ではなく
// SynologyのXL（長辺1707px）を返す。thumb_source が eadir であればMがあり、MとXLは
// 同じ生成器が一緒に書くので、XLの存在はそこから導ける。
func (p Photo) FullPath() string {
	f, supported := supportedExts[ext(p.path)]
	if supported && !f.decodable && p.thumbSource == ThumbSyno {
		return synology.ThumbXLPath(p.path)
	}
	return p.path
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

// IDFor はパスから安定したIDを導出する。
// URLにファイルシステムのパスを露出させないためと、
// 未インデックスのパスを配信させないための両方の役割を持つ。
func IDFor(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])[:32]
}

// FamifoThumbPath は famifo が自分で生成したサムネイルのパスを返す。
// 1ディレクトリにファイルが集中しないようIDの先頭2文字で分割する。
func FamifoThumbPath(thumbDir, id string) string {
	return filepath.Join(thumbDir, id[:2], id+".jpg")
}
