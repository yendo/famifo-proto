// Package photo はインデックス上の1枚を表す。型と安定ID（photo.go）、
// 撮影日時の決め方（takenat.go）。
//
// IDの導出規則はここにしかない。組み立ては New と Restore を通す。
// 呼び出し側が同じ式を書き直すと規則が二重化するため。
//
// 対応する画像形式の表は internal/imagefmt が、表示用に派生した画像の置き場所と
// 選択は internal/thumb が持つ。I/Oは一切行わない。
package photo

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"time"
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
}

// New はインデックスに載せる1枚を組み立てる。
// IDと撮影日時はパスとファイル情報から導く。
//
// exifTakenAt は internal/index/exif が読んだEXIFの撮影日時で、ゼロ値は
// 「EXIFに無い」ことを表す。
//
// サムネイルの調達より先に組み立てる。ModTime が原本の版であり、
// internal/thumb はそれを見て出力の名前を決めるため。
func New(path string, fi fs.FileInfo, exifTakenAt time.Time) Photo {
	return Photo{
		id:      IDFor(path),
		path:    path,
		takenAt: resolveTakenAt(exifTakenAt, fi.ModTime()),
		modTime: fi.ModTime(),
	}
}

// Restore はインデックスに保存済みの1枚を組み立て直す。store が読み出しに使う。
//
// IDは保存された値ではなくパスから導き直す。導出の規則はこのパッケージにしか
// なく、インデックスに入っていた値を信じると規則が二重化するため。
func Restore(path string, takenAt, modTime time.Time) Photo {
	return Photo{
		id:      IDFor(path),
		path:    path,
		takenAt: takenAt,
		modTime: modTime,
	}
}

// ID はパスから導出した安定IDを返す。URLに露出させる。
func (p Photo) ID() string { return p.id }

// Path はディスク上の絶対パスを返す。
func (p Photo) Path() string { return p.path }

// TakenAt は撮影日時を返す。EXIFに無ければmtime。
func (p Photo) TakenAt() time.Time { return p.takenAt }

// ModTime はファイルのmtimeを返す。再スキャン時の変更検知と、
// サムネイルがどの版から作られたかの判別に使う。
func (p Photo) ModTime() time.Time { return p.modTime }

// IDFor はパスから安定したIDを導出する。
// URLにファイルシステムのパスを露出させないためと、
// 未インデックスのパスを配信させないための両方の役割を持つ。
func IDFor(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])[:32]
}
