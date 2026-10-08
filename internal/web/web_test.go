package web_test

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/web"

	"github.com/yendo/famifo-proto/internal/media"
	"github.com/yendo/famifo-proto/internal/store"
	"github.com/yendo/famifo-proto/internal/synology"
	"github.com/yendo/famifo-proto/internal/thumb"
)

type webFixture struct {
	h        http.Handler
	st       *store.Store
	thumbs   *thumb.Provider
	mediaDir string
}

func newWebFixture(t *testing.T, chunkSize int) *webFixture {
	t.Helper()
	base := t.TempDir()
	st, err := store.Open(filepath.Join(base, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })

	thumbs, err := thumb.NewProvider(filepath.Join(base, "thumbs"))
	require.NoError(t, err)
	mediaDir := filepath.Join(base, "items")
	require.NoError(t, os.MkdirAll(mediaDir, 0o755))

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h, err := web.NewHandlerWithChunkSize(st, thumbs, nil, log, chunkSize)
	require.NoError(t, err)
	return &webFixture{h: h, st: st, thumbs: thumbs, mediaDir: mediaDir}
}

// thumbKind は addMedia がどのサムネイルをディスクに置くかを指定する。
// 出どころはDBの列ではなくファイルの有無で決まるので、テストが用意するのもファイルである。
type thumbKind int

const (
	noThumb     thumbKind = iota // 借りるものも作ったものも無い
	famifoThumb                  // 自前で生成したものがある
	eadirThumb                   // Synologyのものが @eaDir にある
)

// addMedia は原本ファイルとDB行を用意する。kind に応じてサムネイルも置く。
//
// 原本の中身は "original-<name>" で、応答がどのファイルから来たかを本文で見分けられる。
// ただし famifoThumb では、自前のサムネイルを Prepare に作らせるため原本を本物の
// JPEGにする。置き場の規則は thumb の外から見えないので、偽のファイルは置けない。
func (f *webFixture) addMedia(t *testing.T, name string, takenAt time.Time, kind thumbKind) media.Media {
	t.Helper()
	path := filepath.Join(f.mediaDir, name)
	body := []byte("original-" + name)
	if kind == famifoThumb {
		body = jpegBytes(t)
	}
	require.NoError(t, os.WriteFile(path, body, 0o644))

	m := media.Restore(path, takenAt, takenAt)
	require.NoError(t, f.st.Upsert(context.Background(), m))

	switch kind {
	case famifoThumb:
		require.NoError(t, f.thumbs.Prepare(m, 1))
	case eadirThumb:
		writeFileAt(t, synology.ThumbMPath(path), "eadir-"+name)
		writeFileAt(t, synology.ThumbXLPath(path), "eadir-xl-"+name)
	}
	return m
}

// jpegBytes は原本として使う小さなJPEGを返す。
func jpegBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 40, 20)), nil))
	return buf.Bytes()
}

