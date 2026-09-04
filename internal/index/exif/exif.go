// Package exif は写真のEXIFを読む。読めなくても失敗せず、パニックもしない。
//
// imagemeta を呼ぶのはこのパッケージだけである。imagemeta はJPEGパーサ自体を
// recoverで囲っているが、ISOBMFF(HEIC/HEIF)とPNGのパスは囲っていない。
// 長時間稼働するデーモンなのでここで拾い損ねるとHTTPサーバーごと道連れになる。
// そのガードを1箇所に集めるために、EXIFの読み取りをここへ閉じ込めている。
//
// 1枚につき1回だけ読めば足りるよう、撮影日時と向きをまとめて返す。
package exif

import (
	"os"
	"time"

	"github.com/evanoberholster/imagemeta"
)

// Meta は写真1枚から読み取れたEXIF。読めなかった項目は既定値になる。
type Meta struct {
	// TakenAt はEXIFの撮影日時。持たない写真ではゼロ値。
	// 時差を持たない値をどう解釈するかは方針の問題なので、ここでは組み直さず
	// imagemeta が返したまま渡す（photo.New が決める）。
	TakenAt time.Time
	// Orientation は表示時の向き。1..8以外だった場合は回転不要の1にする。
	Orientation uint16
}

// unknown はEXIFが読めなかったときのMeta。
var unknown = Meta{Orientation: 1}

// Read は path のEXIFを読む。
//
// EXIFを持たないファイル（スクリーンショット、GIF、WebP、EXIFを削ぎ落とされた
// 画像）は普通に存在するため、読めないことをエラーにはしない。呼び出し側が
// 分岐を持たずに済むよう、常に既定値の入ったMetaを返す。
func Read(path string) (m Meta) {
	// 「Readは絶対に失敗しない」という契約を、あらゆる入力に対して構造的に
	// 保証するためのガード。
	defer func() {
		if recover() != nil {
			m = unknown
		}
	}()

	f, err := os.Open(path)
	if err != nil {
		return unknown
	}
	defer f.Close()

	ex, err := imagemeta.Decode(f)
	if err != nil {
		return unknown
	}

	m = unknown
	// SelectedDate は DateTimeOriginal → CreateDate → ModifyDate の順に探し、
	// どれも無ければゼロ値を返す。
	m.TakenAt = ex.SelectedDate()
	if o := uint16(ex.IFD0.Orientation); o >= 1 && o <= 8 {
		m.Orientation = o
	}
	return m
}
