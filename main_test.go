package main

import (
	"go/parser"
	"go/token"
	"io"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 日付の切り出しは time.Local に依存する（撮影日時の解釈も、日ごとの区切りも）。
// Goは実行時に /etc/localtime か /usr/share/zoneinfo からタイムゾーンを解決するが、
// 配布先の scratch コンテナにはどちらも無い。TZ=Asia/Tokyo を設定しても、名前を
// 引くデータが無いため UTC にフォールバックする（実測で確認済み）。
// tzdata を埋め込むと、バイナリ単体で名前を解決できる。
//
// この性質は zoneinfo の無い環境でしか現れないため、手元のマシンで走る単体
// テストでは実挙動を確認できない。ここでは import が消えていないことだけを
// 保証する。実挙動の確認は scratch コンテナで行う（README参照）。
func TestEmbedsTimezoneDatabase(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, parser.ImportsOnly)
	require.NoError(t, err)

	var paths []string
	for _, imp := range f.Imports {
		paths = append(paths, imp.Path.Value)
	}

	require.Contains(t, paths, `"time/tzdata"`,
		"zoneinfoの無いコンテナで time.Local が UTC に落ちるのを防ぐため、"+
			"タイムゾーンデータベースをバイナリに埋め込むこと")
}

// TZを渡し忘れたコンテナは黙ってUTCで動き、そのまま本番のインデックスを
// 作ると全件に誤った値が焼き付く。起動ログで気づけるようにする。
//
// Location の名前だけでは足りない。/etc/localtime を読んだだけの環境では
// 名前が "Local" になり、JSTなのかUTCなのか読み取れない（実測で確認）。
func TestStartupTimezoneDistinguishesZonesWithTheSameName(t *testing.T) {
	jst := time.Date(2026, 8, 26, 12, 0, 0, 0, time.FixedZone("Local", 9*60*60))
	utc := time.Date(2026, 8, 26, 12, 0, 0, 0, time.FixedZone("Local", 0))

	require.NotEqual(t, startupTimezone(utc), startupTimezone(jst),
		"Location の名前が同じでも、時差で区別できること")
	require.Contains(t, startupTimezone(jst), "+09:00")
}

func TestParseArgsUsesDefaults(t *testing.T) {
	dir := t.TempDir()

	got, _, err := parseArgs([]string{"-dir", dir}, io.Discard)

	require.NoError(t, err)
	require.Equal(t, []string{dir}, got.PhotoDirs)
	require.Equal(t, "./famifo-data", got.DataDir)
	require.Equal(t, ":8080", got.Addr)
	require.Equal(t, max(runtime.NumCPU()/2, 1), got.ScanWorkers,
		"既定はCPU数の半分。設定を書かなくても並行に取り込みつつ、CPUは使い切らない")
}

func TestParseArgsOverridesEveryFlag(t *testing.T) {
	dir := t.TempDir()

	got, _, err := parseArgs([]string{
		"-dir", dir, "-data", "/var/famifo", "-addr", "192.168.1.10:9000",
		"-scan-workers", "3",
	}, io.Discard)

	require.NoError(t, err)
	require.Equal(t, "/var/famifo", got.DataDir)
	require.Equal(t, "192.168.1.10:9000", got.Addr)
	require.Equal(t, 3, got.ScanWorkers)
}

func TestParseArgsSplitsDirOnTheListSeparator(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()

	got, _, err := parseArgs([]string{"-dir", a + string(filepath.ListSeparator) + b}, io.Discard)

	require.NoError(t, err)
	require.Equal(t, []string{a, b}, got.PhotoDirs)
}

// -version はバージョンを表示して終わるだけなので、-dir を要求しない。
// 設定の検証まで進むと「-dir は必須です」で落ちてしまう。
func TestParseArgsVersionShortCircuitsValidation(t *testing.T) {
	_, showVersion, err := parseArgs([]string{"-version"}, io.Discard)

	require.NoError(t, err)
	require.True(t, showVersion)
}

// ':' を含むパスを渡すと分割で壊れる。なぜそうなったか読めるエラーにする。
func TestParseArgsExplainsHowDirWasSplit(t *testing.T) {
	_, _, err := parseArgs([]string{"-dir", "/no/such/2024:05:24"}, io.Discard)

	require.Error(t, err)
	require.Contains(t, err.Error(), "2024",
		"分割結果を示して、区切り文字で切れたことが分かるようにする")
}
