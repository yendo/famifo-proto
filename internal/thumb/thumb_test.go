package thumb_test

import (
	"bytes"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/media"
	"github.com/yendo/famifo-proto/internal/synology"
	"github.com/yendo/famifo-proto/internal/thumb"
)

// mediaOf はディスク上の src から、インデックスに載る1枚を組み立てる。
// 本番と同じく ModTime が原本の版になる。
func mediaOf(t *testing.T, src string) media.Media {
	t.Helper()
	fi, err := os.Stat(src)
	require.NoError(t, err)
	return media.Restore(src, fi.ModTime(), fi.ModTime())
}

// famifoThumbOf は src の自前のサムネイルを Path 経由で引き当てる。置き場の規則は
// テストから見えないので、Path が借りたものでも原本でもないファイルを返したら、
// それを自前のものとみなす。@eaDir があると Path はそちらを返すため、借りられる
// 状態では使えない。
func famifoThumbOf(t *testing.T, pv *thumb.Provider, src string) (string, bool) {
	t.Helper()
	m := mediaOf(t, src)
	got, _, ok := pv.Path(m)
	if !ok || got == m.Path() || got == synology.ThumbMPath(src) {
		return "", false
	}
	return got, true
}

// requireFamifoThumb は src の自前のサムネイルのパスを返す。無ければ失敗する。
func requireFamifoThumb(t *testing.T, pv *thumb.Provider, src string) string {
	t.Helper()
	got, ok := famifoThumbOf(t, pv, src)
	require.True(t, ok, "%s has no thumbnail of famifo's own", src)
	return got
}

// requireNoFamifoThumb は src の自前のサムネイルが無いことを確かめる。
func requireNoFamifoThumb(t *testing.T, pv *thumb.Provider, src string, msgAndArgs ...any) {
	t.Helper()
	_, ok := famifoThumbOf(t, pv, src)
	require.False(t, ok, msgAndArgs...)
}

func writeImage(t *testing.T, dir, name string, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	switch filepath.Ext(name) {
	case ".png":
		require.NoError(t, png.Encode(&buf, img))
	case ".gif":
		require.NoError(t, gif.Encode(&buf, img, nil))
	default:
		require.NoError(t, jpeg.Encode(&buf, img, nil))
	}
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
	return path
}

// newTestProvider は空の置き場を持つ Provider を返す。
func newTestProvider(t *testing.T) *thumb.Provider {
	t.Helper()
	pv, err := thumb.NewProvider(filepath.Join(t.TempDir(), "thumbs"))
	require.NoError(t, err)
	return pv
}

// provide は Prepare 経由でサムネイルを調達する。一時ディレクトリには
// @eaDir が無いので、断りがなければ必ず自前で生成する枝に入る。
func provide(t *testing.T, pv *thumb.Provider, srcPath string, orientation uint16) error {
	t.Helper()
	return pv.Prepare(mediaOf(t, srcPath), orientation)
}

// writeSynoThumb は srcPath の隣に、Synologyが作った体のサムネイルを置く。
func writeSynoThumb(t *testing.T, srcPath string) {
	t.Helper()
	out := synology.ThumbMPath(srcPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(out), 0o755))
	require.NoError(t, os.WriteFile(out, []byte("eadir thumb"), 0o644))
}

// サムネイルは原本のファイル名を経た名前が付くので、置き場を他ユーザに開かない。
// 根と、生成のときに掘る枝の両方を見る。
func TestGeneratedThumbnailsStayInPrivateDirectories(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "thumbs")
	pv, err := thumb.NewProvider(root)
	require.NoError(t, err)
	src := writeImage(t, t.TempDir(), "a.jpg", 400, 200)

	require.NoError(t, provide(t, pv, src, 1))

	for _, dir := range []string{root, filepath.Dir(requireFamifoThumb(t, pv, src))} {
		fi, err := os.Stat(dir)
		require.NoError(t, err)
		require.Zero(t, fi.Mode().Perm()&0o007, "%s must not be open to other users", dir)
	}
}

