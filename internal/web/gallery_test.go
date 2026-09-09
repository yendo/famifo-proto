package web_test

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
	t.Parallel()
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), famifoThumb)

	rec := doGet(t, f.h, "/")

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	body := rec.Body.String()
	require.Contains(t, body, `src="/thumb/`+p.ID()+`"`)
	require.Contains(t, body, `data-full="/file/`+p.ID()+`"`)
}

func TestGalleryEmbedsTotalAndFirstChunk(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 60)
	for i := range 3 {
		f.addPhoto(t, fmt.Sprintf("p%d.jpg", i), time.Unix(int64(1600000000+i), 0), famifoThumb)
	}

	body := doGet(t, f.h, "/").Body.String()

	require.Contains(t, body, `data-total="3"`)
	require.Contains(t, body, `id="spacer"`)
	require.Contains(t, body, `id="window"`)
	require.Equal(t, 3, strings.Count(body, `class="tile"`), "the first chunk comes back filled")
}

func TestGalleryDropsHtmx(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 60)
	f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), famifoThumb)

	body := doGet(t, f.h, "/").Body.String()

	require.NotContains(t, body, "htmx.min.js")
	require.NotContains(t, body, "hx-")
}

func TestGalleryEmptyLibrary(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 60)

	body := doGet(t, f.h, "/").Body.String()

	require.Contains(t, body, `data-total="0"`)
	require.NotContains(t, body, `class="tile"`)
}

// タイルのURLは出どころによらず /thumb/ である。どのファイルを出すかは配信時に
// 決まるので、一覧を組み立てた時点の状態を焼き付けない。
func TestGalleryPointsEveryTileAtThumb(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.heic", time.Unix(1600000000, 0), noThumb)

	body := doGet(t, f.h, "/").Body.String()

	require.Contains(t, body, `src="/thumb/`+p.ID()+`"`,
		"a tile points at /thumb/ even with no thumbnail; the handler falls back to the original")
	require.NotContains(t, body, `src="/file/`+p.ID()+`"`)
}

func TestGalleryOrdersNewestFirst(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	old := f.addPhoto(t, "old.jpg", time.Unix(1600000000, 0), famifoThumb)
	recent := f.addPhoto(t, "new.jpg", time.Unix(1700000000, 0), famifoThumb)

	body := doGet(t, f.h, "/").Body.String()

	require.Less(t, strings.Index(body, recent.ID()), strings.Index(body, old.ID()),
		"ordered by capture time, newest first")
}

func TestTilesReturnsFragmentOnly(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 1)
	f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), famifoThumb)
	last := f.addPhoto(t, "b.jpg", time.Unix(1700000000, 0), famifoThumb)

	rec := doGet(t, f.h, "/tiles?t=1700000000&id="+last.ID())

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.NotContains(t, body, "<html", "a fragment, not a whole page")
	require.NotContains(t, body, "<body")
	require.Contains(t, body, "/file/")
}

func TestTilesReturnsRequestedWindow(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 60)
	var ids []string
	for i := range 5 {
		p := f.addPhoto(t, fmt.Sprintf("p%d.jpg", i), time.Unix(int64(1600000000+i), 0), famifoThumb)
		ids = append(ids, p.ID())
	}

	body := doGet(t, f.h, "/tiles?offset=1&limit=2").Body.String()

	// 新しい順は p4,p3,p2,p1,p0 なので offset=1 の2件は p3,p2
	require.Contains(t, body, ids[3])
	require.Contains(t, body, ids[2])
	require.NotContains(t, body, ids[4])
	require.NotContains(t, body, ids[1])
}

func TestTilesHasNoSentinel(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 60)
	f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), famifoThumb)

	body := doGet(t, f.h, "/tiles?offset=0&limit=1").Body.String()

	require.NotContains(t, body, "hx-", "no htmx attributes are left behind")
	require.NotContains(t, body, "sentinel")
}

func TestTilesRejectsBadOffset(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 60)
	for _, target := range []string{
		"/tiles?offset=abc&limit=10",
		"/tiles?offset=-1&limit=10",
		"/tiles?offset=0&limit=abc",
		"/tiles?offset=0&limit=-1",
	} {
		t.Run(target, func(t *testing.T) {
			require.Equal(t, http.StatusBadRequest, doGet(t, f.h, target).Code)
		})
	}
}

func TestTilesDefaultsToFirstWindow(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 60)
	p := f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), famifoThumb)

	body := doGet(t, f.h, "/tiles").Body.String()

	require.Contains(t, body, p.ID())
}

// embeddedDayGroups は初回HTMLに埋め込まれた日ごとの表を取り出す。
func embeddedDayGroups(t *testing.T, body string) []struct {
	Date  string `json:"d"`
	Count int    `json:"n"`
} {
	t.Helper()
	const open = `<script type="application/json" id="daygroups">`
	i := strings.Index(body, open)
	require.GreaterOrEqual(t, i, 0, "the per-day table is not embedded")
	rest := body[i+len(open):]
	j := strings.Index(rest, "</script>")
	require.GreaterOrEqual(t, j, 0, "the script tag is not closed")

	var out []struct {
		Date  string `json:"d"`
		Count int    `json:"n"`
	}
	require.NoError(t, json.Unmarshal([]byte(rest[:j]), &out))
	return out
}