// writeFileAt は親ディレクトリごとファイルを書く。
func writeFileAt(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

// wellFormedXML は最後まで読み切れるかでXMLの妥当性を見る。
// コメント内の "--" のように、目で見ても気づきにくい壊れ方を捕まえる。
func wellFormedXML(b []byte) error {
	dec := xml.NewDecoder(bytes.NewReader(b))
	for {
		if _, err := dec.Token(); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func TestGalleryRendersTiles(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	m := f.addMedia(t, "a.jpg", time.Unix(1600000000, 0), famifoThumb)

	rec := doGet(t, f.h, "/")

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	body := rec.Body.String()
	require.Contains(t, body, `src="/thumb/`+m.ID()+`"`)
	require.Contains(t, body, `data-full="/file/`+m.ID()+`"`)
}

func TestGalleryEmbedsTotalAndFirstChunk(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 60)
	for i := range 3 {
		f.addMedia(t, fmt.Sprintf("p%d.jpg", i), time.Unix(int64(1600000000+i), 0), famifoThumb)
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
	f.addMedia(t, "a.jpg", time.Unix(1600000000, 0), famifoThumb)

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
	m := f.addMedia(t, "a.heic", time.Unix(1600000000, 0), noThumb)

	body := doGet(t, f.h, "/").Body.String()

	require.Contains(t, body, `src="/thumb/`+m.ID()+`"`,
		"a tile points at /thumb/ even with no thumbnail; the handler falls back to the original")
	require.NotContains(t, body, `src="/file/`+m.ID()+`"`)
}

func TestGalleryOrdersNewestFirst(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	old := f.addMedia(t, "old.jpg", time.Unix(1600000000, 0), famifoThumb)
	recent := f.addMedia(t, "new.jpg", time.Unix(1700000000, 0), famifoThumb)

	body := doGet(t, f.h, "/").Body.String()

	require.Less(t, strings.Index(body, recent.ID()), strings.Index(body, old.ID()),
		"ordered by capture time, newest first")
}

func TestTilesReturnsFragmentOnly(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 1)
	f.addMedia(t, "a.jpg", time.Unix(1600000000, 0), famifoThumb)
	last := f.addMedia(t, "b.jpg", time.Unix(1700000000, 0), famifoThumb)

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
		m := f.addMedia(t, fmt.Sprintf("p%d.jpg", i), time.Unix(int64(1600000000+i), 0), famifoThumb)
		ids = append(ids, m.ID())
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
	f.addMedia(t, "a.jpg", time.Unix(1600000000, 0), famifoThumb)

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
	m := f.addMedia(t, "a.jpg", time.Unix(1600000000, 0), famifoThumb)

	body := doGet(t, f.h, "/tiles").Body.String()

	require.Contains(t, body, m.ID())
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
	f.addMedia(t, "a.jpg", time.Date(2026, 2, 8, 18, 0, 0, 0, time.Local), famifoThumb)
	f.addMedia(t, "b.jpg", time.Date(2026, 2, 8, 10, 0, 0, 0, time.Local), famifoThumb)
	f.addMedia(t, "c.jpg", time.Date(2026, 2, 3, 10, 0, 0, 0, time.Local), famifoThumb)

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
	f.addMedia(t, "a.jpg", time.Date(2026, 2, 8, 10, 0, 0, 0, time.Local), famifoThumb)

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
	f.addMedia(t, "a.jpg", time.Date(2026, 2, 8, 0, 30, 0, 0, time.Local), famifoThumb)

	body := doGet(t, f.h, "/tiles?offset=0&limit=60").Body.String()

	require.Contains(t, body, `data-date="2026-02-08"`,
		"cutting in UTC would give 2026-02-07; group by local time")
}

func TestGalleryTagsFirstChunkWithDates(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 60)
	f.addMedia(t, "a.jpg", time.Date(2026, 2, 8, 12, 0, 0, 0, time.Local), famifoThumb)

	body := doGet(t, f.h, "/").Body.String()

	require.Contains(t, body, `data-date="2026-02-08"`,
		"the first chunk of the initial HTML needs its date too")
}

func TestGalleryUsesTheBorrowedThumbForHEIC(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	m := f.addMedia(t, "a.heic", time.Unix(1600000000, 0), eadirThumb)

	body := doGet(t, f.h, "/").Body.String()

	require.Contains(t, body, `src="/thumb/`+m.ID()+`"`,
		"a HEIC that can borrow from @eaDir uses the thumbnail")
}

// 写真ごとのURLは、その写真を開いた状態のギャラリーを返す。クライアントは
// 埋め込まれた通し番号でその位置へ飛ぶので、番号が一覧の並びと一致していること。
func TestItemOpensTheGalleryAtThatItem(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	var items []string
	for i, name := range []string{"a.jpg", "b.jpg", "c.jpg"} {
		m := f.addMedia(t, name, time.Unix(int64(1600000000+i), 0), famifoThumb)
		items = append(items, m.ID())
	}

	// 新しい順に並ぶので c, b, a。真ん中の b は1番目。
	rec := doGet(t, f.h, "/item/"+items[1])

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	body := rec.Body.String()
	require.Contains(t, body, `data-open="1"`)
	require.Contains(t, body, `data-total="3"`)
}

// 消えた写真のURLを共有されても、壊れた画面ではなくギャラリーを出す。
func TestItemUnknownIDRedirectsToTheGallery(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	f.addMedia(t, "a.jpg", time.Unix(1600000000, 0), famifoThumb)

	rec := doGet(t, f.h, "/item/nosuchid")

	require.Equal(t, http.StatusFound, rec.Code)
	require.Equal(t, "/", rec.Header().Get("Location"))
}

func TestGalleryOpensNoItem(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	f.addMedia(t, "a.jpg", time.Unix(1600000000, 0), famifoThumb)

	body := doGet(t, f.h, "/").Body.String()

	require.Contains(t, body, `data-open="-1"`)
}

// タイルのリンク先は画像そのものではなく写真のページである。新しいタブで開く
// 操作や、リンクのコピーが意味のあるURLを返すようにするため。
func TestTilesLinkToTheItemPage(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	m := f.addMedia(t, "a.jpg", time.Unix(1600000000, 0), famifoThumb)

	body := doGet(t, f.h, "/").Body.String()

	require.Contains(t, body, `href="/item/`+m.ID()+`"`)
	require.Contains(t, body, `data-full="/file/`+m.ID()+`"`)
}

// タイルが動画かどうかはHTMLに出る。app.js が拡大表示の切り替えに使い、
// CSSが再生の印を重ねるのに使う。
func TestGalleryMarksVideoTiles(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	still := f.addMedia(t, "a.jpg", time.Unix(1600000000, 0), famifoThumb)
	video := f.addMedia(t, "clip.mp4", time.Unix(1600000100, 0), noThumb)

	body := doGet(t, f.h, "/").Body.String()

	require.Regexp(t, `id="t-`+video.ID()+`"[^>]*data-video="1"`, body)
	require.NotRegexp(t, `id="t-`+still.ID()+`"[^>]*data-video`, body)
}

func doGet(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestServeThumb(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	m := f.addMedia(t, "a.jpg", time.Unix(1600000000, 0), famifoThumb)

	rec := doGet(t, f.h, "/thumb/"+m.ID())

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "image/jpeg", rec.Header().Get("Content-Type"))
	original, err := os.ReadFile(m.Path())
	require.NoError(t, err)
	require.NotEqual(t, original, rec.Body.Bytes(), "the generated thumbnail is served, not the original")
	_, format, err := image.DecodeConfig(rec.Body)
	require.NoError(t, err)
	require.Equal(t, "jpeg", format)
}

func TestServeThumbNotFoundForUnknownID(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)

	rec := doGet(t, f.h, "/thumb/deadbeef")

	require.Equal(t, http.StatusNotFound, rec.Code)
}

