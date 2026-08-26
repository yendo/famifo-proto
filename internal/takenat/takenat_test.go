package takenat

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResolveUsesEXIFWhenPresent(t *testing.T) {
	dir := t.TempDir()
	path := writeJPEGWithEXIF(t, dir, "a.jpg",
		time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC))
	mtime := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

	got := Resolve(path, mtime)

	// 壁時計で比べる。瞬間で比べると実行環境のタイムゾーンに依存し、
	// 「EXIFをUTCとして読む」という誤った挙動を固定してしまう。
	// 時差の解釈そのものは TestResolveReadsEXIFAsLocalWallClock が押さえる。
	require.Equal(t, "2021-03-04 05:06:07", got.Format("2006-01-02 15:04:05"),
		"EXIFの撮影日時を優先する")
}

// EXIFのDateTimeOriginalは時差情報を持たない「カメラの壁時計」である。
// 撮影地のローカル時刻として読まないと、表示される日付が時差のぶんずれる。
// JSTなら9時間後ろにずれ、15時以降に撮った写真が翌日に回る。
func TestResolveReadsEXIFAsLocalWallClock(t *testing.T) {
	// TZ=UTC の環境ではローカル解釈とUTC解釈が同じになり、この回帰を
	// 検出できなくなる。テスト中だけ固定オフセットに差し替える。
	orig := time.Local
	time.Local = time.FixedZone("JST", 9*60*60)
	t.Cleanup(func() { time.Local = orig })

	dir := t.TempDir()
	// 引数はEXIFに書き込む壁時計を決めるだけで、その Location は使われない。
	path := writeJPEGWithEXIF(t, dir, "a.jpg",
		time.Date(2025, 5, 24, 9, 8, 31, 0, time.UTC))
	mtime := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

	got := Resolve(path, mtime)

	want := time.Date(2025, 5, 24, 9, 8, 31, 0, time.Local)
	require.True(t, got.Equal(want),
		"EXIFの壁時計をローカル時刻として解釈すること: got=%v want=%v", got, want)
}

func TestResolveFallsBackToModTimeWhenNoEXIF(t *testing.T) {
	dir := t.TempDir()
	path := writeJPEGWithoutEXIF(t, dir, "a.jpg")
	mtime := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

	got := Resolve(path, mtime)

	require.True(t, got.Equal(mtime))
}

func TestResolveFallsBackForNonEXIFFormats(t *testing.T) {
	dir := t.TempDir()
	// GIFやWebPはEXIFを持たない。デコードが失敗してもmtimeで必ず決まること。
	path := filepath.Join(dir, "a.gif")
	require.NoError(t, os.WriteFile(path, []byte("GIF89a not really a gif"), 0o644))
	mtime := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

	got := Resolve(path, mtime)

	require.True(t, got.Equal(mtime))
}

func TestResolveFallsBackWhenFileMissing(t *testing.T) {
	mtime := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

	got := Resolve(filepath.Join(t.TempDir(), "nope.jpg"), mtime)

	require.True(t, got.Equal(mtime))
}

// OffsetTimeOriginal がある写真は本当の瞬間が分かっている。これをローカル
// 時刻として読み直すと、旅行先で撮った写真の時刻を自宅の時差で上書きして
// しまう。壁時計として組み直してよいのは、時差が分からない写真だけ。
func TestResolveKeepsEXIFOffsetWhenPresent(t *testing.T) {
	orig := time.Local
	time.Local = time.FixedZone("JST", 9*60*60)
	t.Cleanup(func() { time.Local = orig })

	dir := t.TempDir()
	// ベルリン(+02:00)で正午に撮った写真
	path := writeJPEGWithEXIFOffset(t, dir, "a.jpg",
		time.Date(2023, 8, 4, 12, 0, 0, 0, time.UTC), "+02:00")
	mtime := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

	got := Resolve(path, mtime)

	_, off := got.Zone()
	require.Equal(t, 2*60*60, off,
		"EXIFが示す時差を維持すること（JSTで上書きしない）: got=%v", got)
	require.Equal(t, 12, got.Hour(), "壁時計は現地の12時のまま: got=%v", got)
}
