package main

import (
	"go/parser"
	"go/token"
	"runtime/debug"
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

func TestFormatVersionPrefersTheOverride(t *testing.T) {
	// リリースビルドでは -ldflags で版番号を埋める。VCS情報より優先する。
	got := formatVersion("v1.2.3", []debug.BuildSetting{
		{Key: "vcs.revision", Value: "0123456789abcdef"},
	})

	require.Equal(t, "v1.2.3", got)
}

func TestFormatVersionUsesTheEmbeddedRevision(t *testing.T) {
	// 素の go build ではフラグを付けなくてもVCS情報が埋まる。
	got := formatVersion("", []debug.BuildSetting{
		{Key: "vcs.revision", Value: "0123456789abcdef"},
		{Key: "vcs.time", Value: "2026-08-26T11:50:11Z"},
		{Key: "vcs.modified", Value: "false"},
	})

	require.Equal(t, "01234567 (2026-08-26T11:50:11Z)", got)
}

// 未コミットの変更が混ざったバイナリを見分けられないと、NASに置いたものが
// 手元のどのコミットとも一致しない、という事故に気づけない。
func TestFormatVersionMarksADirtyTree(t *testing.T) {
	got := formatVersion("", []debug.BuildSetting{
		{Key: "vcs.revision", Value: "0123456789abcdef"},
		{Key: "vcs.modified", Value: "true"},
	})

	require.Contains(t, got, "dirty")
}

func TestFormatVersionFallsBackWhenNothingIsEmbedded(t *testing.T) {
	// .git の無い場所でビルドするとVCS情報が付かない（Dockerのマルチステージ等）。
	got := formatVersion("", nil)

	require.Equal(t, "unknown", got)
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
