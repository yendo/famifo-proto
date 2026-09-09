package videometa_test

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/index/videometa"
)

// epochOffset は1904-01-01から1970-01-01までの秒数。
const epochOffset = 2082844800

// bx は1つの箱を組み立てる。テストが読ませるのはヘッダとペイロードだけなので、
// 32bitサイズで足りる。
func bx(typ string, parts ...[]byte) []byte {
	var body []byte
	for _, p := range parts {
		body = append(body, p...)
	}
	out := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(out[:4], uint32(8+len(body)))
	copy(out[4:8], typ)
	return append(out, body...)
}

// ftyp はメジャーブランドと互換ブランド1つを持つ ftyp を作る。
func ftyp(major, compat string) []byte {
	b := make([]byte, 0, 12)
	b = append(b, major...)
	b = append(b, 0, 0, 2, 0) // minor version
	b = append(b, compat...)
	return bx("ftyp", b)
}

// mvhd は version 0 の mvhd を作る。creation_time はペイロードの4バイト目から。
func mvhd(raw uint32) []byte {
	p := make([]byte, 100)
	binary.BigEndian.PutUint32(p[4:8], raw)
	return bx("mvhd", p)
}

// raw1904 は「その文字盤の時刻をUTCとみなした値」を1904年起点の秒に直す。
func raw1904(y int, mo time.Month, d, h, mi, s int) uint32 {
	return uint32(time.Date(y, mo, d, h, mi, s, 0, time.UTC).Unix() + epochOffset)
}

// mdat64 は size==1 の64bit拡張サイズを使う mdat を作る。Pixel の mp4 がこの形で、
// サイズで飛ばせないと末尾の moov に到達しない。
func mdat64(payload int) []byte {
	out := make([]byte, 16+payload)
	binary.BigEndian.PutUint32(out[:4], 1)
	copy(out[4:8], "mdat")
	binary.BigEndian.PutUint64(out[8:16], uint64(16+payload))
	return out
}

// appleMeta は com.apple.quicktime.creationdate を持つ meta を作る。
// QuickTimeの meta はFullBoxではなく、ペイロードが直接 hdlr から始まる。
func appleMeta(value string) []byte {
	hdlr := bx("hdlr", make([]byte, 24))

	name := "com.apple.quicktime.creationdate"
	entry := make([]byte, 8, 8+len(name))
	binary.BigEndian.PutUint32(entry[:4], uint32(8+len(name)))
	copy(entry[4:8], "mdta")
	entry = append(entry, name...)

	keysBody := make([]byte, 8)
	binary.BigEndian.PutUint32(keysBody[4:8], 1) // entry_count
	keys := bx("keys", keysBody, entry)

	data := bx("data", []byte{0, 0, 0, 1, 0, 0, 0, 0}, []byte(value))
	item := bx("\x00\x00\x00\x01", data) // ilst の項目名は keys の1始まりの索引
	ilst := bx("ilst", item)

	return bx("meta", hdlr, keys, ilst)
}

func write(t *testing.T, name string, parts ...[]byte) string {
	t.Helper()
	var b []byte
	for _, p := range parts {
		b = append(b, p...)
	}
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, b, 0o644))
	return path
}

// isom は仕様どおり mvhd に UTC を書く。Pixel 7a がこれである。
func TestReadTreatsIsoBrandAsUTC(t *testing.T) {
	t.Parallel()
	path := write(t, "a.mp4",
		ftyp("isom", "mp41"),
		bx("moov", mvhd(raw1904(2026, 9, 9, 9, 39, 6))),
	)

	got := videometa.Read(path).TakenAt

	require.True(t, got.Equal(time.Date(2026, 9, 9, 9, 39, 6, 0, time.UTC)), "got %v", got)
}

// QuickTimeブランドでAppleのキーが無ければ、mvhd は文字盤の時刻である。
// Canon PowerShot S95 がこれで、同じファイル内のEXIFと一致することを確認済み。
func TestReadTreatsQuickTimeWithoutAppleKeyAsLocalTime(t *testing.T) {
	t.Parallel()
	path := write(t, "a.mov",
		ftyp("qt  ", "qt  "),
		bx("moov", mvhd(raw1904(2011, 1, 11, 20, 5, 54))),
	)

	got := videometa.Read(path).TakenAt

	require.True(t, got.Equal(time.Date(2011, 1, 11, 20, 5, 54, 0, time.Local)), "got %v", got)
}

// 互換ブランドにだけ qt があってもQuickTimeとみなす。
func TestReadDetectsQuickTimeFromACompatibleBrand(t *testing.T) {
	t.Parallel()
	path := write(t, "a.mov",
		ftyp("mp42", "qt  "),
		bx("moov", mvhd(raw1904(2011, 1, 11, 20, 5, 54))),
	)

	got := videometa.Read(path).TakenAt

	require.True(t, got.Equal(time.Date(2011, 1, 11, 20, 5, 54, 0, time.Local)), "got %v", got)
}

// Appleのキーがあればそれが mvhd より優先される。時差を持つ唯一の出どころである。
func TestReadPrefersTheAppleCreationDate(t *testing.T) {
	t.Parallel()
	path := write(t, "a.mov",
		ftyp("qt  ", "qt  "),
		bx("moov",
			mvhd(raw1904(2026, 9, 9, 9, 39, 6)),
			appleMeta("2026-09-09T18:39:06+0900"),
		),
	)

	got := videometa.Read(path).TakenAt

	require.True(t, got.Equal(time.Date(2026, 9, 9, 9, 39, 6, 0, time.UTC)), "got %v", got)
}

