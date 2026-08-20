package web

import (
	"fmt"
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

func TestItemsReturnsRequestedWindow(t *testing.T) {
	f := newWebFixture(t, 60)
	var ids []string
	for i := range 5 {
		p := f.addPhoto(t, fmt.Sprintf("p%d.jpg", i), time.Unix(int64(1600000000+i), 0), true)
		ids = append(ids, p.ID)
	}

	body := do(t, f.h, "/items?offset=1&limit=2").Body.String()

	// 新しい順は p4,p3,p2,p1,p0 なので offset=1 の2件は p3,p2
	require.Contains(t, body, ids[3])
	require.Contains(t, body, ids[2])
	require.NotContains(t, body, ids[4])
	require.NotContains(t, body, ids[1])
}

func TestItemsHasNoSentinel(t *testing.T) {
	f := newWebFixture(t, 60)
	f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), true)

	body := do(t, f.h, "/items?offset=0&limit=1").Body.String()

	require.NotContains(t, body, "hx-", "htmxの属性は残さない")
	require.NotContains(t, body, "sentinel")
}

func TestItemsRejectsBadOffset(t *testing.T) {
	f := newWebFixture(t, 60)
	for _, target := range []string{
		"/items?offset=abc&limit=10",
		"/items?offset=-1&limit=10",
		"/items?offset=0&limit=abc",
		"/items?offset=0&limit=-1",
	} {
		t.Run(target, func(t *testing.T) {
			require.Equal(t, http.StatusBadRequest, do(t, f.h, target).Code)
		})
	}
}

func TestItemsDefaultsToFirstWindow(t *testing.T) {
	f := newWebFixture(t, 60)
	p := f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), true)

	body := do(t, f.h, "/items").Body.String()

	require.Contains(t, body, p.ID)
}
