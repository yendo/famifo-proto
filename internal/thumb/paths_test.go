package thumb_test

// 配信するファイルの選択を確かめる。出どころはDBに持たずディスクの状態で決めるので、
// @eaDir と自前の置き場に実際にファイルを置いて確かめる。

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/media"
	"github.com/yendo/famifo-proto/internal/synology"
	"github.com/yendo/famifo-proto/internal/thumb"
)

type pathFixture struct {
	pv       *thumb.Provider
	photoDir string
}

func newPathFixture(t *testing.T) *pathFixture {
	t.Helper()
	base := t.TempDir()
	pv, err := thumb.NewProvider(filepath.Join(base, "thumbs"))
	require.NoError(t, err)
	photoDir := filepath.Join(base, "photos")
	require.NoError(t, os.MkdirAll(photoDir, 0o755))
	return &pathFixture{pv: pv, photoDir: photoDir}
}

// addPhoto は原本を1つ置いて、その1枚を返す。
// どのテストも中身は見ないので、画像として妥当である必要はない。
func (f *pathFixture) addPhoto(t *testing.T, name string) media.Media {
	t.Helper()
	path := filepath.Join(f.photoDir, name)
	require.NoError(t, os.WriteFile(path, []byte("original"), 0o644))
	return photoOf(t, path)
}

// borrowable は Synologyが作った体のMとXLを写真の隣に置く。
func (f *pathFixture) borrowable(t *testing.T, p media.Media) {
	t.Helper()
	writeFileAt(t, synology.ThumbMPath(p.Path()), "eadir m")
	writeFileAt(t, synology.ThumbXLPath(p.Path()), "eadir xl")
}

// generated は自前で生成した体のサムネイルを、その版の置き場に置く。
func (f *pathFixture) generated(t *testing.T, p media.Media) string {
	t.Helper()
	out := f.pv.GeneratedPath(p)
	writeFileAt(t, out, "generated thumb")
	return out
}

func writeFileAt(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

func TestSmallPathPrefersTheBorrowedThumb(t *testing.T) {
	t.Parallel()
	f := newPathFixture(t)
	p := f.addPhoto(t, "a.jpg")
	own := f.generated(t, p)
	f.borrowable(t, p)

	got, contentType, ok := f.pv.SmallPath(p)

	require.True(t, ok)
	require.Equal(t, synology.ThumbMPath(p.Path()), got,
		"in a real library nearly every photo has @eaDir, so it is looked at first")
	require.NotEqual(t, own, got)
	require.Equal(t, "image/jpeg", contentType)
}

func TestSmallPathUsesTheGeneratedThumbWhenNothingToBorrow(t *testing.T) {
	t.Parallel()
	f := newPathFixture(t)
	p := f.addPhoto(t, "a.jpg")
	own := f.generated(t, p)

	got, contentType, ok := f.pv.SmallPath(p)

	require.True(t, ok)
	require.Equal(t, own, got)
	require.Equal(t, "image/jpeg", contentType)
}

// ブラウザが表示できる形式なら、サムネイルが無くても原本を出せばタイルになる。
func TestSmallPathFallsBackToTheOriginal(t *testing.T) {
	t.Parallel()
	f := newPathFixture(t)
	p := f.addPhoto(t, "a.jpg")

	got, contentType, ok := f.pv.SmallPath(p)

	require.True(t, ok)
	require.Equal(t, p.Path(), got)
	require.Equal(t, "image/jpeg", contentType)
}

// HEICはブラウザが表示できないので、原本を出しても割れたタイルになるだけである。
// 出せるものが無いことを伝えて、配信側にプレースホルダを出させる。
func TestSmallPathHasNothingToShowForAnUnborrowedHEIC(t *testing.T) {
	t.Parallel()
	f := newPathFixture(t)
	p := f.addPhoto(t, "a.heic")

	got, contentType, ok := f.pv.SmallPath(p)

	require.False(t, ok)
	require.Empty(t, got)
	require.Empty(t, contentType)
}

// 取り込みのあとでDSMがサムネイルを作った場合。出どころをDBに焼いていたころは、
// 原本のmtimeが動かない限り再取り込みされないため、永久に反映されなかった。
func TestSmallPathSeesAThumbThatAppearsAfterIndexing(t *testing.T) {
	t.Parallel()
	f := newPathFixture(t)
	p := f.addPhoto(t, "a.heic")
	_, _, ok := f.pv.SmallPath(p)
	require.False(t, ok, "there is nothing to serve yet")

	f.borrowable(t, p)

	got, _, ok := f.pv.SmallPath(p)
	require.True(t, ok)
	require.Equal(t, synology.ThumbMPath(p.Path()), got,
		"the next request serves the borrowed one without reindexing")
}

// 名前に版が入っているので、別の版のサムネイルは引き当たらない。
func TestSmallPathIgnoresAThumbFromAnotherVersion(t *testing.T) {
	t.Parallel()
	f := newPathFixture(t)
	p := f.addPhoto(t, "a.jpg")
	stale := media.Restore(p.Path(), p.TakenAt(), p.ModTime().Add(-time.Hour))
	writeFileAt(t, f.pv.GeneratedPath(stale), "a thumbnail of an older version")

	got, _, ok := f.pv.SmallPath(p)

	require.True(t, ok)
	require.Equal(t, p.Path(), got, "a different version counts as missing and falls back to the original")
}

// 1ディレクトリにファイルが集中しないよう、IDの先頭2文字で分割する。
func TestGeneratedPathShardsByTheFirstTwoCharsOfTheID(t *testing.T) {
	t.Parallel()
	f := newPathFixture(t)
	p := f.addPhoto(t, "a.jpg")

	got := f.pv.GeneratedPath(p)

	require.Equal(t, p.ID()[:2], filepath.Base(filepath.Dir(got)))
	require.Equal(t, ".jpg", filepath.Ext(got), "its own output is always JPEG")
}

// 名前に元画像の版が入るので、写真が差し替われば別のファイルを指す。
// 鮮度を「サムネイルのほうが新しいか」で測らずに済ませるための土台。
func TestGeneratedPathVariesWithTheSourceVersion(t *testing.T) {
	t.Parallel()
	f := newPathFixture(t)
	p := f.addPhoto(t, "a.jpg")
	older := media.Restore(p.Path(), p.TakenAt(), p.ModTime().Add(-time.Hour))

	require.NotEqual(t, f.pv.GeneratedPath(p), f.pv.GeneratedPath(older),
		"a different version gets a different name")
	require.Equal(t,
		filepath.Dir(f.pv.GeneratedPath(p)), filepath.Dir(f.pv.GeneratedPath(older)),
		"the directory is the same")
}

// XLに差し替えるのは「自前でデコードできない形式で、かつ借りられる」ときだけ。
func TestLargePathSwapsInTheXLOnlyForBorrowedOpaquePhotos(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		file     string
		borrowed bool
		wantXL   bool
		wantType string
	}{
		{"HEIC + can borrow -> Synology's XL", "a.heic", true, true, "image/jpeg"},
		{"HEIC + nothing to borrow -> the original (only Safari shows it, but there is nothing else)",
			"a.heic", false, false, "image/heic"},
		{"JPEG + can borrow -> the original (borrowing is only for what cannot be shown)",
			"a.jpg", true, false, "image/jpeg"},
		{"JPEG + nothing to borrow -> the original", "a.jpg", false, false, "image/jpeg"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPathFixture(t)
			p := f.addPhoto(t, tt.file)
			if tt.borrowed {
				f.borrowable(t, p)
			}

			got, contentType := f.pv.LargePath(p)

			want := p.Path()
			if tt.wantXL {
				want = synology.ThumbXLPath(p.Path())
			}
			require.Equal(t, want, got)
			require.Equal(t, tt.wantType, contentType)
		})
	}
}

