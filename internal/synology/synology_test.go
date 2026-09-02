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

func TestIsManagedDirCoversSynologysOwnDirectories(t *testing.T) {
	require.True(t, IsManagedDir("@eaDir"))
	require.True(t, IsManagedDir("#recycle"))
	require.False(t, IsManagedDir("2026-08-16"))
}

// 走査は fs.SkipDir で降りずに済むが、fsnotify のイベントは個々のパスで
// 届くため、途中に挟まっているかを見る必要がある。
func TestInManagedDirFindsTheDirectoryAnywhereInThePath(t *testing.T) {
	require.True(t, InManagedDir("/photos/@eaDir/IMG_0001.jpg/SYNOPHOTO_THUMB_M.jpg"))
	require.True(t, InManagedDir("/photos/#recycle/deleted.jpg"))
	require.False(t, InManagedDir("/photos/2026-08-16/IMG_0001.jpg"))
}