// サムネイルが無くても、ブラウザが表示できる形式なら原本がそのままタイルになる。
func TestServeThumbFallsBackToTheOriginal(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	m := f.addMedia(t, "a.jpg", time.Unix(1600000000, 0), noThumb)

	rec := doGet(t, f.h, "/thumb/"+m.ID())

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "original-a.jpg", rec.Body.String())
	require.Equal(t, "image/jpeg", rec.Header().Get("Content-Type"))
}

// HEICの原本はブラウザが表示できないので、配るとタイルが割れる。
// 一覧のタイルは出どころによらず /thumb/ を指すので、404にすると穴が開く。
// 代わりにプレースホルダを配る。
func TestServeThumbServesAPlaceholderWhenNothingCanBeShown(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	m := f.addMedia(t, "a.heic", time.Unix(1600000000, 0), noThumb)

	rec := doGet(t, f.h, "/thumb/"+m.ID())

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "image/svg+xml", rec.Header().Get("Content-Type"))
	require.NotContains(t, rec.Body.String(), "original-a.heic")
	require.Contains(t, rec.Body.String(), "<svg")
	require.NoError(t, wellFormedXML(rec.Body.Bytes()),
		"image/svg+xml is parsed strictly as XML, so anything invalid does not render")
	require.Equal(t, "no-store", rec.Header().Get("Cache-Control"),
		"so the next view can switch to a thumbnail DSM made later")
}

