// Package videometa は動画のコンテナ（ISO base media file format / QuickTime）から
// 撮影日時を読む。読めなくても失敗せず、パニックもしない。
//
// exif と対になる位置に置いてあるが、読む相手の性質が違う。静止画のEXIFは機種に
// よらず時差を持たないローカル時刻だが、動画は機種によって規約が割れる。同じ
// Pixel 7a が、写真ではEXIFにローカル時刻を書き、動画では mvhd に UTC を書く。
//
// 規約の見分け方は ftyp のブランドである。
//
//   - qt 以外（isom等）  : mvhd は UTC。Pixel 7a で実測
//   - qt + Appleのキー   : com.apple.quicktime.creationdate が時差を持つ。iPhone（未検証）
//   - qt + キー無し      : mvhd は文字盤の時刻。Canon PowerShot S95 で実測
//
// 外部ライブラリは使わない。長時間動くデーモンなので、壊れた入力で落ちないことを
// 自分で保証する。
package videometa

import (
	"encoding/binary"
	"io"
	"os"
	"strings"
	"time"
)

// Meta は動画1本から読み取れた情報。読めなかった項目は既定値になる。
type Meta struct {
	// TakenAt は撮影日時。読めなければゼロ値。
	// 呼び出し側はゼロ値をmtimeに落とす（EXIFが無い写真と同じ扱い）。
	TakenAt time.Time
}

// epochOffset は1904-01-01から1970-01-01までの秒数。コンテナの時刻はこの起点で入る。
const epochOffset = 2082844800

// qtBrand はQuickTimeを表すブランド。末尾の2つは空白である。
const qtBrand = "qt  "

// appleCreationDateKey はAppleが時差付きの撮影日時を入れるキー。
const appleCreationDateKey = "com.apple.quicktime.creationdate"

// 壊れた入力で無限に歩き回らないための上限。
const (
	maxDepth    = 8
	maxBoxes    = 4096
	maxMoovSize = 32 << 20 // moovが32MBを超えるファイルは相手にしない
	maxFtypSize = 1024
	maxKeys     = 256
	maxDataSize = 4096
)

// Read は path のコンテナから撮影日時を読む。
//
// 「Readは絶対に失敗しない」という契約を、あらゆる入力に対して構造的に保証する
// ためのガード。imagemeta に任せている exif と違って自前のパーサだが、境界の
// 読み違いでパニックしうる点は同じである。
func Read(path string) (m Meta) {
	defer func() {
		if recover() != nil {
			m = Meta{}
		}
	}()

	f, err := os.Open(path)
	if err != nil {
		return Meta{}
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return Meta{}
	}

	var quicktime bool
	var creation uint64
	var appleDate string

	walk(f, 0, fi.Size(), 0, func(b box) bool {
		switch b.typ {
		case "ftyp":
			quicktime = isQuickTime(f, b)
		case "moov":
			if b.end-b.start <= maxMoovSize {
				creation, appleDate = readMoov(f, b)
			}
			return false // moovを読み終えたら以降は要らない
		}
		return true
	})

	return Meta{TakenAt: resolve(quicktime, creation, appleDate)}
}

// box は1つの箱のペイロードの範囲。
type box struct {
	typ        string
	start, end int64
}

// walk は [start,end) の直下にある箱を順に fn へ渡す。fn が false を返すと打ち切る。
//
// 壊れた入力ではその場で打ち切る。エラーを返さないのは、途中まで読めた値を捨てない
// ためである（mvhd の後ろが壊れていても撮影日時は取れている）。
func walk(r io.ReaderAt, start, end int64, depth int, fn func(box) bool) {
	if depth > maxDepth {
		return
	}
	pos := start
	for n := 0; pos+8 <= end && n < maxBoxes; n++ {
		var hdr [8]byte
		if _, err := r.ReadAt(hdr[:], pos); err != nil {
			return
		}
		size := int64(binary.BigEndian.Uint32(hdr[:4]))
		typ := string(hdr[4:8])
		head := int64(8)

		switch size {
		case 1:
			// 64bit拡張サイズ。4GBを超えなくても使われる（Pixelの mp4 の mdat がそう）。
			var ext [8]byte
			if _, err := r.ReadAt(ext[:], pos+8); err != nil {
				return
			}
			size = int64(binary.BigEndian.Uint64(ext[:]))
			head = 16
		case 0:
			size = end - pos // 最後の箱は「残り全部」を意味する
		}
		if size < head || pos+size > end {
			return // 壊れている
		}
		if !fn(box{typ: typ, start: pos + head, end: pos + size}) {
			return
		}
		pos += size
	}
}

// isQuickTime は ftyp のメジャーブランドか互換ブランドに qt が含まれるかを返す。
func isQuickTime(r io.ReaderAt, b box) bool {
	n := b.end - b.start
	if n < 8 || n > maxFtypSize {
		return false
	}
	buf := make([]byte, n)
	if _, err := r.ReadAt(buf, b.start); err != nil {
		return false
	}
	if string(buf[:4]) == qtBrand {
		return true
	}
	// 先頭4バイトがメジャーブランド、次の4バイトがマイナーバージョン、以降が互換ブランド。
	for i := 8; i+4 <= len(buf); i += 4 {
		if string(buf[i:i+4]) == qtBrand {
			return true
		}
	}
	return false
}

