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
		return asWallClock(d)
	}
	return modTime
}

// asWallClock はEXIFの日時を、時差が分からない場合にかぎりローカル時刻として
// 解釈し直す。
//
// EXIFのDateTimeOriginalは時差情報を持たない「カメラの壁時計」で、imagemeta は
// これをUTCとして返す。そのまま使うと時差のぶんずれる（JSTなら9時間後ろに
// なり、15時以降に撮った写真が翌日に回る）。撮影地のローカル時刻として読み直す。
//
// ただし OffsetTimeOriginal を持つ写真では imagemeta が時差を適用した固定
// ゾーンを返す。そちらは本当の瞬間が分かっているので触らない。旅行先で撮った
// 写真の時刻を自宅の時差で上書きしてしまう。
//
// 判別は Location が time.UTC そのものかどうかで行う。時差が +00:00 と明示
// された写真は固定ゾーンになるため、UTCと取り違えることはない。
func asWallClock(d time.Time) time.Time {
	if d.Location() != time.UTC {
		return d
	}
	return time.Date(d.Year(), d.Month(), d.Day(),
		d.Hour(), d.Minute(), d.Second(), d.Nanosecond(), time.Local)
}
