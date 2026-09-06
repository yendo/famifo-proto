package main

import (
	"go/parser"
	"go/token"
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