// 取り込みのあとでDSMがサムネイルを作った場合。出どころをDBに焼いていたころは、
// 再取り込みされない限り原本を配信し続けていた。
func TestServeThumbPicksUpAThumbThatAppearsAfterIndexing(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	m := f.addMedia(t, "a.heic", time.Unix(1600000000, 0), noThumb)
	require.Equal(t, "image/svg+xml",
		doGet(t, f.h, "/thumb/"+m.ID()).Header().Get("Content-Type"), "there is nothing to serve yet")

	writeFileAt(t, synology.ThumbMPath(m.Path()), "eadir-a.heic")

	rec := doGet(t, f.h, "/thumb/"+m.ID())

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "eadir-a.heic", rec.Body.String(), "it switches over without reindexing")
}

func TestServeOriginal(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	m := f.addMedia(t, "a.jpg", time.Unix(1600000000, 0), famifoThumb)

	rec := doGet(t, f.h, "/file/"+m.ID())

	require.Equal(t, http.StatusOK, rec.Code)
	original, err := os.ReadFile(m.Path())
	require.NoError(t, err)
	require.Equal(t, original, rec.Body.Bytes())
	require.Equal(t, "image/jpeg", rec.Header().Get("Content-Type"))
}

func TestServeOriginalSetsHEICContentType(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	m := f.addMedia(t, "a.heic", time.Unix(1600000000, 0), noThumb)

	rec := doGet(t, f.h, "/file/"+m.ID())

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "original-a.heic", rec.Body.String(),
		"a HEIC with nothing to borrow still serves the original")
	require.Equal(t, "image/heic", rec.Header().Get("Content-Type"),
		"Go's mime package does not know it, so it is set by hand")
}

func TestServeOriginalNotFoundForUnknownID(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)

	rec := doGet(t, f.h, "/file/deadbeef")

	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestUnindexedPathsAreNotReachable(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	// パスではなくIDでしか引けないため、traversalは構造的に成立しない
	for _, target := range []string{
		"/file/../../etc/passwd",
		"/thumb/..%2f..%2fetc%2fpasswd",
		"/file/" + media.IDFor("/etc/passwd"),
	} {
		t.Run(target, func(t *testing.T) {
			rec := doGet(t, f.h, target)
			require.NotEqual(t, http.StatusOK, rec.Code)
		})
	}
}

func TestServeThumbFromEaDir(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	m := f.addMedia(t, "a.heic", time.Unix(1600000000, 0), eadirThumb)

	rec := doGet(t, f.h, "/thumb/"+m.ID())

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "eadir-a.heic", rec.Body.String())
}

func TestServeHEICBorrowsTheLargeThumbFromEaDir(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	m := f.addMedia(t, "a.heic", time.Unix(1600000000, 0), eadirThumb)

	rec := doGet(t, f.h, "/file/"+m.ID())

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "eadir-xl-a.heic", rec.Body.String(),
		"a HEIC original displays nowhere but Safari, so XL is served instead")
	require.Equal(t, "image/jpeg", rec.Header().Get("Content-Type"),
		"what is served is a JPEG, so the MIME type comes from the extension")
}

func TestServeOriginalForRasterEvenWithEaDir(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	m := f.addMedia(t, "a.jpg", time.Unix(1600000000, 0), eadirThumb)

	rec := doGet(t, f.h, "/file/"+m.ID())

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "original-a.jpg", rec.Body.String(),
		"a format that displays as it is gets the original at full resolution; borrowing is only for what cannot be shown")
}