// Prepare の3つの結末を押さえる。借りられるなら借り（何も作らない）、借りられず
// 自前で作れるなら作り、どちらも駄目なら何も残さない。
func TestEnsureOnlyGeneratesWhatCannotBeBorrowed(t *testing.T) {
	t.Parallel()
	t.Run("borrows from @eaDir when it can", func(t *testing.T) {
		pv := newTestProvider(t)
		src := writeImage(t, t.TempDir(), "a.jpg", 400, 200)
		writeSynoThumb(t, src)

		require.NoError(t, provide(t, pv, src, 1))

		// 借りられる間は Path が @eaDir を返すので、片づけてから自前の分を見る。
		require.NoError(t, os.RemoveAll(filepath.Dir(filepath.Dir(synology.ThumbMPath(src)))))
		requireNoFamifoThumb(t, pv, src, "makes none of its own when it can borrow")
	})

	t.Run("makes its own when there is nothing to borrow", func(t *testing.T) {
		pv := newTestProvider(t)
		src := writeImage(t, t.TempDir(), "a.jpg", 400, 200)

		require.NoError(t, provide(t, pv, src, 1))

		requireFamifoThumb(t, pv, src)
	})

	t.Run("a HEIC with nothing to borrow leaves nothing behind", func(t *testing.T) {
		pv := newTestProvider(t)
		src := filepath.Join(t.TempDir(), "a.heic")
		require.NoError(t, os.WriteFile(src, []byte("famifo does not decode HEIC"), 0o644))

		require.NoError(t, provide(t, pv, src, 1), "no decode is attempted, so it is not an error")

		requireNoFamifoThumb(t, pv, src)
	})
}

func decodeThumb(t *testing.T, path string) image.Config {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	cfg, format, err := image.DecodeConfig(f)
	require.NoError(t, err)
	require.Equal(t, "jpeg", format, "thumbnails are always written as JPEG")
	return cfg
}

func TestGenerateScalesLandscapeByLongEdge(t *testing.T) {
	t.Parallel()
	pv := newTestProvider(t)
	src := writeImage(t, t.TempDir(), "a.jpg", thumb.MaxEdge*2, thumb.MaxEdge)

	require.NoError(t, provide(t, pv, src, 1))

	cfg := decodeThumb(t, requireFamifoThumb(t, pv, src))
	require.Equal(t, thumb.MaxEdge, cfg.Width)
	require.Equal(t, thumb.MaxEdge/2, cfg.Height, "the aspect ratio is kept")
}

func TestGenerateScalesPortraitByLongEdge(t *testing.T) {
	t.Parallel()
	pv := newTestProvider(t)
	src := writeImage(t, t.TempDir(), "a.jpg", thumb.MaxEdge, thumb.MaxEdge*2)

	require.NoError(t, provide(t, pv, src, 1))

	cfg := decodeThumb(t, requireFamifoThumb(t, pv, src))
	require.Equal(t, thumb.MaxEdge/2, cfg.Width)
	require.Equal(t, thumb.MaxEdge, cfg.Height)
}

func TestGenerateDoesNotUpscale(t *testing.T) {
	t.Parallel()
	pv := newTestProvider(t)
	src := writeImage(t, t.TempDir(), "a.jpg", 40, 20)

	require.NoError(t, provide(t, pv, src, 1))

	cfg := decodeThumb(t, requireFamifoThumb(t, pv, src))
	require.Equal(t, 40, cfg.Width)
	require.Equal(t, 20, cfg.Height)
}

func TestGenerateAcceptsPNGAndGIF(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"a.png", "a.gif"} {
		t.Run(name, func(t *testing.T) {
			pv := newTestProvider(t)
			src := writeImage(t, dir, name, thumb.MaxEdge*2, thumb.MaxEdge)

			require.NoError(t, provide(t, pv, src, 1))

			require.Equal(t, thumb.MaxEdge, decodeThumb(t, requireFamifoThumb(t, pv, src)).Width)
		})
	}
}

func TestGenerateFailsOnUndecodableFile(t *testing.T) {
	t.Parallel()
	pv := newTestProvider(t)
	src := filepath.Join(t.TempDir(), "broken.jpg")
	require.NoError(t, os.WriteFile(src, []byte("this is not an image"), 0o644))

	err := provide(t, pv, src, 1)

	require.Error(t, err)
	requireNoFamifoThumb(t, pv, src, "no half-written file is left behind on failure")
}

func TestGenerateFailsOnMissingFile(t *testing.T) {
	t.Parallel()
	pv := newTestProvider(t)
	// 消えた直後にイベントを拾った状況。行だけあって原本が無い1枚を組み立てる。
	missing := filepath.Join(t.TempDir(), "nope.jpg")
	m := media.Restore(missing, time.Unix(1600000000, 0), time.Unix(1600000000, 0))

	require.Error(t, pv.Prepare(m, 1))
}

