package config

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseUsesDefaults(t *testing.T) {
	dir := t.TempDir()

	got, err := Parse([]string{"-dir", dir}, io.Discard)

	require.NoError(t, err)
	require.Equal(t, []string{dir}, got.PhotoDirs)
	require.Equal(t, "./famifo-data", got.DataDir)
	require.Equal(t, ":8080", got.Addr)
	require.Equal(t, 480, got.ThumbSize)
}

func TestParseOverridesEveryFlag(t *testing.T) {
	dir := t.TempDir()

	got, err := Parse([]string{
		"-dir", dir, "-data", "/var/famifo", "-addr", "192.168.1.10:9000", "-thumb", "320",
	}, io.Discard)

	require.NoError(t, err)
	require.Equal(t, "/var/famifo", got.DataDir)
	require.Equal(t, "192.168.1.10:9000", got.Addr)
	require.Equal(t, 320, got.ThumbSize)
}

func TestParseRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.txt")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))

	tests := map[string][]string{
		"dirが未指定":        {},
		"dirが存在しない":      {"-dir", filepath.Join(dir, "nope")},
		"dirがディレクトリではない": {"-dir", file},
		"thumbが小さすぎる":    {"-dir", dir, "-thumb", "0"},
		"thumbが大きすぎる":    {"-dir", dir, "-thumb", "4097"},
		"addrが空":         {"-dir", dir, "-addr", ""},
		"dataがdirの中":     {"-dir", dir, "-data", filepath.Join(dir, "famifo-data")},
		"dataがdirと同じ":    {"-dir", dir, "-data", dir},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(args, io.Discard)
			require.Error(t, err)
		})
	}
}

func TestParseAcceptsSiblingDataDir(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "photos")
	data := filepath.Join(base, "photos-data")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	// "photos-data" は文字列としては "photos" で始まるが、兄弟ディレクトリであり
	// 中には無い。プレフィックス比較ではなくパス階層で判定できていることの確認。
	_, err := Parse([]string{"-dir", dir, "-data", data}, io.Discard)

	require.NoError(t, err)
}

func TestDerivedPaths(t *testing.T) {
	c := Config{DataDir: "/var/famifo"}

	require.Equal(t, "/var/famifo/famifo.db", c.DBPath())
	require.Equal(t, "/var/famifo/thumbs", c.ThumbDir())
}

// -version はバージョンを表示して終わるだけなので、-dir を要求しない。
// 設定の検証まで進むと「-dir は必須です」で落ちてしまう。
func TestParseVersionShortCircuitsValidation(t *testing.T) {
	_, err := Parse([]string{"-version"}, io.Discard)

	require.ErrorIs(t, err, ErrVersionRequested)
}

func TestParseSplitsDirOnTheListSeparator(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()

	got, err := Parse([]string{"-dir", a + string(filepath.ListSeparator) + b}, io.Discard)

	require.NoError(t, err)
	require.Equal(t, []string{a, b}, got.PhotoDirs)
}

func TestParseRejectsDuplicateRoots(t *testing.T) {
	dir := t.TempDir()

	_, err := Parse([]string{"-dir", dir + string(filepath.ListSeparator) + dir}, io.Discard)

	require.Error(t, err, "同じルートを2回走査しても無駄なだけ")
}

// 入れ子のルートは同じファイルを2回走査し、サムネイルを2回作る。
func TestParseRejectsNestedRoots(t *testing.T) {
	outer := t.TempDir()
	inner := filepath.Join(outer, "sub")
	require.NoError(t, os.MkdirAll(inner, 0o755))

	_, err := Parse([]string{"-dir", outer + string(filepath.ListSeparator) + inner}, io.Discard)

	require.Error(t, err)
}

// -data はどのルートの中にあってもいけない。中にあるとサムネイルを
// 走査対象として拾い、それのサムネイルを作る、という自己増殖が起きる。
func TestParseRejectsDataInsideAnyRoot(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()

	_, err := Parse([]string{
		"-dir", a + string(filepath.ListSeparator) + b,
		"-data", filepath.Join(b, "famifo-data"),
	}, io.Discard)

	require.Error(t, err, "2つ目のルートの中でも弾くこと")
}

// ':' を含むパスを渡すと分割で壊れる。なぜそうなったか読めるエラーにする。
func TestParseExplainsHowDirWasSplit(t *testing.T) {
	_, err := Parse([]string{"-dir", "/no/such/2024:05:24"}, io.Discard)

	require.Error(t, err)
	require.Contains(t, err.Error(), "2024",
		"分割結果を示して、区切り文字で切れたことが分かるようにする")
}