// readMoov は moov から mvhd の creation_time と Apple のキーの値を拾う。
//
// meta は moov の直下にある場合と udta の下にある場合の両方があるため、両方を見る。
func readMoov(r io.ReaderAt, moov box) (creation uint64, appleDate string) {
	walk(r, moov.start, moov.end, 1, func(b box) bool {
		switch b.typ {
		case "mvhd":
			creation = readCreationTime(r, b)
		case "meta":
			if s := readAppleDate(r, b); s != "" {
				appleDate = s
			}
		case "udta":
			walk(r, b.start, b.end, 2, func(c box) bool {
				if c.typ == "meta" {
					if s := readAppleDate(r, c); s != "" {
						appleDate = s
					}
				}
				return true
			})
		}
		return true
	})
	return creation, appleDate
}

// readCreationTime は mvhd の creation_time を返す。
// version 0 は32bit、version 1 は64bit。
func readCreationTime(r io.ReaderAt, b box) uint64 {
	var head [4]byte
	if _, err := r.ReadAt(head[:], b.start); err != nil {
		return 0
	}
	switch head[0] {
	case 0:
		var v [4]byte
		if _, err := r.ReadAt(v[:], b.start+4); err != nil {
			return 0
		}
		return uint64(binary.BigEndian.Uint32(v[:]))
	case 1:
		var v [8]byte
		if _, err := r.ReadAt(v[:], b.start+4); err != nil {
			return 0
		}
		return binary.BigEndian.Uint64(v[:])
	}
	return 0
}

// metaPayloadStart は meta のペイロードが始まる位置を返す。
//
// ISOの meta はFullBoxでversion/flagsの4バイトを持つが、QuickTimeの meta は持たず
// 直接 hdlr から始まる。Pixel の mp4 は isom ブランドなのにQuickTime形式で書いて
// いたため、ブランドでは判別できない。最初の子が hdlr であることを手掛かりにする。
func metaPayloadStart(r io.ReaderAt, b box) int64 {
	var buf [12]byte
	if _, err := r.ReadAt(buf[:], b.start); err != nil {
		return b.start
	}
	if string(buf[4:8]) == "hdlr" {
		return b.start
	}
	return b.start + 4
}

// readAppleDate は meta から com.apple.quicktime.creationdate の値を返す。
// 無ければ空文字。
func readAppleDate(r io.ReaderAt, meta box) string {
	var keys []string
	var ilst box
	var haveIlst bool

	walk(r, metaPayloadStart(r, meta), meta.end, 3, func(b box) bool {
		switch b.typ {
		case "keys":
			keys = readKeys(r, b)
		case "ilst":
			ilst, haveIlst = b, true
		}
		return true
	})
	if !haveIlst {
		return ""
	}

	// ilst の項目名は4バイトの整数で、keys の1始まりの索引を指す。
	want := -1
	for i, k := range keys {
		if k == appleCreationDateKey {
			want = i + 1
			break
		}
	}
	if want < 0 {
		return ""
	}

	var out string
	walk(r, ilst.start, ilst.end, 4, func(b box) bool {
		if len(b.typ) != 4 || int(binary.BigEndian.Uint32([]byte(b.typ))) != want {
			return true
		}
		out = readDataString(r, b)
		return false
	})
	return out
}

// readKeys は keys のエントリ名を並び順に返す。
func readKeys(r io.ReaderAt, b box) []string {
	var head [8]byte
	if _, err := r.ReadAt(head[:], b.start); err != nil {
		return nil
	}
	n := int(binary.BigEndian.Uint32(head[4:8]))
	if n <= 0 || n > maxKeys {
		return nil
	}

	out := make([]string, 0, n)
	pos := b.start + 8
	for i := 0; i < n && pos+8 <= b.end; i++ {
		var eh [8]byte
		if _, err := r.ReadAt(eh[:], pos); err != nil {
			return out
		}
		size := int64(binary.BigEndian.Uint32(eh[:4]))
		if size < 8 || pos+size > b.end {
			return out
		}
		name := make([]byte, size-8)
		if _, err := r.ReadAt(name, pos+8); err != nil {
			return out
		}
		out = append(out, string(name))
		pos += size
	}
	return out
}

// readDataString は ilst の項目が持つ data の中身を文字列として返す。
// data の先頭8バイトは型と言語なので読み飛ばす。
func readDataString(r io.ReaderAt, item box) string {
	var out string
	walk(r, item.start, item.end, 5, func(b box) bool {
		if b.typ != "data" {
			return true
		}
		n := b.end - b.start
		if n <= 8 || n > maxDataSize {
			return false
		}
		buf := make([]byte, n-8)
		if _, err := r.ReadAt(buf, b.start+8); err != nil {
			return false
		}
		out = string(buf)
		return false
	})
	return out
}

// resolve は読み取った材料から撮影日時を決める。
func resolve(quicktime bool, creation uint64, appleDate string) time.Time {
	if t, ok := parseAppleDate(appleDate); ok {
		return t
	}
	if creation == 0 {
		return time.Time{}
	}
	secs := int64(creation) - epochOffset
	if secs < 0 {
		return time.Time{} // 1970年より前は壊れた値として捨てる
	}
	if quicktime {
		// QuickTimeの古い機種は文字盤の時刻を書く。UTCとして読むと時差ぶんずれる。
		u := time.Unix(secs, 0).UTC()
		return time.Date(u.Year(), u.Month(), u.Day(), u.Hour(), u.Minute(), u.Second(), 0, time.Local)
	}
	return time.Unix(secs, 0)
}

// parseAppleDate は "2026-09-09T18:39:06+0900" 形式を解く。
// 時差の書き方は揺れるので、コロンの有無の両方を受ける。
func parseAppleDate(s string) (time.Time, bool) {
	s = strings.TrimRight(s, "\x00")
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{"2006-01-02T15:04:05-0700", time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil && t.Unix() >= 0 {
			return t, true
		}
	}
	return time.Time{}, false
}
