package web

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGalleryRendersTiles(t *testing.T) {
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), true)

	rec := do(t, f.h, "/")

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	body := rec.Body.String()
	require.Contains(t, body, `src="/thumb/`+p.ID+`"`)
	require.Contains(t, body, `data-full="/photo/`+p.ID+`"`)
	require.Contains(t, body, "/static/htmx.min.js")
}

func TestGalleryUsesOriginalAsThumbForHEIC(t *testing.T) {
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.heic", time.Unix(1600000000, 0), false)

	body := do(t, f.h, "/").Body.String()

	require.Contains(t, body, `src="/photo/`+p.ID+`"`,
		"サムネイルが無いフォーマットは原本を直接使う")
	require.NotContains(t, body, "/thumb/"+p.ID)
}

func TestGalleryOrdersNewestFirst(t *testing.T) {
	f := newWebFixture(t, 10)
	old := f.addPhoto(t, "old.jpg", time.Unix(1600000000, 0), true)
	recent := f.addPhoto(t, "new.jpg", time.Unix(1700000000, 0), true)

	body := do(t, f.h, "/").Body.String()

	require.Less(t, strings.Index(body, recent.ID), strings.Index(body, old.ID),
		"撮影日時の新しい順に並べる")
}

func TestGalleryEmitsSentinelWhenMorePagesExist(t *testing.T) {
	f := newWebFixture(t, 1)
	f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), true)
	last := f.addPhoto(t, "b.jpg", time.Unix(1700000000, 0), true)

	body := do(t, f.h, "/").Body.String()

	require.Contains(t, body, `hx-trigger="revealed"`)
	// html/template は属性内のリテラルな & をエスケープしない（検証済み）
	require.Contains(t, body, "/items?t=1700000000&id="+last.ID,
		"次ページのカーソルは最後に描画した写真を指す")
}

func TestGalleryOmitsSentinelOnLastPage(t *testing.T) {
	f := newWebFixture(t, 10)
	f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), true)

	body := do(t, f.h, "/").Body.String()

	require.NotContains(t, body, "hx-trigger")
}

func TestItemsReturnsFragmentOnly(t *testing.T) {
	f := newWebFixture(t, 1)
	f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), true)
	last := f.addPhoto(t, "b.jpg", time.Unix(1700000000, 0), true)

	rec := do(t, f.h, "/items?t=1700000000&id="+last.ID)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.NotContains(t, body, "<html", "断片なので完全なページを返さない")
	require.NotContains(t, body, "<body")
	require.Contains(t, body, "/photo/")
}

func TestItemsRejectsBadCursor(t *testing.T) {
	f := newWebFixture(t, 10)

	rec := do(t, f.h, "/items?t=notanumber&id=abc")

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestItemsWithoutCursorReturnsFirstPage(t *testing.T) {
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), true)

	body := do(t, f.h, "/items").Body.String()

	require.Contains(t, body, p.ID)
}
