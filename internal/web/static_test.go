package web

import (
	"net/http"
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
	require.Contains(t, body, "/items?offset=")
	require.Contains(t, body, "data-full", "塊からURLを控えてライトボックスに渡す")
	require.Contains(t, body, "famifo", "他のスクリプトから使える形で公開する")
}

func TestAppJSLightboxUsesGlobalIndex(t *testing.T) {
	f := newWebFixture(t, 10)

	body := do(t, f.h, "/static/app.js").Body.String()

	require.Contains(t, body, "#lightbox")
	require.Contains(t, body, "urlAt", "DOMではなく通し番号でURLを引く")
	require.Contains(t, body, "touchstart", "スワイプ操作を実装している")
	require.NotContains(t, body, "tiles().indexOf",
		"DOM上のタイル一覧に依存する実装は残さない")
}

func TestAppJSImplementsScrubber(t *testing.T) {
	f := newWebFixture(t, 10)

	body := do(t, f.h, "/static/app.js").Body.String()

	require.Contains(t, body, "#scrubber")
	require.Contains(t, body, "daygroups", "日ごとの表は埋め込みから読む")
	require.NotContains(t, body, "/dates", "エンドポイントは廃止した")
	require.Contains(t, body, "scrub-label", "ドラッグ中に年月を表示する")
}

func TestAppCSSIsResponsive(t *testing.T) {
	f := newWebFixture(t, 10)

	body := do(t, f.h, "/static/app.css").Body.String()

	require.Contains(t, body, "@media", "画面幅に応じて列数を変える")
	require.Contains(t, body, "grid-template-columns")
	require.Contains(t, body, "#window", "グリッドは #window 側に定義する")
	require.Contains(t, body, ".scrubber", "日付スクラバーの見た目を定義する")
	require.Contains(t, body, ".scrub-thumb")
	require.Contains(t, body, ".scrub-label")
}
