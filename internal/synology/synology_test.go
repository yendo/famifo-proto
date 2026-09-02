package synology

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeFile は親ディレクトリごとファイルを書く。
// HasThumb は中身を見ず、通常ファイルかつサイズが0でないことだけを見るので、
// 画像として妥当である必要はない。
func writeFile(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

func TestThumbPathPointsAtTheMediumThumbnail(t *testing.T) {
	require.Equal(t,
		"/photos/2026-08-16/@eaDir/IMG_0428.HEIC/SYNOPHOTO_THUMB_M.jpg",
		ThumbPath("/photos/2026-08-16/IMG_0428.HEIC"))
}

func TestLargePathPointsAtTheXLThumbnail(t *testing.T) {
	require.Equal(t,
		"/photos/2026-08-16/@eaDir/IMG_0428.HEIC/SYNOPHOTO_THUMB_XL.jpg",
		LargePath("/photos/2026-08-16/IMG_0428.HEIC"))
}

func TestHasThumbFindsTheThumbnailSynologyLeftBehind(t *testing.T) {
	src := filepath.Join(t.TempDir(), "a.heic")
	writeFile(t, src, "original") // 中身は問わない。存在だけを見る
	writeFile(t, ThumbPath(src), "borrowed")

	require.True(t, HasThumb(src))
}

func TestHasThumbIsFalseWithoutEaDir(t *testing.T) {
	src := filepath.Join(t.TempDir(), "a.jpg")
	writeFile(t, src, "original")

	require.False(t, HasThumb(src))
}

// DSM 7.3 はHEICをデコードできず、0バイトの .fail を置く。.jpg は作られない。
func TestHasThumbIsFalseWhenOnlyAFailMarkerIsThere(t *testing.T) {
	src := filepath.Join(t.TempDir(), "a.heic")
	writeFile(t, src, "original")
	writeFile(t, filepath.Join(filepath.Dir(ThumbPath(src)), "SYNOPHOTO_THUMB_M.fail"), "")

	require.False(t, HasThumb(src))
}

// 手で消したあとに0バイトの .jpg が残るような状況。配信すると壊れた <img> になる。
func TestHasThumbIsFalseForAnEmptyThumbnail(t *testing.T) {
	src := filepath.Join(t.TempDir(), "a.heic")
	writeFile(t, src, "original")
	writeFile(t, ThumbPath(src), "")

	require.False(t, HasThumb(src))
}
