package web_test

import (
	"context"
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
)

type webFixture struct {
	h        http.Handler
	st       *store.Store
	thumbDir string
	photoDir string
}

func newWebFixture(t *testing.T, pageSize int) *webFixture {
	t.Helper()
	base := t.TempDir()
	st, err := store.Open(filepath.Join(base, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })

	thumbDir := filepath.Join(base, "thumbs")
	require.NoError(t, os.MkdirAll(thumbDir, 0o755))
	photoDir := filepath.Join(base, "photos")
	require.NoError(t, os.MkdirAll(photoDir, 0o755))

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := web.NewServer(st, thumbDir, pageSize, log)
	require.NoError(t, err)
	return &webFixture{h: srv.Handler(), st: st, thumbDir: thumbDir, photoDir: photoDir}
}

// addPhoto は原本ファイルとDB行を用意する。srcに応じてサムネイルも置く。
func (f *webFixture) addPhoto(t *testing.T, name string, takenAt time.Time, src photo.ThumbSource) photo.Photo {
	t.Helper()
	path := filepath.Join(f.photoDir, name)
	require.NoError(t, os.WriteFile(path, []byte("original-"+name), 0o644))

	p := photo.Photo{
		ID: photo.IDFor(path), Path: path, TakenAt: takenAt, ModTime: takenAt,
		Size: 10, Ext: filepath.Ext(name), ThumbSource: src,
	}
	require.NoError(t, f.st.Upsert(context.Background(), p))

	switch src {
	case photo.ThumbFamifo:
		writeFileAt(t, photo.FamifoThumbPath(f.thumbDir, p.ID), "thumb-"+name)
	case photo.ThumbSyno:
		writeFileAt(t, synology.ThumbPath(path), "eadir-"+name)
		writeFileAt(t, synology.LargePath(path), "eadir-xl-"+name)
	}
	return p
}

// writeFileAt は親ディレクトリごとファイルを書く。
func writeFileAt(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

func do(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestServeThumb(t *testing.T) {
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), photo.ThumbFamifo)

	rec := do(t, f.h, "/thumb/"+p.ID)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "thumb-a.jpg", rec.Body.String())
}

func TestServeThumbNotFoundForUnknownID(t *testing.T) {
	f := newWebFixture(t, 10)

	rec := do(t, f.h, "/thumb/deadbeef")

	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestServeThumbNotFoundWhenPhotoHasNone(t *testing.T) {
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.heic", time.Unix(1600000000, 0), photo.ThumbNone)

	rec := do(t, f.h, "/thumb/"+p.ID)

	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestServeOriginal(t *testing.T) {
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), photo.ThumbFamifo)

	rec := do(t, f.h, "/photo/"+p.ID)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "original-a.jpg", rec.Body.String())
	require.Equal(t, "image/jpeg", rec.Header().Get("Content-Type"))
}

func TestServeOriginalSetsHEICContentType(t *testing.T) {
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.heic", time.Unix(1600000000, 0), photo.ThumbNone)

	rec := do(t, f.h, "/photo/"+p.ID)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "original-a.heic", rec.Body.String(),
		"借りるものが無いHEICは従来どおり原本を配信する")
	require.Equal(t, "image/heic", rec.Header().Get("Content-Type"),
		"Goのmimeパッケージが知らないので自前で設定する")
}

func TestServeOriginalNotFoundForUnknownID(t *testing.T) {
	f := newWebFixture(t, 10)

	rec := do(t, f.h, "/photo/deadbeef")

	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestUnindexedPathsAreNotReachable(t *testing.T) {
	f := newWebFixture(t, 10)
	// パスではなくIDでしか引けないため、traversalは構造的に成立しない
	for _, target := range []string{
		"/photo/../../etc/passwd",
		"/thumb/..%2f..%2fetc%2fpasswd",
		"/photo/" + photo.IDFor("/etc/passwd"),
	} {
		t.Run(target, func(t *testing.T) {
			rec := do(t, f.h, target)
			require.NotEqual(t, http.StatusOK, rec.Code)
		})
	}
}

func TestServeThumbFromEaDir(t *testing.T) {
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.heic", time.Unix(1600000000, 0), photo.ThumbSyno)

	rec := do(t, f.h, "/thumb/"+p.ID)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "eadir-a.heic", rec.Body.String())
}

func TestServeHEICBorrowsTheLargeThumbFromEaDir(t *testing.T) {
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.heic", time.Unix(1600000000, 0), photo.ThumbSyno)

	rec := do(t, f.h, "/photo/"+p.ID)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "eadir-xl-a.heic", rec.Body.String(),
		"HEICの原本はSafari以外で表示できないのでXLを代わりに配信する")
	require.Equal(t, "image/jpeg", rec.Header().Get("Content-Type"),
		"配信するのはJPEGなので拡張子からMIMEが引ける")
}

func TestServeOriginalForRasterEvenWithEaDir(t *testing.T) {
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), photo.ThumbSyno)

	rec := do(t, f.h, "/photo/"+p.ID)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "original-a.jpg", rec.Body.String(),
		"元から表示できる形式は原本のフル解像度を出す。借用は見えないものの代替に限る")
}
