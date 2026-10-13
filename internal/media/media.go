// Package media はインデックス上の1件を表す。型と安定ID、インデックスに
// 載せられるパスの条件を持つ。
//
// IDの導出規則とパスの条件はここにしかない。組み立ては New と Restore を通す。
// 呼び出し側が同じ式を書き直すと規則が二重化するため。
//
// 対応する形式の表は internal/imagefmt が、表示用に派生した画像の置き場所と
// 選択は internal/thumb が持つ。I/Oは一切行わない。
package media

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/yendo/famifo-proto/internal/imagefmt"
	"github.com/yendo/famifo-proto/internal/synology"
)

// Media はインデックス上の1件。インデックスの1行に対応し、ファイルが
// 差し替わって中身もmtimeも変わっても、同じパスであれば同じ1件として追跡される。
//
// フィールドは非公開で、組み立ては New（新しく見つけた1件）と
// Restore（インデックスからの復元）だけを通る。インデックスに載る1件が
// IsCandidate を満たすことを、このパッケージの外で崩せないようにするため。
type Media struct {
	path    string    // ディスク上の絶対パス
	takenAt time.Time // 読み取れた撮影日時、無ければmtime
	modTime time.Time // ファイルのmtime。再スキャン時の変更検知に使う
}

// New はインデックスに載せる1件を組み立てる。IsCandidate を満たさない
// パスはエラーにする。
//
// takenAt はファイルから読み取った撮影日時。写真ならEXIF、動画ならコンテナから
// 読み取り側が解釈を済ませた値である。ゼロ値は「読み取れなかった」ことを表し、
// mtimeで代替される。撮影日時が取れないファイル（スクリーンショット、GIF、WebP、
// EXIFを削ぎ落とされた画像、コンテナの壊れた動画）は普通に存在し、それらを
// 一覧から落とさないため。
//
// サムネイルの調達より先に組み立てる。ModTime が原本の版であり、
// サムネイルの置き場所はその版から決まるため。
func New(path string, takenAt, modTime time.Time) (Media, error) {
	if !IsCandidate(path) {
		return Media{}, fmt.Errorf("%s cannot be indexed", path)
	}
	if takenAt.IsZero() {
		takenAt = modTime
	}
	return Media{
		path:    path,
		takenAt: takenAt,
		modTime: modTime,
	}, nil
}

// Restore はインデックスに保存済みの1件を組み立て直す。store が読み出しに使う。
//
// 条件は確かめない。インデックスの行は New を通ったものだけである。
func Restore(path string, takenAt, modTime time.Time) Media {
	return Media{
		path:    path,
		takenAt: takenAt,
		modTime: modTime,
	}
}

// ID はパスから導出した安定IDを返す。URLに露出させる。
func (m Media) ID() string { return IDFor(m.path) }

// Path はディスク上の絶対パスを返す。
func (m Media) Path() string { return m.path }

// TakenAt は撮影日時を返す。ファイルから読み取れなければmtime。
func (m Media) TakenAt() time.Time { return m.takenAt }

// ModTime はファイルのmtimeを返す。再スキャン時の変更検知と、
// サムネイルがどの版から作られたかの判別に使う。
func (m Media) ModTime() time.Time { return m.modTime }

// IsCandidate は path がインデックスに載りうるかを報告する。パスだけで
// 決まる条件に限り、ファイルの状態（シンボリックリンクか、中身が拡張子の
// とおりか）は見ない。それは読む直前にしか確かめられない。
//
// 走査と監視はこれで対象を絞り込み、New はこれを満たさないパスを断る。
// @eaDir の中には写真と同じ拡張子のサムネイルが並んでおり、載せると1枚が
// 複数枚に見える。
func IsCandidate(path string) bool {
	return imagefmt.IsSupported(path) && !synology.InManagedDir(path)
}

// IDFor はパスから安定したIDを導出する。
// URLにファイルシステムのパスを露出させないためと、
// 未インデックスのパスを配信させないための両方の役割を持つ。
func IDFor(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])[:32]
}
