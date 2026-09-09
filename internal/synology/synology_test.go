package synology_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/synology"
)

// writeFile は親ディレクトリごとファイルを書く。
// synology.HasThumbM は中身を見ず、通常ファイルかつサイズが0でないことだけを見るので、
// 画像として妥当である必要はない。
func writeFile(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

func TestThumbMPathPointsAtTheMediumThumbnail(t *testing.T) {
	t.Parallel()
	require.Equal(t,
		"/photos/2026-08-16/@eaDir/IMG_0428.HEIC/SYNOPHOTO_THUMB_M.jpg",
		synology.ThumbMPath("/photos/2026-08-16/IMG_0428.HEIC"))
}

func TestThumbXLPathPointsAtTheXLThumbnail(t *testing.T) {
	t.Parallel()
	require.Equal(t,
		"/photos/2026-08-16/@eaDir/IMG_0428.HEIC/SYNOPHOTO_THUMB_XL.jpg",
		synology.ThumbXLPath("/photos/2026-08-16/IMG_0428.HEIC"))
}

func TestHasThumbMFindsTheThumbnailSynologyLeftBehind(t *testing.T) {
	t.Parallel()
	src := filepath.Join(t.TempDir(), "a.heic")
	writeFile(t, src, "original") // 中身は問わない。存在だけを見る
	writeFile(t, synology.ThumbMPath(src), "borrowed")

	require.True(t, synology.HasThumbM(src))
}

func TestHasThumbMIsFalseWithoutEaDir(t *testing.T) {
	t.Parallel()
	src := filepath.Join(t.TempDir(), "a.jpg")
	writeFile(t, src, "original")

	require.False(t, synology.HasThumbM(src))
}

// DSM 7.3 はHEICをデコードできず、0バイトの .fail を置く。.jpg は作られない。
func TestHasThumbMIsFalseWhenOnlyAFailMarkerIsThere(t *testing.T) {
	t.Parallel()
	src := filepath.Join(t.TempDir(), "a.heic")
	writeFile(t, src, "original")
	writeFile(t, filepath.Join(filepath.Dir(synology.ThumbMPath(src)), "SYNOPHOTO_THUMB_M.fail"), "")

	require.False(t, synology.HasThumbM(src))
}

// 手で消したあとに0バイトの .jpg が残るような状況。配信すると壊れた <img> になる。
func TestHasThumbMIsFalseForAnEmptyThumbnail(t *testing.T) {
	t.Parallel()
	src := filepath.Join(t.TempDir(), "a.heic")
	writeFile(t, src, "original")
	writeFile(t, synology.ThumbMPath(src), "")

	require.False(t, synology.HasThumbM(src))
}

func TestFilmPathPointsAtTheTranscodedVideo(t *testing.T) {
	t.Parallel()
	require.Equal(t,
		"/photos/2026-09/@eaDir/PXL_20260909.mp4/SYNOPHOTO_FILM_H.mp4",
		synology.FilmPath("/photos/2026-09/PXL_20260909.mp4"))
}

func TestHasFilmFindsTheTranscodeSynologyLeftBehind(t *testing.T) {
	t.Parallel()
	src := filepath.Join(t.TempDir(), "clip.mp4")
	writeFile(t, src, "hevc original")
	writeFile(t, synology.FilmPath(src), "h264 transcode")

	require.True(t, synology.HasFilm(src))
}

func TestHasFilmIsFalseWithoutEaDir(t *testing.T) {
	t.Parallel()
	src := filepath.Join(t.TempDir(), "clip.mp4")
	writeFile(t, src, "hevc original")

	require.False(t, synology.HasFilm(src))
}

// サムネイルがあっても変換版があるとは限らない。実測した @eaDir には
// SYNOPHOTO_THUMB_M.jpg があるのに SYNOPHOTO_FILM.fail があった。別の工程なので
// 片方だけ成功しうる。
func TestHasFilmIsFalseEvenWhenAThumbnailExists(t *testing.T) {
	t.Parallel()
	src := filepath.Join(t.TempDir(), "clip.mp4")
	writeFile(t, src, "hevc original")
	writeFile(t, synology.ThumbMPath(src), "borrowed")

	require.False(t, synology.HasFilm(src))
}

// DSMは変換に失敗すると0バイトの .fail を置く。空のファイルは借りない。
func TestHasFilmIsFalseForAnEmptyTranscode(t *testing.T) {
	t.Parallel()
	src := filepath.Join(t.TempDir(), "clip.mp4")
	writeFile(t, src, "hevc original")
	writeFile(t, synology.FilmPath(src), "")

	require.False(t, synology.HasFilm(src))
}

func TestIsManagedDirCoversSynologysOwnDirectories(t *testing.T) {
	t.Parallel()
	require.True(t, synology.IsManagedDir("@eaDir"))
	require.True(t, synology.IsManagedDir("#recycle"))
	require.False(t, synology.IsManagedDir("2026-08-16"))
}

// 走査は fs.SkipDir で降りずに済むが、fsnotify のイベントは個々のパスで
// 届くため、途中に挟まっているかを見る必要がある。
func TestInManagedDirFindsTheDirectoryAnywhereInThePath(t *testing.T) {
	t.Parallel()
	require.True(t, synology.InManagedDir("/photos/@eaDir/IMG_0001.jpg/SYNOPHOTO_THUMB_M.jpg"))
	require.True(t, synology.InManagedDir("/photos/#recycle/deleted.jpg"))
	require.False(t, synology.InManagedDir("/photos/2026-08-16/IMG_0001.jpg"))
}
