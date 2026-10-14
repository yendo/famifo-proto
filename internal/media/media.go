// Package media はインデックス上の1件を表す。型と安定ID、インデックスに
// 載せられるパスの条件を持つ。
//
// IDの導出規則とパスの条件はここにしかない。組み立ては New と Restore を通す。
// 呼び出し側が同じ式を書き直すと規則が二重化するため。
//
// I/Oは一切行わない。
package media

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/yendo/famifo-proto/internal/imagefmt"
	"github.com/yendo/famifo-proto/internal/synology"
)

// layoutWithoutTZ は TakenAtLocal の書式。時差を書かないので、Location は落ちる。
// 辞書順が時系列順と一致する。
const layoutWithoutTZ = "2006-01-02T15:04:05"

// Media はインデックス上の1件。インデックスの1行に対応し、ファイルが
// 差し替わって中身もmtimeも変わっても、同じパスであれば同じ1件として追跡される。
//
// フィールドは非公開で、組み立ては New（新しく見つけた1件）と
// Restore（インデックスからの復元）だけを通る。インデックスに載る1件が
// IsCandidate を満たすことを、このパッケージの外で崩せないようにするため。
type Media struct {
	path         string    // ディスク上の絶対パス
	ownerUID     uint32    // ファイルの所有者のuid。撮影者の判別に使う
	takenAtLocal string    // 撮影地の時刻（layoutWithoutTZ）。読み取れなければmtime
	modTime      time.Time // ファイルのmtime。再スキャン時の変更検知に使う
}

// New はインデックスに載せる1件を組み立てる。IsCandidate を満たさない
// パスはエラーにする。
//
// takenAt はファイルから読み取った撮影日時。写真ならEXIF、動画ならコンテナから
// 読み取り側が解釈を済ませた値である。瞬間としては扱わず、その Location で見た
// 年月日と時刻だけを使う。時差の分からない値は UTC のラベル付きで、時差の分かる値は
// 撮影地の固定ゾーンで届くが、どちらもそれが撮影地の時刻である。
//
// ゼロ値は「読み取れなかった」ことを表し、mtimeで代替される。撮影日時が取れない
// ファイル（スクリーンショット、GIF、WebP、EXIFを削ぎ落とされた画像、コンテナの
// 壊れた動画）は普通に存在し、それらを一覧から落とさないため。mtimeは瞬間なので、
// サーバの time.Local の時刻になる。
//
// ownerUID はファイルの所有者。famifo は所有者を撮影者とみなす。名前ではなく
// uid を持つのは、名前を配信のときに引くためである。アカウント側で名前を
// 変えても、インデックスを作り直さずに済む。
//
// サムネイルの調達より先に組み立てる。ModTime が原本の版であり、
// サムネイルの置き場所はその版から決まるため。
func New(path string, ownerUID uint32, takenAt, modTime time.Time) (Media, error) {
	if !IsCandidate(path) {
		return Media{}, fmt.Errorf("%s cannot be indexed", path)
	}
	if takenAt.IsZero() {
		takenAt = modTime.Local()
	}
	return Media{
		path:         path,
		ownerUID:     ownerUID,
		takenAtLocal: takenAt.Format(layoutWithoutTZ),
		modTime:      modTime,
	}, nil
}

// Restore はインデックスに保存済みの1件を組み立て直す。store が読み出しに使う。
//
// 条件は確かめない。インデックスの行は New を通ったものだけである。
func Restore(path string, ownerUID uint32, takenAtLocal string, modTime time.Time) Media {
	return Media{
		path:         path,
		ownerUID:     ownerUID,
		takenAtLocal: takenAtLocal,
		modTime:      modTime,
	}
}

// ID はパスから導出した安定IDを返す。URLに露出させる。
// URLにファイルシステムのパスを露出させないためと、
// 未インデックスのパスを配信させないための両方の役割を持つ。
func (m Media) ID() string {
	sum := sha256.Sum256([]byte(m.path))
	return hex.EncodeToString(sum[:])[:32]
}

// Path はディスク上の絶対パスを返す。
func (m Media) Path() string { return m.path }

// OwnerUID はファイルの所有者のuidを返す。撮影者の判別に使う。
// 取り込んだ時点の値で、後から chown しても追従しない。chown は mtime を
// 変えないので、再スキャンが変更として拾わないためである。
func (m Media) OwnerUID() uint32 { return m.ownerUID }

// TakenAtLocal は撮影地の時刻を "2006-01-02T15:04:05" で返す。時差は持たない。
// ファイルから読み取れなければmtimeをサーバの時刻で見た値。
func (m Media) TakenAtLocal() string { return m.takenAtLocal }

// Day は撮った日を "2006-01-02" で返す。一覧の区切りに使う。
//
// TakenAtLocal から切り出す。インデックスには両方を書くので、別に持つと
// 2つがずれる経路ができる。
func (m Media) Day() string { return m.takenAtLocal[:len("2006-01-02")] }

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
