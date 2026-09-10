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
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "a.txt")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))

	tests := map[string]config.Config{
		"dir is missing": {
			DataDir: "./famifo-data", Addr: ":8080", ScanWorkers: 1, ScanInterval: time.Hour,
		},
		"dir does not exist": {
			PhotoDirs: []string{filepath.Join(dir, "nope")},
			DataDir:   "./famifo-data", Addr: ":8080", ScanWorkers: 1, ScanInterval: time.Hour,
		},
		"dir is not a directory": {
			PhotoDirs: []string{file},
			DataDir:   "./famifo-data", Addr: ":8080", ScanWorkers: 1, ScanInterval: time.Hour,
		},
		"addr is empty": {
			PhotoDirs: []string{dir},
			DataDir:   "./famifo-data", Addr: "", ScanWorkers: 1, ScanInterval: time.Hour,
		},
		"data is inside dir": {
			PhotoDirs: []string{dir},
			DataDir:   filepath.Join(dir, "famifo-data"), Addr: ":8080", ScanWorkers: 1, ScanInterval: time.Hour,
		},
		"data is dir itself": {
			PhotoDirs: []string{dir},
			DataDir:   dir, Addr: ":8080", ScanWorkers: 1, ScanInterval: time.Hour,
		},
		"scan-workers is 0": {
			PhotoDirs: []string{dir},
			DataDir:   "./famifo-data", Addr: ":8080", ScanWorkers: 0, ScanInterval: time.Hour,
		},
		"scan-workers is negative": {
			PhotoDirs: []string{dir},
			DataDir:   "./famifo-data", Addr: ":8080", ScanWorkers: -1, ScanInterval: time.Hour,
		},
		// 0 だと待たずに回り続ける。走査が止まらなくなるので弾く。
		"scan-interval is 0": {
			PhotoDirs: []string{dir},
			DataDir:   "./famifo-data", Addr: ":8080", ScanWorkers: 1, ScanInterval: 0,
		},
		"scan-interval is negative": {
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
	t.Parallel()
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
	t.Parallel()
	c := config.Config{DataDir: "/var/famifo"}

	require.Equal(t, "/var/famifo/famifo.db", c.DBPath())
	require.Equal(t, "/var/famifo/thumbs", c.ThumbDir())
}

func TestValidateRejectsDuplicateRoots(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	c := config.Config{PhotoDirs: []string{dir, dir}, DataDir: "./famifo-data", Addr: ":8080", ScanWorkers: 1, ScanInterval: time.Hour}

	require.Error(t, c.Validate(), "scanning the same root twice is pure waste")
}

// 入れ子のルートは同じファイルを2回走査し、サムネイルを2回作る。
func TestValidateRejectsNestedRoots(t *testing.T) {
	t.Parallel()
	outer := t.TempDir()
	inner := filepath.Join(outer, "sub")
	require.NoError(t, os.MkdirAll(inner, 0o755))

	c := config.Config{PhotoDirs: []string{outer, inner}, DataDir: "./famifo-data", Addr: ":8080", ScanWorkers: 1, ScanInterval: time.Hour}

	require.Error(t, c.Validate())
}

// -data はどのルートの中にあってもいけない。中にあるとサムネイルを
// 走査対象として拾い、それのサムネイルを作る、という自己増殖が起きる。
func TestValidateRejectsDataInsideAnyRoot(t *testing.T) {
	t.Parallel()
	a, b := t.TempDir(), t.TempDir()

	c := config.Config{
		PhotoDirs: []string{a, b},
		DataDir:   filepath.Join(b, "famifo-data"), Addr: ":8080", ScanWorkers: 1, ScanInterval: time.Hour,
	}

	require.Error(t, c.Validate(), "rejected inside the second root too")
}

func validConfig(t *testing.T) config.Config {
	t.Helper()
	dir := t.TempDir()
	photos := filepath.Join(dir, "photos")
	require.NoError(t, os.MkdirAll(photos, 0o755))
	return config.Config{
		PhotoDirs: []string{photos}, DataDir: filepath.Join(dir, "data"),
		Addr: ":8080", ScanWorkers: 1, ScanInterval: time.Hour,
	}
}

func TestValidateAcceptsNoAuth(t *testing.T) {
	c := validConfig(t)
	require.NoError(t, c.Validate())
}

func TestValidateRequiresTheWholeAuthSet(t *testing.T) {
	// 一部だけ渡された状態で黙って無認証にすると、認証したつもりの構成が
	// 素通しで動いてしまう。落とす。
	base := validConfig(t)
	base.OIDCIssuer = "https://idp.example.invalid/sso"
	base.OIDCClientID = "famifo"
	base.OIDCClientSecret = "s3cret"
	base.ExternalURL = "https://famifo.example.invalid:8443"
	require.NoError(t, base.Validate())

	for name, mutate := range map[string]func(*config.Config){
		"no client id":     func(c *config.Config) { c.OIDCClientID = "" },
		"no client secret": func(c *config.Config) { c.OIDCClientSecret = "" },
		"no external url":  func(c *config.Config) { c.ExternalURL = "" },
	} {
		t.Run(name, func(t *testing.T) {
			c := base
			mutate(&c)
			require.Error(t, c.Validate())
		})
	}
}

func TestValidateRejectsABadExternalURL(t *testing.T) {
	base := validConfig(t)
	base.OIDCIssuer = "https://idp.example.invalid/sso"
	base.OIDCClientID = "famifo"
	base.OIDCClientSecret = "s3cret"

	for _, u := range []string{"famifo.example.invalid", "ftp://famifo.example.invalid", "/relative"} {
		c := base
		c.ExternalURL = u
		require.Error(t, c.Validate(), "must reject %q", u)
	}
}

func TestValidateRejectsALogoutURLWithoutAnIssuer(t *testing.T) {
	c := validConfig(t)
	c.OIDCLogoutURL = "https://idp.example.invalid:5001/webman/logout.cgi"

	require.Error(t, c.Validate())
}

func TestValidateAcceptsALogoutURL(t *testing.T) {
	base := validConfig(t)
	base.OIDCIssuer = "https://idp.example.invalid/sso"
	base.OIDCClientID = "famifo"
	base.OIDCClientSecret = "s3cret"
	base.ExternalURL = "https://famifo.example.invalid:8443"
	base.OIDCLogoutURL = "https://idp.example.invalid:5001/webman/logout.cgi"

	require.NoError(t, base.Validate())
}

func TestValidateRejectsABadLogoutURL(t *testing.T) {
	base := validConfig(t)
	base.OIDCIssuer = "https://idp.example.invalid/sso"
	base.OIDCClientID = "famifo"
	base.OIDCClientSecret = "s3cret"
	base.ExternalURL = "https://famifo.example.invalid:8443"

	for _, u := range []string{"idp.example.invalid", "ftp://idp.example.invalid", "/relative"} {
		c := base
		c.OIDCLogoutURL = u
		require.Error(t, c.Validate(), "must reject %q", u)
	}
}

func TestSessionKeyPathSitsInTheDataDir(t *testing.T) {
	c := validConfig(t)
	require.Equal(t, filepath.Join(c.DataDir, "session.key"), c.SessionKeyPath())
}

func TestRedirectURIAppendsCallbackPath(t *testing.T) {
	c := validConfig(t)
	c.ExternalURL = "https://famifo.example.invalid:8443"
	require.Equal(t, "https://famifo.example.invalid:8443/auth/callback", c.RedirectURI())
}

func TestRedirectURIDoesNotDoubleTheSlash(t *testing.T) {
	c := validConfig(t)
	c.ExternalURL = "https://famifo.example.invalid:8443/"
	require.Equal(t, "https://famifo.example.invalid:8443/auth/callback", c.RedirectURI())
}

func TestCookieSecureIsTrueForHTTPS(t *testing.T) {
	c := validConfig(t)
	c.ExternalURL = "https://famifo.example.invalid:8443"
	require.True(t, c.CookieSecure())
}

func TestCookieSecureIsFalseForHTTP(t *testing.T) {
	c := validConfig(t)
	c.ExternalURL = "http://famifo.example.invalid:8080"
	require.False(t, c.CookieSecure())
}

func TestCookieSecureIsTrueForUppercaseScheme(t *testing.T) {
	c := validConfig(t)
	c.ExternalURL = "HTTPS://famifo.example.invalid:8443"
	require.True(t, c.CookieSecure())
}
