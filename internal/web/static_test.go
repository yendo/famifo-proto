package web

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStaticAssetsAreServed(t *testing.T) {
	f := newWebFixture(t, 10)
	tests := map[string]string{
		"/static/app.css":     "text/css",
		"/static/app.js":      "text/javascript",
		"/static/htmx.min.js": "text/javascript",
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

func TestAppJSWiresLightbox(t *testing.T) {
	f := newWebFixture(t, 10)

	body := do(t, f.h, "/static/app.js").Body.String()

	// gallery.html が出力するフックと食い違っていないこと
	require.Contains(t, body, "#lightbox")
	require.Contains(t, body, "data-full")
	require.Contains(t, body, "touchstart", "スワイプ操作を実装している")
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
