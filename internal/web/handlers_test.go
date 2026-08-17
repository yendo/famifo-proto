package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/yendo/famifo-proto/internal/store"
	"github.com/yendo/famifo-proto/internal/thumb"
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

	srv, err := NewServer(st, thumbDir, pageSize)
	require.NoError(t, err)
	return &webFixture{h: srv.Handler(), st: st, thumbDir: thumbDir, photoDir: photoDir}
}

// addPhoto は原本ファイルとDB行を用意する。hasThumbならサムネイルも置く。
func (f *webFixture) addPhoto(t *testing.T, name string, takenAt time.Time, hasThumb bool) store.Photo {
	t.Helper()
	path := filepath.Join(f.photoDir, name)
	require.NoError(t, os.WriteFile(path, []byte("original-"+name), 0o644))

	p := store.Photo{
		ID: store.IDFor(path), Path: path, TakenAt: takenAt, ModTime: takenAt,
		Size: 10, Ext: filepath.Ext(name), HasThumb: hasThumb,
	}
	require.NoError(t, f.st.Upsert(context.Background(), p))

	if hasThumb {
		tp := filepath.Join(f.thumbDir, thumb.RelPath(p.ID))
		require.NoError(t, os.MkdirAll(filepath.Dir(tp), 0o755))
		require.NoError(t, os.WriteFile(tp, []byte("thumb-"+name), 0o644))
	}
	return p
}

func do(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestServeThumb(t *testing.T) {
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), true)

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
	p := f.addPhoto(t, "a.heic", time.Unix(1600000000, 0), false)

	rec := do(t, f.h, "/thumb/"+p.ID)

	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestServeOriginal(t *testing.T) {
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), true)

	rec := do(t, f.h, "/photo/"+p.ID)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "original-a.jpg", rec.Body.String())
	require.Equal(t, "image/jpeg", rec.Header().Get("Content-Type"))
}

func TestServeOriginalSetsHEICContentType(t *testing.T) {
	f := newWebFixture(t, 10)
	p := f.addPhoto(t, "a.heic", time.Unix(1600000000, 0), false)

	rec := do(t, f.h, "/photo/"+p.ID)

	require.Equal(t, http.StatusOK, rec.Code)
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
		"/photo/" + store.IDFor("/etc/passwd"),
	} {
		t.Run(target, func(t *testing.T) {
			rec := do(t, f.h, target)
			require.NotEqual(t, http.StatusOK, rec.Code)
		})
	}
}
