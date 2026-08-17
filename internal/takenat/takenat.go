// Package takenat は写真の「撮影日時」を決める。
// EXIFがあればそれを、無ければファイルのmtimeを使う。
package takenat

import (
	"os"
	"time"

	"github.com/evanoberholster/imagemeta"
)

// Resolve は写真の撮影日時を返す。
//
// EXIFの読み取りに失敗しても呼び出し側にエラーは返さない。撮影日時が取れない
// ファイル（スクリーンショット、GIF、WebP、EXIFを削ぎ落とされた画像）は普通に
// 存在し、それらを一覧から落とさないために必ずmodTimeで代替する。
func Resolve(path string, modTime time.Time) (takenAt time.Time) {
	// imagemeta はJPEGパーサ自体をrecoverで囲っているが、ISOBMFF(HEIC/HEIF)と
	// PNGのパスは囲っていない。長時間稼働するデーモンなのでここで拾い損ねると
	// HTTPサーバーごと道連れになる。「Resolveは絶対に失敗しない」という
	// このパッケージの契約を、あらゆる入力に対して構造的に保証するためのガード。
	defer func() {
		if r := recover(); r != nil {
			takenAt = modTime
		}
	}()

	f, err := os.Open(path)
	if err != nil {
		return modTime
	}
	defer f.Close()

	ex, err := imagemeta.Decode(f)
	if err != nil {
		return modTime
	}
	// SelectedDate は DateTimeOriginal → CreateDate → ModifyDate の順に探し、
	// どれも無ければゼロ値を返す。
	if d := ex.SelectedDate(); !d.IsZero() {
		return d
	}
	return modTime
}