// HEVCの原本はハードウェアデコーダを持たない端末で再生できない。Synologyが作った
// H.264版があるならそれを配る。HEICで XL を借りているのと同じ構えである。
func TestServeVideoBorrowsTheTranscode(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	m := f.addMedia(t, "clip.mp4", time.Unix(1600000000, 0), noThumb)
	writeFileAt(t, synology.FilmPath(m.Path()), "film-clip.mp4")

	rec := doGet(t, f.h, "/file/"+m.ID())

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "film-clip.mp4", rec.Body.String())
	require.Equal(t, "video/mp4", rec.Header().Get("Content-Type"))
}

// 借りるものが無ければ原本に落ちる。再生できるかは端末次第になる。
func TestServeOriginalVideoWithoutATranscode(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	m := f.addMedia(t, "clip.mov", time.Unix(1600000000, 0), noThumb)

	rec := doGet(t, f.h, "/file/"+m.ID())

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "original-clip.mov", rec.Body.String())
	require.Equal(t, "video/quicktime", rec.Header().Get("Content-Type"))
}

// サムネイルがあっても変換版があるとは限らない。実測した @eaDir には
// SYNOPHOTO_THUMB_M.jpg があるのに SYNOPHOTO_FILM.fail があった。写真のXLのように
// 一方から他方を導けないので、動画では静止画のXLを掴んでしまってもいけない。
func TestServeVideoDoesNotBorrowTheXL(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	m := f.addMedia(t, "clip.mp4", time.Unix(1600000000, 0), eadirThumb)

	rec := doGet(t, f.h, "/file/"+m.ID())

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "original-clip.mp4", rec.Body.String(), "an XL still is not what you play")
	require.Equal(t, "video/mp4", rec.Header().Get("Content-Type"))
}

// TestResponsesCarryTheSecurityHeaders は、どの経路の応答にも方針とnosniffが
// 載ることを固定する。認証の内側（/、/tiles）と、認証もセッションも通らない
// /static/ の両方を見る。ミドルウェアの掛け場所を内側に動かすと /static/ だけ
// 素の応答に戻るが、画面は何も変わらないので気づけない。
func TestResponsesCarryTheSecurityHeaders(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	m := f.addMedia(t, "a.jpg", time.Unix(1600000000, 0), famifoThumb)

	for _, target := range []string{"/", "/tiles", "/static/app.css", "/thumb/" + m.ID(), "/file/" + m.ID()} {
		t.Run(target, func(t *testing.T) {
			rec := doGet(t, f.h, target)

			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
			require.NotEmpty(t, rec.Header().Get("Content-Security-Policy"))
		})
	}
}

// TestTheContentSecurityPolicyKeepsScriptsStrict は script-src に 'unsafe-inline'
// が混ざらないことを固定する。方針を入れる理由そのものがここであり、ゆるめても
// 画面は正常に動き続けるため、テストでしか守れない。
//
// style-src のほうは 'unsafe-inline' を許してある。app.js の cardHTML が
// style 属性を持つ日カードを組み立てており、外すとレイアウトが崩れる。
// 取り違えて script 側に足されるのを防ぐため、両者を別々に見る。
func TestTheContentSecurityPolicyKeepsScriptsStrict(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)

	policy := doGet(t, f.h, "/").Header().Get("Content-Security-Policy")

	directives := make(map[string]string)
	for _, d := range strings.Split(policy, ";") {
		name, value, _ := strings.Cut(strings.TrimSpace(d), " ")
		directives[name] = value
	}
	require.Equal(t, "'self'", directives["script-src"])
	require.Contains(t, directives["style-src"], "'unsafe-inline'",
		"the day cards carry style attributes; see cardHTML in app.js")
	require.Equal(t, "'none'", directives["default-src"],
		"anything not listed must stay blocked, so a new kind of resource fails loudly")
}
