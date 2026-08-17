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
	require.Equal(t, dir, got.PhotoDir)
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
