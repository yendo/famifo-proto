package web

import (
	"encoding/json"
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
}

func TestGalleryEmbedsTotalAndFirstChunk(t *testing.T) {
	f := newWebFixture(t, 60)
	for i := range 3 {
		f.addPhoto(t, fmt.Sprintf("p%d.jpg", i), time.Unix(int64(1600000000+i), 0), true)
	}

	body := do(t, f.h, "/").Body.String()

	require.Contains(t, body, `data-total="3"`)
	require.Contains(t, body, `id="spacer"`)
	require.Contains(t, body, `id="window"`)
	require.Equal(t, 3, strings.Count(body, `class="tile"`), "先頭の塊を埋めて返すこと")
}

func TestGalleryDropsHtmx(t *testing.T) {
	f := newWebFixture(t, 60)
	f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), true)

	body := do(t, f.h, "/").Body.String()

	require.NotContains(t, body, "htmx.min.js")
	require.NotContains(t, body, "hx-")
}

func TestGalleryEmptyLibrary(t *testing.T) {
	f := newWebFixture(t, 60)

	body := do(t, f.h, "/").Body.String()

	require.Contains(t, body, `data-total="0"`)
	require.NotContains(t, body, `class="tile"`)
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

func TestDatesReturnsMonthOffsets(t *testing.T) {
	f := newWebFixture(t, 60)
	f.addPhoto(t, "a.jpg", time.Date(2022, 12, 5, 10, 0, 0, 0, time.Local), true)
	f.addPhoto(t, "b.jpg", time.Date(2022, 11, 8, 10, 0, 0, 0, time.Local), true)

	rec := do(t, f.h, "/dates")

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "application/json")

	var got []struct {
		M string `json:"m"`
		O int    `json:"o"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, "2022-12", got[0].M)
	require.Equal(t, 0, got[0].O)
	require.Equal(t, "2022-11", got[1].M)
	require.Equal(t, 1, got[1].O)
}

func TestDatesOnEmptyLibraryReturnsEmptyArray(t *testing.T) {
	f := newWebFixture(t, 60)

	rec := do(t, f.h, "/dates")

	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `[]`, rec.Body.String(), "null ではなく空配列を返すこと")
}

func TestItemsTagsEachTileWithLocalDate(t *testing.T) {
	f := newWebFixture(t, 60)
	// TZ=UTC の環境でも回帰を検出できるよう、テスト中だけ固定オフセットにする。
	orig := time.Local
	time.Local = time.FixedZone("JST", 9*60*60)
	t.Cleanup(func() { time.Local = orig })

	// ローカルで2月8日の未明。UTCに直すと2月7日になる時刻。
	f.addPhoto(t, "a.jpg", time.Date(2026, 2, 8, 0, 30, 0, 0, time.Local), true)

	body := do(t, f.h, "/items?offset=0&limit=60").Body.String()

	require.Contains(t, body, `data-date="2026-02-08"`,
		"UTCで切ると2026-02-07になる。ローカル時刻で分類すること")
}

func TestGalleryTagsFirstChunkWithDates(t *testing.T) {
	f := newWebFixture(t, 60)
	f.addPhoto(t, "a.jpg", time.Date(2026, 2, 8, 12, 0, 0, 0, time.Local), true)

	body := do(t, f.h, "/").Body.String()

	require.Contains(t, body, `data-date="2026-02-08"`,
		"初回HTMLの先頭の塊にも日付が要る")
}