// HEVCの原本はハードウェアデコーダを持たない端末で再生できない。Synologyが作った
// H.264版があるならそれを配る。HEICで XL を借りているのと同じ構えである。
func TestLargePathBorrowsTheTranscodedVideo(t *testing.T) {
	t.Parallel()
	f := newPathFixture(t)
	m := f.addPhoto(t, "clip.mp4")
	film := synology.FilmPath(m.Path())
	writeFileAt(t, film, "h264 transcode")

	path, ct := f.pv.LargePath(m)

	require.Equal(t, film, path)
	require.Equal(t, "video/mp4", ct)
}

// 借りるものが無ければ原本に落ちる。再生できるかは端末次第になる。
func TestLargePathFallsBackToTheOriginalVideo(t *testing.T) {
	t.Parallel()
	f := newPathFixture(t)
	m := f.addPhoto(t, "clip.mov")

	path, ct := f.pv.LargePath(m)

	require.Equal(t, m.Path(), path)
	require.Equal(t, "video/quicktime", ct)
}

// サムネイルがあっても変換版があるとは限らない。実測した @eaDir には
// SYNOPHOTO_THUMB_M.jpg があるのに SYNOPHOTO_FILM.fail があった。写真のXLのように
// 一方から他方を導けないので、動画では静止画のXLを掴んでしまってもいけない。
func TestLargePathDoesNotBorrowTheXLForAVideo(t *testing.T) {
	t.Parallel()
	f := newPathFixture(t)
	m := f.addPhoto(t, "clip.mp4")
	f.borrowable(t, m)

	path, ct := f.pv.LargePath(m)

	require.Equal(t, m.Path(), path, "an XL still is not what you play")
	require.Equal(t, "video/mp4", ct)
}

// 動画のタイルは借りたサムネイルになる。写真と同じ経路が拡張子を見ずに効く。
func TestSmallPathBorrowsTheVideoThumbnail(t *testing.T) {
	t.Parallel()
	f := newPathFixture(t)
	m := f.addPhoto(t, "clip.mp4")
	f.borrowable(t, m)

	path, ct, ok := f.pv.SmallPath(m)

	require.True(t, ok)
	require.Equal(t, synology.ThumbMPath(m.Path()), path)
	require.Equal(t, "image/jpeg", ct)
}

// 借りるものが無い動画には出せる絵が無い。原本を出しても再生はされないので、
// 配信側がプレースホルダに差し替える。
func TestSmallPathHasNothingForAVideoWithoutABorrowedThumbnail(t *testing.T) {
	t.Parallel()
	f := newPathFixture(t)
	m := f.addPhoto(t, "clip.mp4")

	_, _, ok := f.pv.SmallPath(m)

	require.False(t, ok)
}
