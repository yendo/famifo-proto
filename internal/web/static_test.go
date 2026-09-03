package web_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStaticAssetsAreServed(t *testing.T) {
	f := newWebFixture(t, 10)
	tests := map[string]string{
		"/static/app.css": "text/css",
		"/static/app.js":  "text/javascript",
	}
	for target, wantType := range tests {
		t.Run(target, func(t *testing.T) {
			rec := do(t, f.h, target)

			require.Equal(t, http.StatusOK, rec.Code)
			require.Contains(t, rec.Header().Get("Content-Type"), wantType)
			require.NotEmpty(t, rec.Body.String())
		})
	}
}

func TestAppJSImplementsVirtualScroll(t *testing.T) {
	f := newWebFixture(t, 10)

	body := do(t, f.h, "/static/app.js").Body.String()

	require.Contains(t, body, "#spacer")
	require.Contains(t, body, "#window")
	require.Contains(t, body, "gridTemplateColumns", "列数はブラウザの計算結果から読む")
	require.Contains(t, body, "--label-h", "ラベル高はCSSの1箇所から読む")
	require.Contains(t, body, "/items?offset=")
	require.Contains(t, body, "dataset.full", "塊からURLを控えてライトボックスに渡す")
	require.Contains(t, body, "daycard", "日ごとのカードを組み立てる")
	require.Contains(t, body, "famifo", "他のスクリプトから使える形で公開する")
	require.NotContains(t, body, "gridColumnStart",
		"塊単位で貼って列位置を補正する方式は廃した")
	require.NotContains(t, body, "pastedIndex",
		"貼り付け先頭ではなく範囲を公開する")
}

func TestAppCSSDefinesDayCards(t *testing.T) {
	f := newWebFixture(t, 10)

	body := do(t, f.h, "/static/app.css").Body.String()

	require.Contains(t, body, "--label-h", "ラベル高の定義はここ1箇所だけ")
	require.Contains(t, body, ".daycard")
	require.Contains(t, body, ".daylabel")
	require.Contains(t, body, "1 / -1", "ラベルはカードの全幅を占める")
}

func TestAppJSLightboxUsesGlobalIndex(t *testing.T) {
	f := newWebFixture(t, 10)

	body := do(t, f.h, "/static/app.js").Body.String()

	require.Contains(t, body, "#lightbox")
	require.Contains(t, body, "urlAt", "DOMではなく通し番号でURLを引く")
	require.Contains(t, body, "touchstart", "スワイプ操作を実装している")
	require.Contains(t, body, "dataset.i", "タイルに書いた通し番号をそのまま読む")
	require.NotContains(t, body, "tiles().indexOf",
		"DOM上のタイル一覧に依存する実装は残さない")
	require.NotContains(t, body, "parentElement.querySelectorAll",
		"DOM上の位置を数える実装は残さない。カードに入ると数えられなくなる")
}

func TestAppJSImplementsScrubber(t *testing.T) {
	f := newWebFixture(t, 10)

	body := do(t, f.h, "/static/app.js").Body.String()

	require.Contains(t, body, "#scrubber")
	require.Contains(t, body, "daygroups", "日ごとの表は埋め込みから読む")
	require.NotContains(t, body, "/dates", "エンドポイントは廃止した")
	require.Contains(t, body, "scrub-label", "ドラッグ中に年月を表示する")
	require.Contains(t, body, "dayAtY", "位置から日を引く。枚数の比例では求まらない")
	require.NotContains(t, body, "frac * famifo.total",
		"行の高さが日ごとに変わった時点でこの比例関係は成立しない")
}

func TestAppCSSIsResponsive(t *testing.T) {
	f := newWebFixture(t, 10)

	body := do(t, f.h, "/static/app.css").Body.String()

	require.Contains(t, body, "@media", "画面幅に応じて列数を変える")
	require.Contains(t, body, "grid-template-columns")
	require.Contains(t, body, "#window", "グリッドは #window と #colprobe に定義する")
	require.Contains(t, body, ".scrubber", "日付スクラバーの見た目を定義する")
	require.Contains(t, body, ".scrub-thumb")
	require.Contains(t, body, ".scrub-label")
}

// #window と #colprobe は同じトラック定義を共有していなければならない。
// 片方だけに breakpoint を足すと、測る列数と描く列数が食い違う。どちらの
// 要素も単体では辻褄が合っているため、何も落ちないまま全部ずれる。
func TestGridTracksAreSharedByWindowAndProbe(t *testing.T) {
	f := newWebFixture(t, 10)

	body := do(t, f.h, "/static/app.css").Body.String()

	found := 0
	for _, block := range strings.Split(body, "}") {
		if !strings.Contains(block, "grid-template-columns") {
			continue
		}
		found++
		open := strings.LastIndex(block, "{")
		require.GreaterOrEqual(t, open, 0, "セレクタが見つからない: %q", block)
		sel := block[:open]
		require.Contains(t, sel, "#window", "セレクタ: %q", sel)
		require.Contains(t, sel, "#colprobe", "セレクタ: %q", sel)
	}
	require.GreaterOrEqual(t, found, 3, "基本 + breakpoint 2つで最低3つあるはず")
}

func TestAppJSRestoresPositionByPhotoIndex(t *testing.T) {
	f := newWebFixture(t, 10)

	body := do(t, f.h, "/static/app.js").Body.String()

	require.Contains(t, body, "yForIndex(L, topIndex)",
		"復元先は通し番号から引く。行の高さが不均一なので掛け算では出ない")
	require.NotContains(t, body, "Math.floor(topIndex / cols) * rowH",
		"均一な行を前提にした復元は残さない")
	require.NotContains(t, body, "pasted.from :",
		"アンカーに貼り付け範囲の先頭を使わない。OVERSCAN のぶん手前に着地する")
}
