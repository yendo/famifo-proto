package web_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/web"

	"github.com/yendo/famifo-proto/internal/photo"
	"github.com/yendo/famifo-proto/internal/store"
	"github.com/yendo/famifo-proto/internal/synology"
	"github.com/yendo/famifo-proto/internal/thumb"
)

type webFixture struct {
	h        http.Handler
	st       *store.Store
	thumbs   *thumb.Provider
	photoDir string
}

func newWebFixture(t *testing.T, chunkSize int) *webFixture {
	t.Helper()
	base := t.TempDir()
	st, err := store.Open(filepath.Join(base, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })

	thumbs, err := thumb.NewProvider(filepath.Join(base, "thumbs"))
	require.NoError(t, err)
	photoDir := filepath.Join(base, "photos")
	require.NoError(t, os.MkdirAll(photoDir, 0o755))

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := web.NewServer(st, thumbs, log)
	require.NoError(t, err)
	srv.SetChunkSize(chunkSize)
	return &webFixture{h: srv.Handler(), st: st, thumbs: thumbs, photoDir: photoDir}
}

// thumbKind は addPhoto がどのサムネイルをディスクに置くかを指定する。
// 出どころはDBの列ではなくファイルの有無で決まるので、テストが用意するのもファイルである。
type thumbKind int

const (
	noThumb     thumbKind = iota // 借りるものも作ったものも無い
	famifoThumb                  // 自前で生成したものがある
	eadirThumb                   // Synologyのものが @eaDir にある
)

// addPhoto は原本ファイルとDB行を用意する。kind に応じてサムネイルも置く。
func (f *webFixture) addPhoto(t *testing.T, name string, takenAt time.Time, kind thumbKind) photo.Photo {
	t.Helper()
	path := filepath.Join(f.photoDir, name)
	require.NoError(t, os.WriteFile(path, []byte("original-"+name), 0o644))

	p := photo.Restore(path, takenAt, takenAt)
	require.NoError(t, f.st.Upsert(context.Background(), p))

	switch kind {
	case famifoThumb:
		writeFileAt(t, f.thumbs.GeneratedPath(p), "thumb-"+name)
	case eadirThumb:
		writeFileAt(t, synology.ThumbMPath(path), "eadir-"+name)
		writeFileAt(t, synology.ThumbXLPath(path), "eadir-xl-"+name)
	}
	return p
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
	p := f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), famifoThumb)

	rec := doGet(t, f.h, "/thumb/"+p.ID())

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "thumb-a.jpg", rec.Body.String())
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
	p := f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), noThumb)

	rec := doGet(t, f.h, "/thumb/"+p.ID())

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
	p := f.addPhoto(t, "a.heic", time.Unix(1600000000, 0), noThumb)

	rec := doGet(t, f.h, "/thumb/"+p.ID())

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
	p := f.addPhoto(t, "a.heic", time.Unix(1600000000, 0), noThumb)
	require.Equal(t, "image/svg+xml",
		doGet(t, f.h, "/thumb/"+p.ID()).Header().Get("Content-Type"), "there is nothing to serve yet")

	writeFileAt(t, synology.ThumbMPath(p.Path()), "eadir-a.heic")

	rec := doGet(t, f.h, "/thumb/"+p.ID())

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "eadir-a.heic", rec.Body.String(), "it switches over without reindexing")
}

func TestServeOriginal(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), famifoThumb)

	rec := doGet(t, f.h, "/photo/"+p.ID())

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "original-a.jpg", rec.Body.String())
	require.Equal(t, "image/jpeg", rec.Header().Get("Content-Type"))
}

func TestServeOriginalSetsHEICContentType(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.heic", time.Unix(1600000000, 0), noThumb)

	rec := doGet(t, f.h, "/photo/"+p.ID())

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "original-a.heic", rec.Body.String(),
		"a HEIC with nothing to borrow still serves the original")
	require.Equal(t, "image/heic", rec.Header().Get("Content-Type"),
		"Go's mime package does not know it, so it is set by hand")
}

func TestServeOriginalNotFoundForUnknownID(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)

	rec := doGet(t, f.h, "/photo/deadbeef")

	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestUnindexedPathsAreNotReachable(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	// パスではなくIDでしか引けないため、traversalは構造的に成立しない
	for _, target := range []string{
		"/photo/../../etc/passwd",
		"/thumb/..%2f..%2fetc%2fpasswd",
		"/photo/" + photo.IDFor("/etc/passwd"),
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
	p := f.addPhoto(t, "a.heic", time.Unix(1600000000, 0), eadirThumb)

	rec := doGet(t, f.h, "/thumb/"+p.ID())

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "eadir-a.heic", rec.Body.String())
}

func TestServeHEICBorrowsTheLargeThumbFromEaDir(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.heic", time.Unix(1600000000, 0), eadirThumb)

	rec := doGet(t, f.h, "/photo/"+p.ID())

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "eadir-xl-a.heic", rec.Body.String(),
		"a HEIC original displays nowhere but Safari, so XL is served instead")
	require.Equal(t, "image/jpeg", rec.Header().Get("Content-Type"),
		"what is served is a JPEG, so the MIME type comes from the extension")
}

func TestServeOriginalForRasterEvenWithEaDir(t *testing.T) {
	t.Parallel()
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), eadirThumb)

	rec := doGet(t, f.h, "/photo/"+p.ID())

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "original-a.jpg", rec.Body.String(),
		"a format that displays as it is gets the original at full resolution; borrowing is only for what cannot be shown")
}