func TestRemoveFamifoThumbs(t *testing.T) {
	t.Parallel()
	pv := newTestProvider(t)
	src := writeImage(t, t.TempDir(), "a.jpg", 400, 200)
	require.NoError(t, provide(t, pv, src, 1))

	require.NoError(t, pv.RemoveFamifoThumbs(media.IDFor(src)))

	requireNoFamifoThumb(t, pv, src)
	require.NoError(t, pv.RemoveFamifoThumbs(media.IDFor(src)), "deleting a thumbnail that does not exist is not an error")
}

// TestGenerateAppliesOrientation は、渡されたOrientationがサムネイルの
// 画素に実際に適用されることを確かめる。
//
// image.Decode はEXIFを見ずに生の画素をそのまま返し、jpeg.Encode はEXIFを
// 一切書き出さない。したがってサムネイル生成時に回転を適用しないと、回転情報
// はどこにも残らず失われる。実測では手元の4,495枚中1,230枚(27.4%)が
// Orientation 6/8 で、その全てが横倒しになっていた。
//
// 値が実際のEXIFから来ることは internal/index のテストが押さえる。
//
// 元画像は 16x8（横長）で左上の四分割だけが赤。回転後にその赤がどの隅へ
// 来るかで、寸法の入れ替えだけでなく画素が本当に動いたかまで見分けられる。
func TestGenerateAppliesOrientation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		orientation uint16
		wantW       int
		wantH       int
		markerLeft  bool // 赤が左半分にあるか
		markerTop   bool // 赤が上半分にあるか
	}{
		{"0 unknown", 0, 16, 8, true, true},
		{"1 as is", 1, 16, 8, true, true},
		{"2 mirrored horizontally", 2, 16, 8, false, true},
		{"3 rotated 180", 3, 16, 8, false, false},
		{"4 mirrored vertically", 4, 16, 8, true, false},
		{"5 transposed", 5, 8, 16, true, true},
		{"6 rotated 90 clockwise", 6, 8, 16, false, true},
		{"7 transverse", 7, 8, 16, false, false},
		{"8 rotated 270 clockwise", 8, 8, 16, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pv := newTestProvider(t) // 16x8は縮小されないので画素を直接見られる
			src := writeJPEGImage(t, t.TempDir(), "a.jpg", markerJPEG(16, 8))

			require.NoError(t, provide(t, pv, src, tt.orientation))

			img := decodeThumbImage(t, requireFamifoThumb(t, pv, src))
			b := img.Bounds()
			require.Equalf(t, tt.wantW, b.Dx(),
				"width of the Orientation=%d thumbnail; width and height look unswapped (actually %dx%d)",
				tt.orientation, b.Dx(), b.Dy())
			require.Equalf(t, tt.wantH, b.Dy(),
				"height of the Orientation=%d thumbnail (actually %dx%d)", tt.orientation, b.Dx(), b.Dy())

			// 赤があるべき四分割の中心と、その対角の中心を見る。
			markX, markY := quadrantCenter(b.Dx(), b.Dy(), tt.markerLeft, tt.markerTop)
			oppX, oppY := quadrantCenter(b.Dx(), b.Dy(), !tt.markerLeft, !tt.markerTop)

			require.Truef(t, isRed(t, img, markX, markY),
				"Orientation=%d: red should land in the %s-%s corner but (%d,%d) is not red; "+
					"the dimensions look swapped without the pixels being rotated",
				tt.orientation, side(tt.markerTop, "top", "bottom"), side(tt.markerLeft, "left", "right"), markX, markY)
			require.Falsef(t, isRed(t, img, oppX, oppY),
				"Orientation=%d: the opposite corner (%d,%d) is red; the rotation looks reversed",
				tt.orientation, oppX, oppY)
		})
	}
}

func quadrantCenter(w, h int, left, top bool) (int, int) {
	x, y := w*3/4, h*3/4
	if left {
		x = w / 4
	}
	if top {
		y = h / 4
	}
	return x, y
}

