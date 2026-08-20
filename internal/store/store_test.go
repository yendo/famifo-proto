package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
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

func TestDeleteByPathPrefixIsSeparatorTerminated(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := photoAt("/p/album/a.jpg", time.Unix(1600000000, 0))
	b := photoAt("/p/album2/b.jpg", time.Unix(1600000001, 0))
	require.NoError(t, s.Upsert(ctx, a))
	require.NoError(t, s.Upsert(ctx, b))

	deleted, err := s.DeleteByPathPrefix(ctx, "/p/album")

	require.NoError(t, err)
	require.Len(t, deleted, 1, "album2 まで巻き込んではいけない")
	require.Equal(t, a.Path, deleted[0].Path)

	n, err := s.Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n, "album2 の行は残る")
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

func TestListRangeReturnsRequestedWindow(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	// 新しい順に e, d, c, b, a になるよう投入する
	for i, name := range []string{"a", "b", "c", "d", "e"} {
		require.NoError(t, s.Upsert(ctx, photoAt("/photos/"+name+".jpg", time.Unix(int64(1600000000+i), 0))))
	}

	got, err := s.ListRange(ctx, 1, 2)

	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "/photos/d.jpg", got[0].Path)
	require.Equal(t, "/photos/c.jpg", got[1].Path)
}

func TestListRangeOrdersNewestFirstWithIDTiebreak(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	same := time.Unix(1600000000, 0)
	for _, name := range []string{"a", "b", "c"} {
		require.NoError(t, s.Upsert(ctx, photoAt("/photos/"+name+".jpg", same)))
	}

	// 撮影日時が同じ場合は id の降順で安定すること。
	// 順序が崩れるとページ境界で重複や欠落が起きる。
	//
	// 注意: 複合インデックス idx_photos_order が物理行順を安定させるため、
	// ORDER BY 句から id DESC を外しただけではこのテストは落ちない。
	// 不変条件は「ORDER BY 句 + インデックス」の組で担保されており、
	// テストで句の削除だけを検出することは原理的にできない。
	paths := []string{"/photos/a.jpg", "/photos/b.jpg", "/photos/c.jpg"}
	want := append([]string(nil), paths...)
	sort.Slice(want, func(i, j int) bool { return IDFor(want[i]) > IDFor(want[j]) })

	var seen []string
	for i := range 3 {
		page, err := s.ListRange(ctx, i, 1)
		require.NoError(t, err)
		require.Len(t, page, 1)
		seen = append(seen, page[0].Path)
	}

	require.Equal(t, want, seen)
}

func TestListRangeHandlesBoundaries(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for i, name := range []string{"a", "b", "c"} {
		require.NoError(t, s.Upsert(ctx, photoAt("/photos/"+name+".jpg", time.Unix(int64(1600000000+i), 0))))
	}

	tail, err := s.ListRange(ctx, 2, 10) // limitが残数を超える
	require.NoError(t, err)
	require.Len(t, tail, 1)

	past, err := s.ListRange(ctx, 99, 10) // 範囲外
	require.NoError(t, err)
	require.Empty(t, past)

	zero, err := s.ListRange(ctx, 0, 0) // limit=0
	require.NoError(t, err)
	require.Empty(t, zero)
}

func TestListRangeRejectsNegativeArguments(t *testing.T) {
	s := openTestStore(t)

	_, err := s.ListRange(context.Background(), -1, 10)
	require.Error(t, err)

	_, err = s.ListRange(context.Background(), 0, -1)
	require.Error(t, err)
}

func TestMonthOffsetsMarksMonthBoundaries(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	// 新しい順に: 2022-12 が2枚、2022-11 が1枚、2021-05 が2枚
	times := []time.Time{
		time.Date(2022, 12, 5, 10, 0, 0, 0, time.Local),
		time.Date(2022, 12, 1, 10, 0, 0, 0, time.Local),
		time.Date(2022, 11, 8, 10, 0, 0, 0, time.Local),
		time.Date(2021, 5, 20, 10, 0, 0, 0, time.Local),
		time.Date(2021, 5, 2, 10, 0, 0, 0, time.Local),
	}
	for i, at := range times {
		require.NoError(t, s.Upsert(ctx, photoAt(fmt.Sprintf("/photos/%d.jpg", i), at)))
	}

	got, err := s.MonthOffsets(ctx)

	require.NoError(t, err)
	require.Equal(t, []MonthOffset{
		{Month: "2022-12", Offset: 0},
		{Month: "2022-11", Offset: 2},
		{Month: "2021-05", Offset: 3},
	}, got)
}

func TestMonthOffsetsUsesLocalTime(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	// TZ=UTC の環境ではローカル集計とUTC集計が同じ結果になり、
	// このテストが回帰を検出できなくなる。テスト中だけ固定オフセットに差し替える。
	orig := time.Local
	time.Local = time.FixedZone("JST", 9*60*60)
	t.Cleanup(func() { time.Local = orig })

	// ローカルで11月1日の未明。UTCに直すと10月31日になる時刻を選ぶ。
	at := time.Date(2022, 11, 1, 0, 30, 0, 0, time.Local)
	require.NoError(t, s.Upsert(ctx, photoAt("/photos/a.jpg", at)))

	got, err := s.MonthOffsets(ctx)

	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "2022-11", got[0].Month,
		"UTCで切ると2022-10になる。ローカル時刻で分類すること")
}

func TestMonthOffsetsEmptyStore(t *testing.T) {
	s := openTestStore(t)

	got, err := s.MonthOffsets(context.Background())

	require.NoError(t, err)
	require.Empty(t, got)
}
