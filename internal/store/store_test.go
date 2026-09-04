package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/photo"
	"github.com/yendo/famifo-proto/internal/store"
	_ "modernc.org/sqlite" // PRAGMAを直接読むために自前で接続する
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s
}

func photoAt(path string, takenAt time.Time) photo.Photo {
	return photoWithThumb(path, takenAt, photo.ThumbFamifo)
}

func photoWithThumb(path string, takenAt time.Time, thumbSource photo.ThumbSource) photo.Photo {
	return photo.Restore(path, takenAt, takenAt, 1234, thumbSource)
}

// store.Open がディレクトリを用意するので、呼び出し側は順序を気にしなくてよい。
// 他のテストは t.TempDir() を直接使うため、この経路をどれも通らない。
func TestOpenCreatesTheDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "famifo-data")

	s, err := store.Open(filepath.Join(dir, "famifo.db"))

	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	require.FileExists(t, filepath.Join(dir, "famifo.db"))
}

func TestOpenEnablesWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(path)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	// WALはデータベースファイルに記録される永続的な性質なので、Openが張った
	// 接続ではなく、別に開いた接続から確かめられる。
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()

	var mode string
	require.NoError(t, db.QueryRow("PRAGMA journal_mode").Scan(&mode))
	require.Equal(t, "wal", mode)
}

func TestUpsertThenGetByID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	want := photoAt("/photos/a.jpg", time.Unix(1600000000, 0))

	require.NoError(t, s.Upsert(ctx, want))
	got, err := s.GetByID(ctx, want.ID())

	require.NoError(t, err)
	require.Equal(t, want.Path(), got.Path())
	require.Equal(t, want.TakenAt().Unix(), got.TakenAt().Unix())
	require.Equal(t, want.Size(), got.Size())
	require.Equal(t, photo.ThumbFamifo, got.ThumbSource())
}

func TestUpsertReplacesExistingRow(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	p := photoAt("/photos/a.jpg", time.Unix(1600000000, 0))
	require.NoError(t, s.Upsert(ctx, p))

	p = photoWithThumb(p.Path(), time.Unix(1700000000, 0), photo.ThumbNone)
	require.NoError(t, s.Upsert(ctx, p))

	got, err := s.GetByID(ctx, p.ID())
	require.NoError(t, err)
	require.Equal(t, int64(1700000000), got.TakenAt().Unix())
	require.Equal(t, photo.ThumbNone, got.ThumbSource())

	n, err := s.Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

func TestGetByIDMissingReturnsErrNotFound(t *testing.T) {
	s := openTestStore(t)

	_, err := s.GetByID(context.Background(), "deadbeef")

	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestDeleteByPath(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	p := photoAt("/photos/a.jpg", time.Unix(1600000000, 0))
	require.NoError(t, s.Upsert(ctx, p))

	got, ok, err := s.DeleteByPath(ctx, p.Path())
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, p.ID(), got.ID())
	require.Equal(t, photo.ThumbFamifo, got.ThumbSource()) // サムネイル削除の判断に使う

	_, ok, err = s.DeleteByPath(ctx, p.Path())
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
	require.Equal(t, a.Path(), deleted[0].Path())

	n, err := s.Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n, "album2 の行は残る")
}

func TestAllPaths(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	p := photo.Restore("/photos/a.jpg",
		time.Unix(1600000000, 0), time.Unix(1650000000, 0), 1234, photo.ThumbFamifo)
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
	require.Equal(t, "/photos/d.jpg", got[0].Path())
	require.Equal(t, "/photos/c.jpg", got[1].Path())
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
	sort.Slice(want, func(i, j int) bool { return photo.IDFor(want[i]) > photo.IDFor(want[j]) })

	var seen []string
	for i := range 3 {
		page, err := s.ListRange(ctx, i, 1)
		require.NoError(t, err)
		require.Len(t, page, 1)
		seen = append(seen, page[0].Path())
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

func TestDayGroupsCountsEachDay(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	// 新しい順に: 2022-12-05 が2枚、2022-12-01 が1枚、2021-05-20 が2枚
	times := []time.Time{
		time.Date(2022, 12, 5, 18, 0, 0, 0, time.Local),
		time.Date(2022, 12, 5, 10, 0, 0, 0, time.Local),
		time.Date(2022, 12, 1, 10, 0, 0, 0, time.Local),
		time.Date(2021, 5, 20, 20, 0, 0, 0, time.Local),
		time.Date(2021, 5, 20, 9, 0, 0, 0, time.Local),
	}
	for i, at := range times {
		require.NoError(t, s.Upsert(ctx, photoAt(fmt.Sprintf("/photos/%d.jpg", i), at)))
	}

	got, err := s.DayGroups(ctx)

	require.NoError(t, err)
	require.Equal(t, []store.DayGroup{
		{Date: "2022-12-05", Count: 2},
		{Date: "2022-12-01", Count: 1},
		{Date: "2021-05-20", Count: 2},
	}, got)
}

func TestDayGroupsUsesLocalTime(t *testing.T) {
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

	got, err := s.DayGroups(ctx)

	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "2022-11-01", got[0].Date,
		"UTCで切ると2022-10-31になる。ローカル時刻で分類すること")
}

func TestDayGroupsEmptyStore(t *testing.T) {
	s := openTestStore(t)

	got, err := s.DayGroups(context.Background())

	require.NoError(t, err)
	require.Empty(t, got)
}

// 表と実際の並びがズレると一覧全体が崩れるため、両者の整合を直接押さえる。
func TestDayGroupsTotalMatchesCountAndListRange(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 2, 8, 12, 0, 0, 0, time.Local)
	// 1枚の日・3枚の日・5枚の日を混ぜる
	perDay := []int{5, 1, 3}
	n := 0
	for d, count := range perDay {
		for k := 0; k < count; k++ {
			at := base.AddDate(0, 0, -d).Add(time.Duration(-k) * time.Hour)
			require.NoError(t, s.Upsert(ctx, photoAt(fmt.Sprintf("/photos/%d.jpg", n), at)))
			n++
		}
	}

	groups, err := s.DayGroups(ctx)
	require.NoError(t, err)

	total, err := s.Count(ctx)
	require.NoError(t, err)
	sum := 0
	for _, g := range groups {
		sum += g.Count
	}
	require.Equal(t, total, sum, "枚数の合計がCountと一致すること")

	// 区切り位置が ListRange の並びと合うこと
	all, err := s.ListRange(ctx, 0, total)
	require.NoError(t, err)
	offset := 0
	for _, g := range groups {
		for k := 0; k < g.Count; k++ {
			require.Equal(t, g.Date, all[offset].TakenAt().Format("2006-01-02"),
				"offset=%d の写真は %s のはず", offset, g.Date)
			offset++
		}
	}
}

func TestUpsertRoundTripsEveryThumbSource(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	for _, thumbSource := range []photo.ThumbSource{photo.ThumbNone, photo.ThumbFamifo, photo.ThumbSyno} {
		t.Run(string(thumbSource), func(t *testing.T) {
			p := photoWithThumb("/photos/"+string(thumbSource)+".jpg", time.Unix(1600000000, 0), thumbSource)
			require.NoError(t, s.Upsert(ctx, p))

			got, err := s.GetByID(ctx, p.ID())
			require.NoError(t, err)
			require.Equal(t, thumbSource, got.ThumbSource())
		})
	}
}
