package web_test

import (
	"bytes"
	"context"
	"encoding/xml"
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
	gallery, err := web.NewGallery(st, thumbs, nil, log)
	require.NoError(t, err)
	gallery.SetChunkSize(chunkSize)
	return &webFixture{h: gallery.Handler(), st: st, thumbs: thumbs, mediaDir: mediaDir}
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
