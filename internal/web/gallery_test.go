package web_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yendo/famifo-proto/internal/photo"
)

func TestGalleryRendersTiles(t *testing.T) {
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), photo.ThumbFamifo)

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
		f.addPhoto(t, fmt.Sprintf("p%d.jpg", i), time.Unix(int64(1600000000+i), 0), photo.ThumbFamifo)
	}

	body := do(t, f.h, "/").Body.String()

	require.Contains(t, body, `data-total="3"`)
	require.Contains(t, body, `id="spacer"`)
	require.Contains(t, body, `id="window"`)
	require.Equal(t, 3, strings.Count(body, `class="tile"`), "先頭の塊を埋めて返すこと")
}

func TestGalleryDropsHtmx(t *testing.T) {
	f := newWebFixture(t, 60)
	f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), photo.ThumbFamifo)

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
	p := f.addPhoto(t, "a.heic", time.Unix(1600000000, 0), photo.ThumbNone)

	body := do(t, f.h, "/").Body.String()

	require.Contains(t, body, `src="/photo/`+p.ID+`"`,
		"サムネイルが無いフォーマットは原本を直接使う")
	require.NotContains(t, body, "/thumb/"+p.ID)
}

func TestGalleryOrdersNewestFirst(t *testing.T) {
	f := newWebFixture(t, 10)
	old := f.addPhoto(t, "old.jpg", time.Unix(1600000000, 0), photo.ThumbFamifo)
	recent := f.addPhoto(t, "new.jpg", time.Unix(1700000000, 0), photo.ThumbFamifo)

	body := do(t, f.h, "/").Body.String()

	require.Less(t, strings.Index(body, recent.ID), strings.Index(body, old.ID),
		"撮影日時の新しい順に並べる")
}

func TestItemsReturnsFragmentOnly(t *testing.T) {
	f := newWebFixture(t, 1)
	f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), photo.ThumbFamifo)
	last := f.addPhoto(t, "b.jpg", time.Unix(1700000000, 0), photo.ThumbFamifo)

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
		p := f.addPhoto(t, fmt.Sprintf("p%d.jpg", i), time.Unix(int64(1600000000+i), 0), photo.ThumbFamifo)
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
	f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), photo.ThumbFamifo)

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
	p := f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), photo.ThumbFamifo)

	body := do(t, f.h, "/items").Body.String()

	require.Contains(t, body, p.ID)
}

// embeddedDayGroups は初回HTMLに埋め込まれた日ごとの表を取り出す。
func embeddedDayGroups(t *testing.T, body string) []struct {
	D string `json:"d"`
	N int    `json:"n"`
} {
	t.Helper()
	const open = `<script type="application/json" id="daygroups">`
	i := strings.Index(body, open)
	require.GreaterOrEqual(t, i, 0, "日ごとの表が埋め込まれていない")
	rest := body[i+len(open):]
	j := strings.Index(rest, "</script>")
	require.GreaterOrEqual(t, j, 0, "script タグが閉じていない")

	var out []struct {
		D string `json:"d"`
		N int    `json:"n"`
	}
	require.NoError(t, json.Unmarshal([]byte(rest[:j]), &out))
	return out
}

func TestGalleryEmbedsDayGroups(t *testing.T) {
	f := newWebFixture(t, 60)
	// 新しい順に: 2026-02-08 が2枚、2026-02-03 が1枚
	f.addPhoto(t, "a.jpg", time.Date(2026, 2, 8, 18, 0, 0, 0, time.Local), photo.ThumbFamifo)
	f.addPhoto(t, "b.jpg", time.Date(2026, 2, 8, 10, 0, 0, 0, time.Local), photo.ThumbFamifo)
	f.addPhoto(t, "c.jpg", time.Date(2026, 2, 3, 10, 0, 0, 0, time.Local), photo.ThumbFamifo)

	got := embeddedDayGroups(t, do(t, f.h, "/").Body.String())

	require.Len(t, got, 2)
	require.Equal(t, "2026-02-08", got[0].D)
	require.Equal(t, 2, got[0].N)
	require.Equal(t, "2026-02-03", got[1].D)
	require.Equal(t, 1, got[1].N)
}

func TestGalleryEmbedsEmptyDayGroupsForEmptyLibrary(t *testing.T) {
	f := newWebFixture(t, 60)

	got := embeddedDayGroups(t, do(t, f.h, "/").Body.String())

	require.Empty(t, got, "空でも配列として埋め込むこと（JSON.parse が落ちないように）")
}

func TestDatesEndpointIsGone(t *testing.T) {
	f := newWebFixture(t, 60)
	f.addPhoto(t, "a.jpg", time.Date(2026, 2, 8, 10, 0, 0, 0, time.Local), photo.ThumbFamifo)

	rec := do(t, f.h, "/dates")

	require.Equal(t, http.StatusNotFound, rec.Code,
		"日ごとの表は初回HTMLに埋め込むので、この口は持たない")
}

func TestItemsTagsEachTileWithLocalDate(t *testing.T) {
	f := newWebFixture(t, 60)
	// TZ=UTC の環境でも回帰を検出できるよう、テスト中だけ固定オフセットにする。
	orig := time.Local
	time.Local = time.FixedZone("JST", 9*60*60)
	t.Cleanup(func() { time.Local = orig })

	// ローカルで2月8日の未明。UTCに直すと2月7日になる時刻。
	f.addPhoto(t, "a.jpg", time.Date(2026, 2, 8, 0, 30, 0, 0, time.Local), photo.ThumbFamifo)

	body := do(t, f.h, "/items?offset=0&limit=60").Body.String()

	require.Contains(t, body, `data-date="2026-02-08"`,
		"UTCで切ると2026-02-07になる。ローカル時刻で分類すること")
}

func TestGalleryTagsFirstChunkWithDates(t *testing.T) {
	f := newWebFixture(t, 60)
	f.addPhoto(t, "a.jpg", time.Date(2026, 2, 8, 12, 0, 0, 0, time.Local), photo.ThumbFamifo)

	body := do(t, f.h, "/").Body.String()

	require.Contains(t, body, `data-date="2026-02-08"`,
		"初回HTMLの先頭の塊にも日付が要る")
}

func TestGalleryUsesTheBorrowedThumbForHEIC(t *testing.T) {
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.heic", time.Unix(1600000000, 0), photo.ThumbSyno)

	body := do(t, f.h, "/").Body.String()

	require.Contains(t, body, `src="/thumb/`+p.ID+`"`,
		"@eaDir から借りられるHEICはサムネイルを使う")
}