// meta は udta の下にある場合もある。
func TestReadFindsTheAppleCreationDateUnderUdta(t *testing.T) {
	t.Parallel()
	path := write(t, "a.mov",
		ftyp("qt  ", "qt  "),
		bx("moov",
			mvhd(raw1904(2026, 9, 9, 9, 39, 6)),
			bx("udta", appleMeta("2026-09-09T18:39:06+09:00")),
		),
	)

	got := videometa.Read(path).TakenAt

	require.True(t, got.Equal(time.Date(2026, 9, 9, 9, 39, 6, 0, time.UTC)), "got %v", got)
}

// moov が末尾にあり mdat が64bit拡張サイズでも読める。Pixel の mp4 がこの配置。
func TestReadFindsMoovAfterA64BitMdat(t *testing.T) {
	t.Parallel()
	path := write(t, "a.mp4",
		ftyp("isom", "mp41"),
		mdat64(4096),
		bx("moov", mvhd(raw1904(2026, 9, 9, 9, 39, 6))),
	)

	got := videometa.Read(path).TakenAt

	require.True(t, got.Equal(time.Date(2026, 9, 9, 9, 39, 6, 0, time.UTC)), "got %v", got)
}

// version 1 の mvhd は creation_time が64bitになる。
func TestReadHandlesAVersion1Mvhd(t *testing.T) {
	t.Parallel()
	p := make([]byte, 120)
	p[0] = 1 // version
	binary.BigEndian.PutUint64(p[4:12], uint64(raw1904(2026, 9, 9, 9, 39, 6)))
	path := write(t, "a.mp4",
		ftyp("isom", "mp41"),
		bx("moov", bx("mvhd", p)),
	)

	got := videometa.Read(path).TakenAt

	require.True(t, got.Equal(time.Date(2026, 9, 9, 9, 39, 6, 0, time.UTC)), "got %v", got)
}

// 読めなかったときはゼロ値を返す。呼び出し側がmtimeに落ちる。
func TestReadReturnsZeroWhenNothingIsReadable(t *testing.T) {
	t.Parallel()
	tests := map[string][]byte{
		"creation_timeが0": append(ftyp("isom", "mp41"), bx("moov", mvhd(0))...),
		"moovが無い":         ftyp("isom", "mp41"),
		"mvhdが無い":         append(ftyp("isom", "mp41"), bx("moov")...),
		"1970年より前":       append(ftyp("isom", "mp41"), bx("moov", mvhd(1))...),
		"途中で切れている":        append(ftyp("isom", "mp41"), []byte{0, 0, 1, 0, 'm', 'o'}...),
		"空":              {},
		"箱ではない":          []byte("this is not a container at all"),
		"サイズが過小":         append(ftyp("isom", "mp41"), []byte{0, 0, 0, 2, 'm', 'o', 'o', 'v'}...),
		"サイズが行き過ぎ":        append(ftyp("isom", "mp41"), []byte{0x7f, 0, 0, 0, 'm', 'o', 'o', 'v'}...),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := write(t, "a.mp4", body)
			require.True(t, videometa.Read(path).TakenAt.IsZero())
		})
	}
}

// ftyp が無ければ ISO とみなす。qt の判定が偽になるだけで、読めるものは読む。
func TestReadWithoutFtypTreatsTheFileAsIso(t *testing.T) {
	t.Parallel()
	path := write(t, "a.mp4", bx("moov", mvhd(raw1904(2026, 9, 9, 9, 39, 6))))

	got := videometa.Read(path).TakenAt

	require.True(t, got.Equal(time.Date(2026, 9, 9, 9, 39, 6, 0, time.UTC)), "got %v", got)
}

// 開けないパスでも失敗せずゼロ値を返す。
func TestReadReturnsZeroForAMissingFile(t *testing.T) {
	t.Parallel()
	require.True(t, videometa.Read(filepath.Join(t.TempDir(), "nope.mp4")).TakenAt.IsZero())
}

// ディレクトリを渡されても落ちない。拡張子付きのディレクトリは実在する。
func TestReadReturnsZeroForADirectory(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "sub.mp4")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	require.True(t, videometa.Read(dir).TakenAt.IsZero())
}

// 時差が "Z" の動画でも、返る値の Location が time.UTC そのものになってはいけない。
// media 側がその条件で文字盤の時刻へ読み替えるため、時差ぶんずれてしまう。
func TestReadNeverReturnsABareUTCLocation(t *testing.T) {
	t.Parallel()
	path := write(t, "a.mov",
		ftyp("qt  ", "qt  "),
		bx("moov",
			mvhd(raw1904(2026, 9, 9, 9, 39, 6)),
			appleMeta("2026-09-09T09:39:06Z"),
		),
	)

	got := videometa.Read(path).TakenAt

	require.True(t, got.Equal(time.Date(2026, 9, 9, 9, 39, 6, 0, time.UTC)), "got %v", got)
	require.NotEqual(t, time.UTC, got.Location(), "media.resolveTakenAt would shift a bare UTC value")
}
