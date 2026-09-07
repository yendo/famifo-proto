package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/config"
)

func TestValidateRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.txt")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))

	tests := map[string]config.Config{
		"dirが未指定": {
			DataDir: "./famifo-data", Addr: ":8080", ScanWorkers: 1, ScanInterval: time.Hour,
		},
		"dirが存在しない": {
			PhotoDirs: []string{filepath.Join(dir, "nope")},
			DataDir:   "./famifo-data", Addr: ":8080", ScanWorkers: 1, ScanInterval: time.Hour,
		},
		"dirがディレクトリではない": {
			PhotoDirs: []string{file},
			DataDir:   "./famifo-data", Addr: ":8080", ScanWorkers: 1, ScanInterval: time.Hour,
		},
		"addrが空": {
			PhotoDirs: []string{dir},
			DataDir:   "./famifo-data", Addr: "", ScanWorkers: 1, ScanInterval: time.Hour,
		},
		"dataがdirの中": {
			PhotoDirs: []string{dir},
			DataDir:   filepath.Join(dir, "famifo-data"), Addr: ":8080", ScanWorkers: 1, ScanInterval: time.Hour,
		},
		"dataがdirと同じ": {
			PhotoDirs: []string{dir},
			DataDir:   dir, Addr: ":8080", ScanWorkers: 1, ScanInterval: time.Hour,
		},
		"scan-workersが0": {
			PhotoDirs: []string{dir},
			DataDir:   "./famifo-data", Addr: ":8080", ScanWorkers: 0, ScanInterval: time.Hour,
		},
		"scan-workersが負": {
			PhotoDirs: []string{dir},
			DataDir:   "./famifo-data", Addr: ":8080", ScanWorkers: -1, ScanInterval: time.Hour,
		},
		// 0 だと待たずに回り続ける。走査が止まらなくなるので弾く。
		"scan-intervalが0": {
			PhotoDirs: []string{dir},
			DataDir:   "./famifo-data", Addr: ":8080", ScanWorkers: 1, ScanInterval: 0,
		},
		"scan-intervalが負": {
			PhotoDirs: []string{dir},
			DataDir:   "./famifo-data", Addr: ":8080", ScanWorkers: 1, ScanInterval: -time.Second,
		},
	}
	for name, c := range tests {
		t.Run(name, func(t *testing.T) {
			require.Error(t, c.Validate())
		})
	}
}

func TestValidateAcceptsSiblingDataDir(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "photos")
	data := filepath.Join(base, "photos-data")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	// "photos-data" は文字列としては "photos" で始まるが、兄弟ディレクトリであり
	// 中には無い。プレフィックス比較ではなくパス階層で判定できていることの確認。
	c := config.Config{PhotoDirs: []string{dir}, DataDir: data, Addr: ":8080", ScanWorkers: 1, ScanInterval: time.Hour}

	require.NoError(t, c.Validate())
}

func TestDerivedPaths(t *testing.T) {
	c := config.Config{DataDir: "/var/famifo"}

	require.Equal(t, "/var/famifo/famifo.db", c.DBPath())
	require.Equal(t, "/var/famifo/thumbs", c.ThumbDir())
}

func TestValidateRejectsDuplicateRoots(t *testing.T) {
	dir := t.TempDir()

	c := config.Config{PhotoDirs: []string{dir, dir}, DataDir: "./famifo-data", Addr: ":8080", ScanWorkers: 1, ScanInterval: time.Hour}

	require.Error(t, c.Validate(), "同じルートを2回走査しても無駄なだけ")
}

// 入れ子のルートは同じファイルを2回走査し、サムネイルを2回作る。
func TestValidateRejectsNestedRoots(t *testing.T) {
	outer := t.TempDir()
	inner := filepath.Join(outer, "sub")
	require.NoError(t, os.MkdirAll(inner, 0o755))

	c := config.Config{PhotoDirs: []string{outer, inner}, DataDir: "./famifo-data", Addr: ":8080", ScanWorkers: 1, ScanInterval: time.Hour}

	require.Error(t, c.Validate())
}

// -data はどのルートの中にあってもいけない。中にあるとサムネイルを
// 走査対象として拾い、それのサムネイルを作る、という自己増殖が起きる。
func TestValidateRejectsDataInsideAnyRoot(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()

	c := config.Config{
		PhotoDirs: []string{a, b},
		DataDir:   filepath.Join(b, "famifo-data"), Addr: ":8080", ScanWorkers: 1, ScanInterval: time.Hour,
	}

	require.Error(t, c.Validate(), "2つ目のルートの中でも弾くこと")
}