func side(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

// DBを作り直すたびに全サムネイルを作り直すと、4,495枚で37分（NASなら数時間）
// かかる。写真が変わっていないなら既存のものをそのまま使う。
func TestGenerateSkipsWhenTheThumbnailIsUpToDate(t *testing.T) {
	t.Parallel()
	pv := newTestProvider(t)
	dir := t.TempDir()
	src := writeImage(t, dir, "a.jpg", 400, 200)
	require.NoError(t, provide(t, pv, src, 1))

	// 中身を見分けられる印を置く。版が同じなら中身は問わず、そのまま使う。
	own := requireFamifoThumb(t, pv, src)
	marker := []byte("not a real thumbnail")
	require.NoError(t, os.WriteFile(own, marker, 0o644))

	require.NoError(t, provide(t, pv, src, 1))

	got, err := os.ReadFile(requireFamifoThumb(t, pv, src))
	require.NoError(t, err)
	require.Equal(t, marker, got, "not rebuilt while the source file is unchanged")
}

// 写真が差し替えられたらサムネイルは古い。mtimeで判定する。
func TestGenerateRebuildsWhenTheSourceIsNewer(t *testing.T) {
	t.Parallel()
	pv := newTestProvider(t)
	dir := t.TempDir()
	src := writeImage(t, dir, "a.jpg", 40, 20)
	require.NoError(t, provide(t, pv, src, 1))
	require.NoError(t, os.WriteFile(requireFamifoThumb(t, pv, src), []byte("stale"), 0o644))

	// 元ファイルをサムネイルより新しくする
	future := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(src, future, future))

	require.NoError(t, provide(t, pv, src, 1))

	cfg := decodeThumb(t, requireFamifoThumb(t, pv, src))
	require.Equal(t, 40, cfg.Width, "rebuilt when the source is newer")
}

// mtimeは前にしか進まないとは限らない。cp -p や rsync -t でバックアップから
// 写真を戻すと過去へ動く。順序で鮮度を判定すると「サムネイルのほうが新しい」
// ままなので作り直しを見送り、一覧に古い画像が残り続ける。
func TestGenerateRebuildsWhenTheSourceMtimeMovesBackwards(t *testing.T) {
	t.Parallel()
	pv := newTestProvider(t)
	dir := t.TempDir()
	src := writeImage(t, dir, "a.jpg", 40, 20)
	require.NoError(t, provide(t, pv, src, 1))

	// 縦横を入れ替えた写真に差し替えたうえで、mtimeを過去へ動かす
	src = writeImage(t, dir, "a.jpg", 20, 40)
	past := time.Now().Add(-24 * time.Hour)
	require.NoError(t, os.Chtimes(src, past, past))

	require.NoError(t, provide(t, pv, src, 1))

	cfg := decodeThumb(t, requireFamifoThumb(t, pv, src))
	require.Equal(t, 20, cfg.Width, "rebuilt even when the mtime goes back")
	require.Equal(t, 40, cfg.Height)
}

// 版を名前に持つので、写真が差し替わると古い版がそのまま残る。
// 新しい版を置いたあとに掃く。
func TestEnsureRemovesOlderVersions(t *testing.T) {
	t.Parallel()
	pv := newTestProvider(t)
	dir := t.TempDir()
	src := writeImage(t, dir, "a.jpg", 400, 200)
	require.NoError(t, provide(t, pv, src, 1))
	older := requireFamifoThumb(t, pv, src)

	src = writeImage(t, dir, "a.jpg", 200, 400)
	future := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(src, future, future))
	require.NoError(t, provide(t, pv, src, 1))

	require.NotEqual(t, older, requireFamifoThumb(t, pv, src), "the new version stays")
	require.NoFileExists(t, older, "the old version is cleaned up")
}

// 生成に失敗しても古い版は消さない。新しいものができるまでの控えとして
// 働いており、先に消すと一覧のタイルが割れる。
func TestEnsureKeepsTheOlderVersionWhenGenerationFails(t *testing.T) {
	t.Parallel()
	pv := newTestProvider(t)
	dir := t.TempDir()
	src := writeImage(t, dir, "a.jpg", 400, 200)
	require.NoError(t, provide(t, pv, src, 1))
	older := requireFamifoThumb(t, pv, src)

	// コピー途中の壊れたファイルを掴んだ状況を模す
	require.NoError(t, os.WriteFile(src, []byte("this is not an image"), 0o644))
	future := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(src, future, future))

	require.Error(t, provide(t, pv, src, 1))

	require.FileExists(t, older, "the old version is kept on failure")
}

// Synologyのバックグラウンド索引が後からサムネイルを作ったあとで取り込み直すと、
// 借りるほうへ切り替わる。自前で作ったものは用済みになる。
func TestEnsureRemovesTheOwnThumbWhenSwitchingToEaDir(t *testing.T) {
	t.Parallel()
	pv := newTestProvider(t)
	src := writeImage(t, t.TempDir(), "a.jpg", 400, 200)
	require.NoError(t, provide(t, pv, src, 1))
	own := requireFamifoThumb(t, pv, src)

	writeSynoThumb(t, src)

	require.NoError(t, provide(t, pv, src, 1))

	require.NoFileExists(t, own, "its own copy goes once it switches to the borrowed one")
}
