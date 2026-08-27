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
		return assumeLocal(d)
	}
	return modTime
}

// assumeLocal は時差の分からないEXIF日時を、撮影地の時刻とみなして解釈し直す。
//
// EXIFのDateTimeOriginalは時差を持たない。imagemeta はこれをUTCとして返すが、
// 実際にはカメラが表示していた時刻なので、そのまま使うと時差のぶんずれる
// （JSTなら9時間後ろになり、15時以降に撮った写真が翌日に回る）。
//
// ただし OffsetTimeOriginal を持つ写真では imagemeta が時差を適用した固定
// ゾーンを返す。そちらは撮影した瞬間が確定しているので触らない。旅行先で撮った
// 写真の時刻を自宅の時差で上書きしてしまう。
//
// 判別は Location が time.UTC そのものかどうかで行う。時差が +00:00 と明示
// された写真は固定ゾーンになるため、UTCと取り違えることはない。
func assumeLocal(d time.Time) time.Time {
	if d.Location() != time.UTC {
		return d
	}
	return time.Date(d.Year(), d.Month(), d.Day(),
		d.Hour(), d.Minute(), d.Second(), d.Nanosecond(), time.Local)
}
