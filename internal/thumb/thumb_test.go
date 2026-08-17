package thumb

import (
	"bytes"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const testID = "abcdef0123456789abcdef0123456789"

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

func newTestGenerator(t *testing.T, size int) *Generator {
	t.Helper()
	g, err := NewGenerator(filepath.Join(t.TempDir(), "thumbs"), size)
	require.NoError(t, err)
	return g
}

func decodeThumb(t *testing.T, path string) image.Config {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	cfg, format, err := image.DecodeConfig(f)
	require.NoError(t, err)
	require.Equal(t, "jpeg", format, "サムネイルは常にJPEGで書き出す")
	return cfg
}

func TestRelPathShardsByFirstTwoChars(t *testing.T) {
	require.Equal(t, filepath.Join("ab", testID+".jpg"), RelPath(testID))
}

func TestGenerateScalesLandscapeByLongEdge(t *testing.T) {
	g := newTestGenerator(t, 100)
	src := writeImage(t, t.TempDir(), "a.jpg", 400, 200)

	require.NoError(t, g.Generate(src, testID))

	cfg := decodeThumb(t, g.Path(testID))
	require.Equal(t, 100, cfg.Width)
	require.Equal(t, 50, cfg.Height, "アスペクト比を保つ")
}

func TestGenerateScalesPortraitByLongEdge(t *testing.T) {
	g := newTestGenerator(t, 100)
	src := writeImage(t, t.TempDir(), "a.jpg", 200, 400)

	require.NoError(t, g.Generate(src, testID))

	cfg := decodeThumb(t, g.Path(testID))
	require.Equal(t, 50, cfg.Width)
	require.Equal(t, 100, cfg.Height)
}

func TestGenerateDoesNotUpscale(t *testing.T) {
	g := newTestGenerator(t, 500)
	src := writeImage(t, t.TempDir(), "a.jpg", 40, 20)

	require.NoError(t, g.Generate(src, testID))

	cfg := decodeThumb(t, g.Path(testID))
	require.Equal(t, 40, cfg.Width)
	require.Equal(t, 20, cfg.Height)
}

func TestGenerateAcceptsPNGAndGIF(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.png", "a.gif"} {
		t.Run(name, func(t *testing.T) {
			g := newTestGenerator(t, 100)
			src := writeImage(t, dir, name, 400, 200)

			require.NoError(t, g.Generate(src, testID))

			require.Equal(t, 100, decodeThumb(t, g.Path(testID)).Width)
		})
	}
}

func TestGenerateFailsOnUndecodableFile(t *testing.T) {
	g := newTestGenerator(t, 100)
	src := filepath.Join(t.TempDir(), "broken.jpg")
	require.NoError(t, os.WriteFile(src, []byte("this is not an image"), 0o644))

	err := g.Generate(src, testID)

	require.Error(t, err)
	require.NoFileExists(t, g.Path(testID), "失敗時に中途半端なファイルを残さない")
}

func TestGenerateFailsOnMissingFile(t *testing.T) {
	g := newTestGenerator(t, 100)

	require.Error(t, g.Generate(filepath.Join(t.TempDir(), "nope.jpg"), testID))
}

func TestRemove(t *testing.T) {
	g := newTestGenerator(t, 100)
	src := writeImage(t, t.TempDir(), "a.jpg", 400, 200)
	require.NoError(t, g.Generate(src, testID))

	require.NoError(t, g.Remove(testID))

	require.NoFileExists(t, g.Path(testID))
	require.NoError(t, g.Remove(testID), "存在しないサムネイルの削除はエラーにしない")
}
