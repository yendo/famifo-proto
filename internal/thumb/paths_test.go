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
	mediaDir string
}

func newPathFixture(t *testing.T) *pathFixture {
	t.Helper()
	base := t.TempDir()
	pv, err := thumb.NewProvider(filepath.Join(base, "thumbs"))
	require.NoError(t, err)
	mediaDir := filepath.Join(base, "items")
	require.NoError(t, os.MkdirAll(mediaDir, 0o755))
	return &pathFixture{pv: pv, mediaDir: mediaDir}
}

// addMedia は原本を1つ置いて、その1枚を返す。
// どのテストも中身は見ないので、画像として妥当である必要はない。
func (f *pathFixture) addMedia(t *testing.T, name string) media.Media {
	t.Helper()
	path := filepath.Join(f.mediaDir, name)
	require.NoError(t, os.WriteFile(path, []byte("original"), 0o644))
	return mediaOf(t, path)
}

// borrowable は Synologyが作った体のMとXLを写真の隣に置く。
func (f *pathFixture) borrowable(t *testing.T, m media.Media) {
	t.Helper()
	writeFileAt(t, synology.ThumbMPath(m.Path()), "eadir m")
	writeFileAt(t, synology.ThumbXLPath(m.Path()), "eadir xl")
}

// generated は画像として妥当な原本を置いて Prepare を通し、自前のサムネイルを
// 作らせる。返すのはその1枚と、Path が引き当てた自前のサムネイルのパス。
func (f *pathFixture) generated(t *testing.T, name string) (media.Media, string) {
	t.Helper()
	src := writeImage(t, f.mediaDir, name, 40, 20)
	require.NoError(t, f.pv.Prepare(mediaOf(t, src), 1))
	return mediaOf(t, src), requireFamifoThumb(t, f.pv, src)
}

func writeFileAt(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

func TestPathPrefersTheBorrowedThumb(t *testing.T) {
	t.Parallel()
	f := newPathFixture(t)
	m, own := f.generated(t, "a.jpg")
	f.borrowable(t, m)

	got, contentType, ok := f.pv.Path(m)

	require.True(t, ok)
	require.Equal(t, synology.ThumbMPath(m.Path()), got,
		"in a real library nearly every item has @eaDir, so it is looked at first")
	require.NotEqual(t, own, got)
	require.Equal(t, "image/jpeg", contentType)
}

func TestPathUsesTheGeneratedThumbWhenNothingToBorrow(t *testing.T) {
	t.Parallel()
	f := newPathFixture(t)
	m, own := f.generated(t, "a.jpg")

	got, contentType, ok := f.pv.Path(m)

	require.True(t, ok)
	require.Equal(t, own, got)
	require.Equal(t, "image/jpeg", contentType)
}

// ブラウザが表示できる形式なら、サムネイルが無くても原本を出せばタイルになる。
func TestPathFallsBackToTheOriginal(t *testing.T) {
	t.Parallel()
	f := newPathFixture(t)
	m := f.addMedia(t, "a.jpg")

	got, contentType, ok := f.pv.Path(m)

	require.True(t, ok)
	require.Equal(t, m.Path(), got)
	require.Equal(t, "image/jpeg", contentType)
}

// HEICはブラウザが表示できないので、原本を出しても割れたタイルになるだけである。
// 出せるものが無いことを伝えて、配信側にプレースホルダを出させる。
func TestPathHasNothingToShowForAnUnborrowedHEIC(t *testing.T) {
	t.Parallel()
	f := newPathFixture(t)
	m := f.addMedia(t, "a.heic")

	got, contentType, ok := f.pv.Path(m)

	require.False(t, ok)
	require.Empty(t, got)
	require.Empty(t, contentType)
}

// 取り込みのあとでDSMがサムネイルを作った場合。出どころをDBに焼いていたころは、
// 原本のmtimeが動かない限り再取り込みされないため、永久に反映されなかった。
func TestPathSeesAThumbThatAppearsAfterIndexing(t *testing.T) {
	t.Parallel()
	f := newPathFixture(t)
	m := f.addMedia(t, "a.heic")
	_, _, ok := f.pv.Path(m)
	require.False(t, ok, "there is nothing to serve yet")

	f.borrowable(t, m)

	got, _, ok := f.pv.Path(m)
	require.True(t, ok)
	require.Equal(t, synology.ThumbMPath(m.Path()), got,
		"the next request serves the borrowed one without reindexing")
}

// 名前に版が入っているので、写真が差し替わったあと取り込み直す前に、
// 古い版のサムネイルが引き当たることはない。
func TestPathIgnoresAThumbFromAnotherVersion(t *testing.T) {
	t.Parallel()
	f := newPathFixture(t)
	m, _ := f.generated(t, "a.jpg")
	future := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(m.Path(), future, future))
	m = mediaOf(t, m.Path())

	got, _, ok := f.pv.Path(m)

	require.True(t, ok)
	require.Equal(t, m.Path(), got, "a different version counts as missing and falls back to the original")
}

// 動画のタイルは借りたサムネイルになる。写真と同じ経路が拡張子を見ずに効く。
func TestPathBorrowsTheVideoThumbnail(t *testing.T) {
	t.Parallel()
	f := newPathFixture(t)
	m := f.addMedia(t, "clip.mp4")
	f.borrowable(t, m)

	path, ct, ok := f.pv.Path(m)

	require.True(t, ok)
	require.Equal(t, synology.ThumbMPath(m.Path()), path)
	require.Equal(t, "image/jpeg", ct)
}

// 借りるものが無い動画には出せる絵が無い。原本を出しても再生はされないので、
// 配信側がプレースホルダに差し替える。
func TestPathHasNothingForAVideoWithoutABorrowedThumbnail(t *testing.T) {
	t.Parallel()
	f := newPathFixture(t)
	m := f.addMedia(t, "clip.mp4")

	_, _, ok := f.pv.Path(m)

	require.False(t, ok)
}
