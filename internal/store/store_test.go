package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s
}

func photoAt(path string, takenAt time.Time) Photo {
	return Photo{
		ID:       IDFor(path),
		Path:     path,
		TakenAt:  takenAt,
		ModTime:  takenAt,
		Size:     1234,
		Ext:      ".jpg",
		HasThumb: true,
	}
}

func TestIDForIsStableAndDistinct(t *testing.T) {
	a := IDFor("/photos/a.jpg")

	require.Len(t, a, 32)
	require.Equal(t, a, IDFor("/photos/a.jpg"))
	require.NotEqual(t, a, IDFor("/photos/b.jpg"))
}

func TestOpenEnablesWAL(t *testing.T) {
	s := openTestStore(t)

	var mode string
	require.NoError(t, s.db.QueryRow("PRAGMA journal_mode").Scan(&mode))
	require.Equal(t, "wal", mode)
}

func TestUpsertThenGetByID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	want := photoAt("/photos/a.jpg", time.Unix(1600000000, 0))

	require.NoError(t, s.Upsert(ctx, want))
	got, err := s.GetByID(ctx, want.ID)

	require.NoError(t, err)
	require.Equal(t, want.Path, got.Path)
	require.Equal(t, want.TakenAt.Unix(), got.TakenAt.Unix())
	require.Equal(t, want.Size, got.Size)
	require.True(t, got.HasThumb)
}

func TestUpsertReplacesExistingRow(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	p := photoAt("/photos/a.jpg", time.Unix(1600000000, 0))
	require.NoError(t, s.Upsert(ctx, p))

	p.TakenAt = time.Unix(1700000000, 0)
	p.HasThumb = false
	require.NoError(t, s.Upsert(ctx, p))

	got, err := s.GetByID(ctx, p.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1700000000), got.TakenAt.Unix())
	require.False(t, got.HasThumb)

	n, err := s.Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

func TestGetByIDMissingReturnsErrNotFound(t *testing.T) {
	s := openTestStore(t)

	_, err := s.GetByID(context.Background(), "deadbeef")

	require.ErrorIs(t, err, ErrNotFound)
}

func TestDeleteByPath(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	p := photoAt("/photos/a.jpg", time.Unix(1600000000, 0))
	require.NoError(t, s.Upsert(ctx, p))

	got, ok, err := s.DeleteByPath(ctx, p.Path)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, p.ID, got.ID)
	require.True(t, got.HasThumb) // サムネイル削除の判断に使う

	_, ok, err = s.DeleteByPath(ctx, p.Path)
	require.NoError(t, err)
	require.False(t, ok, "2回目の削除は見つからないと報告する")
}

func TestListPagePaginatesNewestFirst(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	// 撮影日時が新しい順に c, b, a になるよう投入する
	for i, name := range []string{"a", "b", "c"} {
		p := photoAt("/photos/"+name+".jpg", time.Unix(int64(1600000000+i), 0))
		require.NoError(t, s.Upsert(ctx, p))
	}

	first, err := s.ListPage(ctx, Cursor{}, 2)
	require.NoError(t, err)
	require.Len(t, first, 2)
	require.Equal(t, "/photos/c.jpg", first[0].Path)
	require.Equal(t, "/photos/b.jpg", first[1].Path)

	last := first[len(first)-1]
	second, err := s.ListPage(ctx, Cursor{TakenAt: last.TakenAt, ID: last.ID, Set: true}, 2)
	require.NoError(t, err)
	require.Len(t, second, 1)
	require.Equal(t, "/photos/a.jpg", second[0].Path)
}

func TestListPageBreaksTiesByID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	same := time.Unix(1600000000, 0)
	for _, name := range []string{"a", "b", "c"} {
		require.NoError(t, s.Upsert(ctx, photoAt("/photos/"+name+".jpg", same)))
	}

	// 撮影日時が全て同じでも、ページをまたいで重複や欠落が起きないこと
	var seen []string
	cur := Cursor{}
	for range 3 {
		page, err := s.ListPage(ctx, cur, 1)
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		seen = append(seen, page[0].Path)
		cur = Cursor{TakenAt: page[0].TakenAt, ID: page[0].ID, Set: true}
	}

	require.ElementsMatch(t, []string{"/photos/a.jpg", "/photos/b.jpg", "/photos/c.jpg"}, seen)
}

func TestAllPaths(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	p := photoAt("/photos/a.jpg", time.Unix(1600000000, 0))
	p.ModTime = time.Unix(1650000000, 0)
	require.NoError(t, s.Upsert(ctx, p))

	got, err := s.AllPaths(ctx)

	require.NoError(t, err)
	require.Equal(t, map[string]int64{"/photos/a.jpg": 1650000000}, got)
}
