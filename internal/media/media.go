// Package media はインデックス上の1件を表す。型と安定ID（media.go）、
// 撮影日時の決め方（takenat.go）。
//
// IDの導出規則はここにしかない。組み立ては New と Restore を通す。
// 呼び出し側が同じ式を書き直すと規則が二重化するため。
//
// 対応する形式の表は internal/imagefmt が、表示用に派生した画像の置き場所と
// 選択は internal/thumb が持つ。I/Oは一切行わない。
package media

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"time"
)

// Media はインデックス上の1件。インデックスの1行に対応し、ファイルが
// 差し替わって中身もmtimeも変わっても、同じパスであれば同じ1件として追跡される。
//
// フィールドは非公開で、組み立ては New（新しく見つけた1件）と
// Restore（インデックスからの復元）だけを通る。パスから導ける値の規則を
// このパッケージの外で組み直せないようにするため。
type Media struct {
	id      string    // パスから導出した安定ID。URLに露出させる
	path    string    // ディスク上の絶対パス
	takenAt time.Time // 読み取れた撮影日時、無ければmtime
	modTime time.Time // ファイルのmtime。再スキャン時の変更検知に使う
}

// New はインデックスに載せる1件を組み立てる。
// IDと撮影日時はパスとファイル情報から導く。
//
// takenAt はファイルから読み取った撮影日時。写真ならEXIFの DateTimeOriginal、
// 動画ならコンテナから videometa が解釈した値である。ゼロ値は「読み取れなかった」
// ことを表し、mtimeで代替される。
//
// サムネイルの調達より先に組み立てる。ModTime が原本の版であり、
// サムネイルの置き場所はその版から決まるため。
func New(path string, fi fs.FileInfo, takenAt time.Time) Media {
	return Media{
		id:      IDFor(path),
		path:    path,
		takenAt: resolveTakenAt(takenAt, fi.ModTime()),
		modTime: fi.ModTime(),
	}
}

// Restore はインデックスに保存済みの1件を組み立て直す。store が読み出しに使う。
//
// IDは保存された値ではなくパスから導き直す。導出の規則はこのパッケージにしか
// なく、インデックスに入っていた値を信じると規則が二重化するため。
func Restore(path string, takenAt, modTime time.Time) Media {
	return Media{
		id:      IDFor(path),
		path:    path,
		takenAt: takenAt,
		modTime: modTime,
	}
}

// ID はパスから導出した安定IDを返す。URLに露出させる。
func (m Media) ID() string { return m.id }

// Path はディスク上の絶対パスを返す。
func (m Media) Path() string { return m.path }

// TakenAt は撮影日時を返す。ファイルから読み取れなければmtime。
func (m Media) TakenAt() time.Time { return m.takenAt }

// ModTime はファイルのmtimeを返す。再スキャン時の変更検知と、
// サムネイルがどの版から作られたかの判別に使う。
func (m Media) ModTime() time.Time { return m.modTime }

// IDFor はパスから安定したIDを導出する。
// URLにファイルシステムのパスを露出させないためと、
// 未インデックスのパスを配信させないための両方の役割を持つ。
func IDFor(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])[:32]
}
