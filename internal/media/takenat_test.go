package media_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/media"
)

// newWithEXIFDate は撮影日時だけを変えて1枚を組み立てる。
func newWithEXIFDate(exifTakenAt time.Time) media.Media {
	return media.New("/photos/a.jpg",
		fakeFileInfo{modTime: testModTime}, exifTakenAt)
}

func TestNewUsesTheEXIFDateWhenPresent(t *testing.T) {
	t.Parallel()
	p := newWithEXIFDate(time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC))

	// 日付と時刻の見た目で比べる。瞬間で比べると実行環境のタイムゾーンに
	// 依存し、「EXIFをUTCとして読む」という誤った挙動を固定してしまう。
	// 時差の解釈そのものは TestNewAssumesLocalWhenTheEXIFDateHasNoOffset が押さえる。
	require.Equal(t, "2021-03-04 05:06:07", p.TakenAt().Format("2006-01-02 15:04:05"),
		"the EXIF capture time wins")
}

// EXIFのDateTimeOriginalは時差を持たない。撮影地の時刻とみなして読まないと、
// 表示される日付が時差のぶんずれる。JSTなら9時間後ろにずれ、15時以降に
// 撮った写真が翌日に回る。
func TestNewAssumesLocalWhenTheEXIFDateHasNoOffset(t *testing.T) {
	// time.Local はプロセス全体で1つしかない。書き換えるテストが並列に走ると、
	// 同時に走っている他のテストの時刻解釈まで巻き添えで変わる。実際 -race が
	// 競合として検出する。このテストは t.Parallel() を呼ばない。
	// TZ=UTC の環境ではローカル解釈とUTC解釈が同じになり、この回帰を
	// 検出できなくなる。テスト中だけ固定オフセットに差し替える。
	orig := time.Local
	time.Local = time.FixedZone("JST", 9*60*60)
	t.Cleanup(func() { time.Local = orig })

	// 時差を持たないEXIF日時は、読み取り側からUTCとして渡ってくる。
	p := newWithEXIFDate(time.Date(2025, 5, 24, 9, 8, 31, 0, time.UTC))

	want := time.Date(2025, 5, 24, 9, 8, 31, 0, time.Local)
	require.True(t, p.TakenAt().Equal(want),
		"the EXIF time is read as the local time where it was shot: got=%v want=%v", p.TakenAt(), want)
}

// OffsetTimeOriginal がある写真は本当の瞬間が分かっている。これをローカル
// 時刻として読み直すと、旅行先で撮った写真の時刻を自宅の時差で上書きして
// しまう。ローカルとみなして組み直してよいのは、時差が分からない写真だけ。
func TestNewKeepsTheEXIFOffsetWhenPresent(t *testing.T) {
	// time.Local はプロセス全体で1つしかない。書き換えるテストが並列に走ると、
	// 同時に走っている他のテストの時刻解釈まで巻き添えで変わる。実際 -race が
	// 競合として検出する。このテストは t.Parallel() を呼ばない。
	orig := time.Local
	time.Local = time.FixedZone("JST", 9*60*60)
	t.Cleanup(func() { time.Local = orig })

	// ベルリン(+02:00)で正午に撮った写真
	berlin := time.Date(2023, 8, 4, 12, 0, 0, 0, time.FixedZone("", 2*60*60))

	p := newWithEXIFDate(berlin)

	_, off := p.TakenAt().Zone()
	require.Equal(t, 2*60*60, off,
		"the offset written in the EXIF is kept, not overwritten with JST: got=%v", p.TakenAt())
	require.Equal(t, 12, p.TakenAt().Hour(), "still noon local time: got=%v", p.TakenAt())
}

func TestNewFallsBackToModTimeWithoutAnEXIFDate(t *testing.T) {
	t.Parallel()
	p := newWithEXIFDate(time.Time{})

	require.True(t, p.TakenAt().Equal(testModTime), "a photo with no capture time still stays in the gallery")
}