func TestGalleryEmbedsDayGroups(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 60)
	// 新しい順に: 2026-02-08 が2枚、2026-02-03 が1枚
	f.addPhoto(t, "a.jpg", time.Date(2026, 2, 8, 18, 0, 0, 0, time.Local), famifoThumb)
	f.addPhoto(t, "b.jpg", time.Date(2026, 2, 8, 10, 0, 0, 0, time.Local), famifoThumb)
	f.addPhoto(t, "c.jpg", time.Date(2026, 2, 3, 10, 0, 0, 0, time.Local), famifoThumb)

	got := embeddedDayGroups(t, doGet(t, f.h, "/").Body.String())

	require.Len(t, got, 2)
	require.Equal(t, "2026-02-08", got[0].Date)
	require.Equal(t, 2, got[0].Count)
	require.Equal(t, "2026-02-03", got[1].Date)
	require.Equal(t, 1, got[1].Count)
}

func TestGalleryEmbedsEmptyDayGroupsForEmptyLibrary(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 60)

	got := embeddedDayGroups(t, doGet(t, f.h, "/").Body.String())

	require.Empty(t, got, "embedded as an array even when empty, so JSON.parse does not fail")
}

func TestDatesEndpointIsGone(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 60)
	f.addPhoto(t, "a.jpg", time.Date(2026, 2, 8, 10, 0, 0, 0, time.Local), famifoThumb)

	rec := doGet(t, f.h, "/dates")

	require.Equal(t, http.StatusNotFound, rec.Code,
		"the per-day table ships in the first HTML, so there is no endpoint for it")
}

func TestTilesTagsEachTileWithLocalDate(t *testing.T) {
	// time.Local はプロセス全体で1つしかない。書き換えるテストが並列に走ると、
	// 同時に走っている他のテストの時刻解釈まで巻き添えで変わる。実際 -race が
	// 競合として検出する。このテストは t.Parallel() を呼ばない。
	f := newWebFixture(t, 60)
	// TZ=UTC の環境でも回帰を検出できるよう、テスト中だけ固定オフセットにする。
	orig := time.Local
	time.Local = time.FixedZone("JST", 9*60*60)
	t.Cleanup(func() { time.Local = orig })

	// ローカルで2月8日の未明。UTCに直すと2月7日になる時刻。
	f.addPhoto(t, "a.jpg", time.Date(2026, 2, 8, 0, 30, 0, 0, time.Local), famifoThumb)

	body := doGet(t, f.h, "/tiles?offset=0&limit=60").Body.String()

	require.Contains(t, body, `data-date="2026-02-08"`,
		"cutting in UTC would give 2026-02-07; group by local time")
}

func TestGalleryTagsFirstChunkWithDates(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 60)
	f.addPhoto(t, "a.jpg", time.Date(2026, 2, 8, 12, 0, 0, 0, time.Local), famifoThumb)

	body := doGet(t, f.h, "/").Body.String()

	require.Contains(t, body, `data-date="2026-02-08"`,
		"the first chunk of the initial HTML needs its date too")
}

func TestGalleryUsesTheBorrowedThumbForHEIC(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.heic", time.Unix(1600000000, 0), eadirThumb)

	body := doGet(t, f.h, "/").Body.String()

	require.Contains(t, body, `src="/thumb/`+p.ID()+`"`,
		"a HEIC that can borrow from @eaDir uses the thumbnail")
}

// 写真ごとのURLは、その写真を開いた状態のギャラリーを返す。クライアントは
// 埋め込まれた通し番号でその位置へ飛ぶので、番号が一覧の並びと一致していること。
func TestItemOpensTheGalleryAtThePhoto(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	var photos []string
	for i, name := range []string{"a.jpg", "b.jpg", "c.jpg"} {
		p := f.addPhoto(t, name, time.Unix(int64(1600000000+i), 0), famifoThumb)
		photos = append(photos, p.ID())
	}

	// 新しい順に並ぶので c, b, a。真ん中の b は1番目。
	rec := doGet(t, f.h, "/item/"+photos[1])

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	body := rec.Body.String()
	require.Contains(t, body, `data-open="1"`)
	require.Contains(t, body, `data-total="3"`)
}

// 消えた写真のURLを共有されても、壊れた画面ではなくギャラリーを出す。
func TestItemUnknownPhotoRedirectsToTheGallery(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), famifoThumb)

	rec := doGet(t, f.h, "/item/nosuchphoto")

	require.Equal(t, http.StatusFound, rec.Code)
	require.Equal(t, "/", rec.Header().Get("Location"))
}

func TestGalleryOpensNoPhoto(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), famifoThumb)

	body := doGet(t, f.h, "/").Body.String()

	require.Contains(t, body, `data-open="-1"`)
}

// タイルのリンク先は画像そのものではなく写真のページである。新しいタブで開く
// 操作や、リンクのコピーが意味のあるURLを返すようにするため。
func TestTilesLinkToThePhotoPage(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), famifoThumb)

	body := doGet(t, f.h, "/").Body.String()

	require.Contains(t, body, `href="/item/`+p.ID()+`"`)
	require.Contains(t, body, `data-full="/file/`+p.ID()+`"`)
}
