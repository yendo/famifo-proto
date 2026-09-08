// 配信そのものと、ソースを読まないと分からない構造だけをここで見る。
//
// かつては app.js / app.css に特定の文字列が含まれるかを照合するテストが
// 6本あった（仮想スクロール・ライトボックス・スクラバー・スクロール復元と、
// CSSの日カード・レスポンシブ）。browser_test.go が実際にJavaScriptを走らせて
// 同じ挙動を確かめるようになったため削除した。文字列の照合は挙動を保証せず、
// 識別子を変えただけで落ちる一方、壊れたJSでも通ってしまう。
// 捨てた実装方式とその理由は、それぞれを検出するブラウザテストのコメントに
// 移してある。
package web_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStaticAssetsAreServed(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	tests := map[string]string{
		"/static/app.css": "text/css",
		"/static/app.js":  "text/javascript",
	}
	for target, wantType := range tests {
		t.Run(target, func(t *testing.T) {
			rec := doGet(t, f.h, target)

			require.Equal(t, http.StatusOK, rec.Code)
			require.Contains(t, rec.Header().Get("Content-Type"), wantType)
			require.NotEmpty(t, rec.Body.String())
		})
	}
}

// #window と #colprobe は同じトラック定義を共有していなければならない。
// 片方だけに breakpoint を足すと、測る列数と描く列数が食い違う。どちらの
// 要素も単体では辻褄が合っているため、何も落ちないまま全部ずれる。
//
// ブラウザテストは 375px・800px・1600px で走っており breakpoint を3つとも
// 跨ぐが、境界そのもの（700px・1100px）は踏んでいない。CSSの構造として
// 押さえるのはここだけである。
func TestGridTracksAreSharedByWindowAndProbe(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)

	body := doGet(t, f.h, "/static/app.css").Body.String()

	found := 0
	for _, block := range strings.Split(body, "}") {
		if !strings.Contains(block, "grid-template-columns") {
			continue
		}
		found++
		open := strings.LastIndex(block, "{")
		require.GreaterOrEqual(t, open, 0, "no selector found: %q", block)
		sel := block[:open]
		require.Contains(t, sel, "#window", "selector: %q", sel)
		require.Contains(t, sel, "#colprobe", "selector: %q", sel)
	}
	require.GreaterOrEqual(t, found, 3, "the base plus two breakpoints should make at least three")
}
