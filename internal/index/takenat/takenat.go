// Package takenat は写真の「撮影日時」を決める。
// EXIFの撮影日時があればそれを、無ければファイルのmtimeを使う。
//
// EXIFの読み取りそのものは internal/index/exif が担う。ここにあるのは、
// 読み取れた値をどう解釈し、無いときに何で代替するかという方針だけである。
package takenat

import "time"

// Resolve は写真の撮影日時を返す。
//
// exifTakenAt は internal/index/exif が読んだ撮影日時で、ゼロ値は「EXIFに無い」ことを
// 表す。撮影日時が取れないファイル（スクリーンショット、GIF、WebP、EXIFを
// 削ぎ落とされた画像）は普通に存在し、それらを一覧から落とさないために
// 必ずmodTimeで代替する。
func Resolve(exifTakenAt, modTime time.Time) time.Time {
	if exifTakenAt.IsZero() {
		return modTime
	}
	return assumeLocal(exifTakenAt)
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
