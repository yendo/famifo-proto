# 仮想スクロール 実装計画

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 4495枚のライブラリで全期間の任意の位置へ即座に移動できるよう、無限スクロールを仮想スクロール＋日付スクラバーに置き換える。

**Architecture:** ブラウザのスクロールバーが全枚数分の高さを持ち、可視範囲のタイルだけをDOMに展開する。サーバはオフセット指定でHTML断片を返し、月ごとの開始位置をJSONで別途返す。htmxは一覧から外れ、クライアントは素のJSで窓枠を管理する。

**Tech Stack:** Go 1.25 / `modernc.org/sqlite` / `html/template` + `embed` / vanilla JS（フレームワーク・ビルドステップなし）

**Spec:** `docs/superpowers/specs/2026-08-20-virtual-scroll-design.md`

## Global Constraints

- モジュールパス: `github.com/yendo/famifo-proto`
- ビルド・テストは `CGO_ENABLED=0` で通ること。`-race` は cgo を要するため `CGO_ENABLED=1 go test -race` と分けて実行する
- テストは `github.com/stretchr/testify/require`
- コメントとユーザー向け文字列は日本語
- **依存は追加しない。** `go mod tidy` は実行してよいが、5つの直接依存のバージョンを変えないこと
- `gofmt -w` を触ったファイルに必ず実行する。最終ゲートは `gofmt -l .` が空
- **並び順は `taken_at DESC, id DESC` で不変。** 既存インデックス `idx_photos_order` がそのまま効く
- **サーバが返すタイルはHTML断片のまま。** マークアップは `items.html` の1箇所に置く。JSONにするとJS側に同じマークアップが複製され、初回表示とスクロール後で食い違うバグを生む
- **月の集計はGo側でローカル時刻に変換して行う。** SQLの `strftime('%Y-%m', taken_at, 'unixepoch')` はUTCで切るため、JST 2022-11-01 00:30 の写真が "2022-10" に入る（実測確認済み）
- タイルは正方形（`aspect-ratio: 1`）である前提に依存する。写真ごとに高さが変わるレイアウトにすると全体高の事前計算が破綻する

---

### Task 1: store に ListRange を追加

任意の位置から取得できるようにする。カーソル方式は任意位置に飛べないため、仮想スクロールでは使えない。

**Files:**
- Modify: `internal/store/store.go`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Consumes: 既存の `Photo`, `scanPhoto`, `selectCols`
- Produces: `(*Store) ListRange(ctx context.Context, offset, limit int) ([]Photo, error)`

**設計メモ:** `ListPage`（カーソル方式）はまだ削除しない。web層が移行し終える Task 10 でまとめて消す。ここで消すとビルドが壊れる。

- [ ] **Step 1: 失敗するテストを書く**

`internal/store/store_test.go` の末尾に追記:

```go
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

func TestListRangeMatchesListPageOrdering(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for i, name := range []string{"a", "b", "c", "d", "e"} {
		require.NoError(t, s.Upsert(ctx, photoAt("/photos/"+name+".jpg", time.Unix(int64(1600000000+i), 0))))
	}

	byCursor, err := s.ListPage(ctx, Cursor{}, 5)
	require.NoError(t, err)
	byOffset, err := s.ListRange(ctx, 0, 5)
	require.NoError(t, err)

	require.Len(t, byOffset, 5)
	for i := range byCursor {
		require.Equal(t, byCursor[i].Path, byOffset[i].Path, "移行の前後で並び順が変わらないこと")
	}
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
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `CGO_ENABLED=0 go test ./internal/store/ -run ListRange -v`
Expected: コンパイルエラー `s.ListRange undefined`

- [ ] **Step 3: 実装する**

`internal/store/store.go` の `ListPage` の直後に追加:

```go
// ListRange は撮影日時の新しい順で offset 番目から limit 件を返す。
// 仮想スクロールは任意の位置へ飛ぶため、カーソルではなくオフセットで引く。
func (s *Store) ListRange(ctx context.Context, offset, limit int) ([]Photo, error) {
	if offset < 0 {
		return nil, fmt.Errorf("offset は0以上で指定してください: %d", offset)
	}
	if limit < 0 {
		return nil, fmt.Errorf("limit は0以上で指定してください: %d", limit)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+selectCols+` FROM photos
		 ORDER BY taken_at DESC, id DESC
		 LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("一覧を取得できません: %w", err)
	}
	defer rows.Close()

	var out []Photo
	for rows.Next() {
		p, err := scanPhoto(rows)
		if err != nil {
			return nil, fmt.Errorf("一覧を読めません: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `CGO_ENABLED=0 go test ./internal/store/ -v`
Expected: 既存テストを含め全PASS

- [ ] **Step 5: gofmt とコミット**

```bash
gofmt -w internal/store/store.go internal/store/store_test.go
git add internal/store
git commit -m "feat: add offset-based ListRange to the store"
```

---

### Task 2: store に月ごとの開始位置を追加

日付スクラバーの目盛りと吹き出しに使う。

**Files:**
- Modify: `internal/store/store.go`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Consumes: なし
- Produces:
  - `store.MonthOffset{Month string; Offset int}`
  - `(*Store) MonthOffsets(ctx context.Context) ([]MonthOffset, error)`

**設計メモ:** SQLの `strftime` は使わない。UTCで月を切るため、JST 2022-11-01 00:30 の写真が "2022-10" に分類されてしまう（実測確認済み）。`taken_at` を順に取り出し、Go 側で `time.Unix(at, 0).Format("2006-01")`（ローカル時刻）に変換して境目を拾う。4495行の走査は一瞬で終わる。

- [ ] **Step 1: 失敗するテストを書く**

```go
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
```

テストファイル冒頭の import に `"fmt"` を追加すること。

- [ ] **Step 2: テストが失敗することを確認する**

Run: `CGO_ENABLED=0 go test ./internal/store/ -run MonthOffsets -v`
Expected: コンパイルエラー `undefined: MonthOffset`

- [ ] **Step 3: 実装する**

`internal/store/store.go` に追加:

```go
// MonthOffset は、その月の写真が一覧の何番目から始まるかを表す。
type MonthOffset struct {
	Month  string // "2006-01" 形式。ローカル時刻で判定する
	Offset int    // 0 起点
}

// MonthOffsets は月の境目を新しい順に返す。日付スクラバーの目盛りに使う。
//
// SQLの strftime は UTC で月を切るため使わない。ローカルで月初の未明に
// 撮った写真が前月に分類されてしまう。Go 側で time.Local に変換して数える。
func (s *Store) MonthOffsets(ctx context.Context) ([]MonthOffset, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT taken_at FROM photos ORDER BY taken_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("撮影日時を取得できません: %w", err)
	}
	defer rows.Close()

	var out []MonthOffset
	last := ""
	i := 0
	for rows.Next() {
		var at int64
		if err := rows.Scan(&at); err != nil {
			return nil, fmt.Errorf("撮影日時を読めません: %w", err)
		}
		if m := time.Unix(at, 0).Format("2006-01"); m != last {
			out = append(out, MonthOffset{Month: m, Offset: i})
			last = m
		}
		i++
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `CGO_ENABLED=0 go test ./internal/store/ -v`
Expected: 全PASS

- [ ] **Step 5: gofmt とコミット**

```bash
gofmt -w internal/store/store.go internal/store/store_test.go
git add internal/store
git commit -m "feat: add per-month offsets for the date scrubber"
```

---

### Task 3: /items をオフセット指定に切り替える

**Files:**
- Modify: `internal/web/handlers.go`
- Modify: `internal/web/templates/items.html`
- Test: `internal/web/gallery_test.go`

**Interfaces:**
- Consumes: `(*store.Store).ListRange`
- Produces: `GET /items?offset=<n>&limit=<m>` がタイルのHTML断片を返す

**設計メモ:** センチネル（`hx-trigger` を持つ末尾要素）を `items.html` から外す。仮想スクロールでは末尾検知ではなくスクロール位置から取得するため不要。`itemsView.Next` と `cursorView` もここで使われなくなるが、`handleGallery` がまだ `buildPage` を使っているため削除は Task 10 で行う。

- [ ] **Step 1: 失敗するテストを書く**

`internal/web/gallery_test.go` の既存テスト `TestGalleryEmitsSentinelWhenMorePagesExist`、`TestGalleryOmitsSentinelOnLastPage`、`TestItemsRejectsBadCursor`、`TestItemsWithoutCursorReturnsFirstPage` を削除し、以下に置き換える:

```go
func TestItemsReturnsRequestedWindow(t *testing.T) {
	f := newWebFixture(t, 60)
	var ids []string
	for i := range 5 {
		p := f.addPhoto(t, fmt.Sprintf("p%d.jpg", i), time.Unix(int64(1600000000+i), 0), true)
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
	f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), true)

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
	p := f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), true)

	body := do(t, f.h, "/items").Body.String()

	require.Contains(t, body, p.ID)
}
```

テストファイル冒頭の import に `"fmt"` と `"net/http"` を追加すること（未追加の場合）。

- [ ] **Step 2: テストが失敗することを確認する**

Run: `CGO_ENABLED=0 go test ./internal/web/ -run Items -v`
Expected: FAIL（`/items?offset=1&limit=2` が offset を無視して先頭を返す）

- [ ] **Step 3: items.html からセンチネルを外す**

`internal/web/templates/items.html` を全置換:

```html
{{define "items"}}
{{range .Photos}}
<a class="tile" href="{{.FullURL}}" data-full="{{.FullURL}}">
  <img src="{{.ThumbURL}}" alt="" loading="lazy" decoding="async">
</a>
{{end}}
{{end}}
```

- [ ] **Step 4: ハンドラを実装する**

`internal/web/handlers.go` の `buildPage` の直後に追加:

```go
// buildRange はオフセット指定で1窓枠分を組み立てる。
func (s *Server) buildRange(r *http.Request, offset, limit int) (itemsView, error) {
	photos, err := s.st.ListRange(r.Context(), offset, limit)
	if err != nil {
		return itemsView{}, err
	}

	v := itemsView{Photos: make([]photoView, 0, len(photos))}
	for _, p := range photos {
		pv := photoView{ID: p.ID, FullURL: "/photo/" + p.ID, ThumbURL: "/photo/" + p.ID}
		if p.HasThumb {
			pv.ThumbURL = "/thumb/" + p.ID
		}
		v.Photos = append(v.Photos, pv)
	}
	return v, nil
}

// parseWindow はクエリから窓枠の範囲を読む。省略時は先頭から pageSize 件。
func parseWindow(r *http.Request, defaultLimit int) (offset, limit int, err error) {
	limit = defaultLimit
	q := r.URL.Query()

	if raw := q.Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 {
			return 0, 0, fmt.Errorf("offset が不正です: %q", raw)
		}
	}
	if raw := q.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 0 {
			return 0, 0, fmt.Errorf("limit が不正です: %q", raw)
		}
	}
	return offset, limit, nil
}
```

`handleItems` を全置換:

```go
// handleItems は仮想スクロール用のHTML断片を返す。
// 初回ページと同じテンプレートを使い、マークアップを1箇所に保つ。
func (s *Server) handleItems(w http.ResponseWriter, r *http.Request) {
	offset, limit, err := parseWindow(r, s.pageSize)
	if err != nil {
		http.Error(w, "bad range", http.StatusBadRequest)
		return
	}
	items, err := s.buildRange(r, offset, limit)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "items", items); err != nil {
		// ヘッダ送出後なのでステータスは変えられない。ログに残す。
		s.log.Error("items テンプレートの描画に失敗", "err", err)
		return
	}
}
```

`handlers.go` の import に `"fmt"` を追加すること。

- [ ] **Step 5: テストが通ることを確認する**

Run: `CGO_ENABLED=0 go test ./internal/web/ -v`
Expected: 全PASS。`TestGalleryRendersTiles` など初回ページ側のテストも通ること

- [ ] **Step 6: gofmt とコミット**

```bash
gofmt -w internal/web/handlers.go internal/web/gallery_test.go
git add internal/web
git commit -m "feat: serve gallery windows by offset instead of cursor"
```

---

### Task 4: /dates エンドポイント

**Files:**
- Modify: `internal/web/server.go`
- Modify: `internal/web/handlers.go`
- Test: `internal/web/gallery_test.go`

**Interfaces:**
- Consumes: `(*store.Store).MonthOffsets`
- Produces: `GET /dates` が `[{"m":"2022-11","o":1777}, ...]` を返す

**設計メモ:** ここだけJSONで返す。マークアップではなくデータであり、`items.html` と重複するものがないため。キー名を短くしているのは、月数が数百になったときの転送量を抑えるため。

- [ ] **Step 1: 失敗するテストを書く**

```go
func TestDatesReturnsMonthOffsets(t *testing.T) {
	f := newWebFixture(t, 60)
	f.addPhoto(t, "a.jpg", time.Date(2022, 12, 5, 10, 0, 0, 0, time.Local), true)
	f.addPhoto(t, "b.jpg", time.Date(2022, 11, 8, 10, 0, 0, 0, time.Local), true)

	rec := do(t, f.h, "/dates")

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "application/json")

	var got []struct {
		M string `json:"m"`
		O int    `json:"o"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, "2022-12", got[0].M)
	require.Equal(t, 0, got[0].O)
	require.Equal(t, "2022-11", got[1].M)
	require.Equal(t, 1, got[1].O)
}

func TestDatesOnEmptyLibraryReturnsEmptyArray(t *testing.T) {
	f := newWebFixture(t, 60)

	rec := do(t, f.h, "/dates")

	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `[]`, rec.Body.String(), "null ではなく空配列を返すこと")
}
```

テストファイルの import に `"encoding/json"` を追加すること。

- [ ] **Step 2: テストが失敗することを確認する**

Run: `CGO_ENABLED=0 go test ./internal/web/ -run Dates -v`
Expected: FAIL（404 が返る）

- [ ] **Step 3: ルートを追加する**

`internal/web/server.go` の `Handler()` に1行追加:

```go
	mux.HandleFunc("GET /dates", s.handleDates)
```

`mux.HandleFunc("GET /items", s.handleItems)` の直後に置くこと。

- [ ] **Step 4: ハンドラを実装する**

`internal/web/handlers.go` に追加:

```go
// monthView は /dates の1要素。転送量を抑えるためキー名を短くしている。
type monthView struct {
	M string `json:"m"` // "2006-01"
	O int    `json:"o"` // その月が始まるオフセット
}

// handleDates は月ごとの開始位置を返す。日付スクラバーの目盛りに使う。
// ここだけJSONなのは、マークアップではなくデータだから。
func (s *Server) handleDates(w http.ResponseWriter, r *http.Request) {
	months, err := s.st.MonthOffsets(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	out := make([]monthView, 0, len(months))
	for _, m := range months {
		out = append(out, monthView{M: m.Month, O: m.Offset})
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		s.log.Error("dates の書き出しに失敗", "err", err)
	}
}
```

`handlers.go` の import に `"encoding/json"` を追加すること。

- [ ] **Step 5: テストが通ることを確認する**

Run: `CGO_ENABLED=0 go test ./internal/web/ -v`
Expected: 全PASS

- [ ] **Step 6: gofmt とコミット**

```bash
gofmt -w internal/web
git add internal/web
git commit -m "feat: expose per-month offsets at /dates"
```

---

### Task 5: 初回ページを仮想スクロール用の構造にする

**Files:**
- Modify: `internal/web/templates/gallery.html`
- Modify: `internal/web/handlers.go`
- Test: `internal/web/gallery_test.go`

**Interfaces:**
- Consumes: `(*Server).buildRange`, `(*store.Store).Count`
- Produces: `GET /` が `#spacer` / `#window` 構造と `data-total` を含むHTMLを返す

**設計メモ:** 初回ページは**先頭1塊（120枚）を埋めた状態**で返す。空で返してJSの取得を待つと、開いた瞬間に灰色の画面が一瞬出る。クライアントは塊番号0を取得済みとしてキャッシュに載せて開始する。htmx の `<script>` タグはここで外す。

- [ ] **Step 1: 失敗するテストを書く**

`TestGalleryRendersTiles` を残したまま、以下を追加:

```go
func TestGalleryEmbedsTotalAndFirstChunk(t *testing.T) {
	f := newWebFixture(t, 60)
	for i := range 3 {
		f.addPhoto(t, fmt.Sprintf("p%d.jpg", i), time.Unix(int64(1600000000+i), 0), true)
	}

	body := do(t, f.h, "/").Body.String()

	require.Contains(t, body, `data-total="3"`)
	require.Contains(t, body, `id="spacer"`)
	require.Contains(t, body, `id="window"`)
	require.Equal(t, 3, strings.Count(body, `class="tile"`), "先頭の塊を埋めて返すこと")
}

func TestGalleryDropsHtmx(t *testing.T) {
	f := newWebFixture(t, 60)
	f.addPhoto(t, "a.jpg", time.Unix(1600000000, 0), true)

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
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `CGO_ENABLED=0 go test ./internal/web/ -run Gallery -v`
Expected: FAIL（`data-total` も `#spacer` も無い）

- [ ] **Step 3: gallery.html を全置換する**

```html
{{define "gallery"}}<!doctype html>
<html lang="ja">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<title>famifo</title>
<link rel="stylesheet" href="/static/app.css">
<script src="/static/app.js" defer></script>
</head>
<body>
<header class="topbar">
  <h1>famifo</h1>
  <span class="count">{{.Total}} 枚</span>
</header>

<main id="gallery" class="gallery" data-total="{{.Total}}" data-chunk="{{.ChunkSize}}">
  <div id="spacer">
    <div id="window" class="grid">
{{template "items" .}}
    </div>
  </div>
</main>

<div id="scrubber" class="scrubber" hidden>
  <div class="scrub-thumb"></div>
  <div class="scrub-label" hidden></div>
</div>

<div id="lightbox" class="lightbox" hidden>
  <img alt="">
  <button class="lb-nav lb-prev" type="button" aria-label="前の写真">‹</button>
  <button class="lb-nav lb-next" type="button" aria-label="次の写真">›</button>
</div>
</body>
</html>
{{end}}
```

- [ ] **Step 4: handleGallery を書き換える**

`internal/web/handlers.go` の `galleryView` と `handleGallery` を置換:

```go
// galleryView は gallery.html の入力。itemsViewを埋め込むので
// {{template "items" .}} にそのまま渡せる。
type galleryView struct {
	itemsView
	Total     int
	ChunkSize int
}

// handleGallery はギャラリーのトップページを返す。
// 先頭の塊を埋めた状態で返すので、開いた直後に灰色の画面が出ない。
func (s *Server) handleGallery(w http.ResponseWriter, r *http.Request) {
	items, err := s.buildRange(r, 0, s.pageSize)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	total, err := s.st.Count(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	view := galleryView{itemsView: items, Total: total, ChunkSize: s.pageSize}
	if err := s.tmpl.ExecuteTemplate(w, "gallery", view); err != nil {
		// ヘッダ送出後なのでステータスは変えられない。ログに残す。
		s.log.Error("gallery テンプレートの描画に失敗", "err", err)
		return
	}
}
```

- [ ] **Step 5: テストが通ることを確認する**

Run: `CGO_ENABLED=0 go test ./internal/web/ -v`
Expected: 全PASS

テストファイルの import に `"strings"` を追加すること。

- [ ] **Step 6: gofmt とコミット**

```bash
gofmt -w internal/web
git add internal/web
git commit -m "feat: render the gallery shell for virtual scrolling"
```

---

### Task 6: CSS を仮想スクロール構造に合わせる

**Files:**
- Modify: `internal/web/static/app.css`
- Test: `internal/web/static_test.go`

**Interfaces:**
- Consumes: Task 5 の `#spacer` / `#window` / `#scrubber`
- Produces: グリッドが `#window` 側に移り、スクラバーの見た目が定義される

**設計メモ:** グリッドは `.gallery` から `#window` へ移す。`#gallery` はスクロール領域、`#spacer` は高さだけを持つ箱、`#window` が実際に並べる場所。列数の算出はJSが `getComputedStyle(#window).gridTemplateColumns` から読むので、`repeat(auto-fill, minmax(...))` の指定はそのまま維持する。

- [ ] **Step 1: 失敗するテストを書く**

`internal/web/static_test.go` の `TestAppCSSIsResponsive` を置換:

```go
func TestAppCSSIsResponsive(t *testing.T) {
	f := newWebFixture(t, 10)

	body := do(t, f.h, "/static/app.css").Body.String()

	require.Contains(t, body, "@media", "画面幅に応じて列数を変える")
	require.Contains(t, body, "grid-template-columns")
	require.Contains(t, body, "#window", "グリッドは #window 側に定義する")
	require.Contains(t, body, ".scrubber", "日付スクラバーの見た目を定義する")
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `CGO_ENABLED=0 go test ./internal/web/ -run AppCSS -v`
Expected: FAIL（`#window` も `.scrubber` も無い）

- [ ] **Step 3: app.css の該当部分を置換する**

`.gallery` のグリッド定義（`.gallery { display: grid; ... }` と2つの `@media` ブロック）を、以下に置き換える:

```css
/* スクロール領域。高さは #spacer が決める */
.gallery {
  padding: var(--gap);
}

/* 全枚数分の高さを持つ箱。ブラウザのスクロールバーはこれを見る */
#spacer {
  position: relative;
}

/* 可視範囲のタイルだけを並べる。位置は JS が translateY で動かす */
#window {
  position: absolute;
  left: 0;
  right: 0;
  top: 0;
  display: grid;
  gap: var(--gap);
  grid-template-columns: repeat(auto-fill, minmax(110px, 1fr));
}
@media (min-width: 700px) {
  #window { grid-template-columns: repeat(auto-fill, minmax(160px, 1fr)); }
}
@media (min-width: 1100px) {
  #window { grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); }
}
```

`.sentinel` のルールを削除し、末尾に以下を追加:

```css
/* 日付スクラバー。スクロール中だけ現れる */
.scrubber {
  position: fixed;
  top: 56px;
  right: 0;
  bottom: 0;
  width: 32px;
  z-index: 6;
  touch-action: none;
  opacity: 0;
  transition: opacity 0.2s;
}
.scrubber[hidden] { display: none; }
.scrubber.visible { opacity: 1; }

.scrub-thumb {
  position: absolute;
  right: 4px;
  width: 24px;
  height: 40px;
  border-radius: 12px;
  background: rgba(255, 255, 255, 0.35);
}

.scrub-label {
  position: absolute;
  right: 36px;
  padding: 4px 10px;
  border-radius: 4px;
  background: rgba(0, 0, 0, 0.85);
  color: var(--fg);
  font-size: 0.85rem;
  white-space: nowrap;
}
.scrub-label[hidden] { display: none; }
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `CGO_ENABLED=0 go test ./internal/web/ -v`
Expected: 全PASS

- [ ] **Step 5: コミット**

```bash
git add internal/web/static/app.css internal/web/static_test.go
git commit -m "feat: move the grid onto the virtual window and style the scrubber"
```

---

### Task 7: 仮想スクロールの本体

**Files:**
- Modify: `internal/web/static/app.js`
- Test: `internal/web/static_test.go`

**Interfaces:**
- Consumes: `#gallery[data-total][data-chunk]`, `#spacer`, `#window`, `GET /items?offset=&limit=`
- Produces: モジュールスコープの定数 `famifo` を公開する。Task 8・9 が使う
  - `famifo.total` (number) / `famifo.chunkSize` (number)
  - `famifo.urlAt(i)` → `Promise<string|null>` 全体の通し番号から写真URLを引く
  - `famifo.ensureChunk(i)` → `void` その番号を含む塊を先読みする
  - `famifo.pastedIndex()` → `number` いま `#window` に貼ってある先頭の通し番号
  - `famifo.gallery` (Element) / `famifo.render()` → `void`
  - 写真が0枚のときなど土台が無い場合は `null` になる。利用側は必ず判定すること

**設計メモ:**

列数はJSで `auto-fill` の計算を再現せず、`getComputedStyle(#window).gridTemplateColumns` が返す実際のトラック列を数える。ブラウザが決めた値をそのまま使うので、CSSの breakpoint を変えてもJSを直す必要がない。

塊を受け取ったら `data-full` を抜き出して配列に控える。ライトボックス（Task 8）が「全体で何番目の写真のURLか」を引くために必要で、これがないと窓枠の外へスワイプできない。

- [ ] **Step 1: 失敗するテストを書く**

`internal/web/static_test.go` の `TestAppJSWiresLightbox` を置換:

```go
func TestAppJSImplementsVirtualScroll(t *testing.T) {
	f := newWebFixture(t, 10)

	body := do(t, f.h, "/static/app.js").Body.String()

	require.Contains(t, body, "#spacer")
	require.Contains(t, body, "#window")
	require.Contains(t, body, "gridTemplateColumns", "列数はブラウザの計算結果から読む")
	require.Contains(t, body, "/items?offset=")
	require.Contains(t, body, "data-full", "塊からURLを控えてライトボックスに渡す")
	require.Contains(t, body, "famifo", "他のスクリプトから使える形で公開する")
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `CGO_ENABLED=0 go test ./internal/web/ -run AppJS -v`
Expected: FAIL

- [ ] **Step 3: app.js の先頭に仮想スクロールを実装する**

`app.js` の既存のIIFE（ライトボックス）の**前**に、以下を挿入する。ライトボックス側は Task 8 で書き換えるので、この時点では触らない。

```js
// 仮想スクロール。全枚数分の高さを #spacer が持ち、可視範囲だけを
// #window に展開する。無限スクロールと違い、任意の位置へ即座に飛べる。
const famifo = (() => {
  const gallery = document.querySelector('#gallery');
  const spacer = document.querySelector('#spacer');
  const win = document.querySelector('#window');
  if (!gallery || !spacer || !win) return null;

  const total = Number(gallery.dataset.total || 0);
  const chunkSize = Number(gallery.dataset.chunk || 60);
  const OVERSCAN_ROWS = 4; // 可視範囲の上下に余分に描く行数

  // 塊番号 -> { html, urls }
  const chunks = new Map();
  const inFlight = new Map();

  let cols = 1;
  let rowH = 0;
  let pastedFrom = 0; // いま貼り付けてある先頭が全体で何番目か
  let renderedKey = ''; // 「どの塊を何個貼ったか」。同じなら描き直さない

  // 初回ページはサーバが先頭の塊を埋めて返しているので、取得済みとして控える。
  function seedFirstChunk() {
    const urls = [...win.querySelectorAll('[data-full]')].map((a) => a.dataset.full);
    if (urls.length > 0) {
      chunks.set(0, { html: win.innerHTML, urls });
    }
  }

  // 列数とタイル高をブラウザの計算結果から読む。
  // auto-fill の計算を自前で再現すると CSS の breakpoint と二重管理になる。
  function measure() {
    const cs = getComputedStyle(win);
    const tracks = cs.gridTemplateColumns.split(' ').filter((t) => t.length > 0);
    cols = Math.max(1, tracks.length);
    const tileW = parseFloat(tracks[0]) || 0;
    const gap = parseFloat(cs.rowGap) || 0;
    rowH = tileW + gap; // タイルは正方形なので幅がそのまま高さになる
    const rows = Math.ceil(total / cols);
    spacer.style.height = `${Math.max(0, rows * rowH)}px`;
  }

  async function fetchChunk(ci) {
    if (chunks.has(ci)) return chunks.get(ci);
    if (inFlight.has(ci)) return inFlight.get(ci);

    const job = (async () => {
      const res = await fetch(`/items?offset=${ci * chunkSize}&limit=${chunkSize}`);
      if (!res.ok) throw new Error(`items ${res.status}`);
      const html = await res.text();
      const tmp = document.createElement('div');
      tmp.innerHTML = html;
      const urls = [...tmp.querySelectorAll('[data-full]')].map((a) => a.dataset.full);
      const entry = { html, urls };
      chunks.set(ci, entry);
      return entry;
    })().finally(() => inFlight.delete(ci));

    inFlight.set(ci, job);
    return job;
  }

  // 全体の通し番号から写真のURLを引く。未取得なら取りに行く。
  async function urlAt(i) {
    if (i < 0 || i >= total) return null;
    const entry = await fetchChunk(Math.floor(i / chunkSize));
    return entry.urls[i % chunkSize] ?? null;
  }

  function ensureChunk(i) {
    if (i < 0 || i >= total) return;
    fetchChunk(Math.floor(i / chunkSize)).catch(() => {});
  }

  function render() {
    if (rowH <= 0 || total === 0) return;

    const viewRows = Math.ceil(gallery.clientHeight / rowH);
    const firstRow = Math.max(0, Math.floor(gallery.scrollTop / rowH) - OVERSCAN_ROWS);
    const lastRow = Math.min(Math.ceil(total / cols) - 1, firstRow + viewRows + OVERSCAN_ROWS * 2);

    const from = firstRow * cols;
    const to = Math.min(total, (lastRow + 1) * cols);
    const firstChunk = Math.floor(from / chunkSize);
    const lastChunk = Math.floor((to - 1) / chunkSize);

    // 足りない塊は取りに行く。届いたら描き直す。
    for (let ci = firstChunk; ci <= lastChunk; ci++) {
      if (!chunks.has(ci)) {
        fetchChunk(ci).then(render).catch(() => {});
      }
    }

    // 先頭の塊が無いと貼り付け位置を決められないので、届くまで灰色のまま待つ
    if (!chunks.has(firstChunk)) return;

    // 塊は先頭から連続している分だけ貼る。途中が欠けたまま先の塊を貼ると
    // 位置がずれて、写真が実際とは違う場所に並んでしまう。
    const parts = [];
    for (let ci = firstChunk; ci <= lastChunk; ci++) {
      const entry = chunks.get(ci);
      if (!entry) break;
      parts.push(entry.html);
    }

    // 貼る内容が前回と同じなら触らない。スクロールのたびに
    // innerHTML を書き換えると画像の再読み込みが起きる。
    const key = `${firstChunk}:${parts.length}`;
    if (key === renderedKey) return;
    renderedKey = key;

    pastedFrom = firstChunk * chunkSize;
    win.innerHTML = parts.join('');
    win.style.transform = `translateY(${Math.floor(pastedFrom / cols) * rowH}px)`;
  }

  function onResize() {
    renderedKey = ''; // 列数が変われば貼り直しが必要
    measure();
    render();
  }

  seedFirstChunk();
  measure();
  gallery.addEventListener('scroll', render, { passive: true });
  window.addEventListener('resize', onResize);

  return {
    total,
    chunkSize,
    urlAt,
    ensureChunk,
    gallery,
    render,
    pastedIndex: () => pastedFrom, // 貼り付け先頭の通し番号。Task 8 が使う
  };
})();
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `CGO_ENABLED=0 go test ./internal/web/ -v`
Expected: 全PASS

- [ ] **Step 5: コミット**

```bash
git add internal/web/static/app.js internal/web/static_test.go
git commit -m "feat: render the gallery with a virtual scroll window"
```

---

### Task 8: ライトボックスを通し番号ベースに作り直す

**Files:**
- Modify: `internal/web/static/app.js`
- Test: `internal/web/static_test.go`

**Interfaces:**
- Consumes: `famifo.urlAt(i)`, `famifo.total`, `famifo.ensureChunk(i)`, `famifo.pastedIndex()`
- Produces: なし（ブラウザ側のみ）

**設計メモ:** 現行のライトボックスはDOM上の `.tile` を集めて動くため、仮想スクロールでは**窓枠の端でスワイプが止まる**。DOMではなく全体の通し番号で動かす。

クリックされたタイルの通し番号は、`#window` 内で何番目かを数えて `famifo.pastedIndex()` を足して求める。Task 7 が塊単位で貼り付けているので、この足し算が成立する。

- [ ] **Step 1: 失敗するテストを書く**

```go
func TestAppJSLightboxUsesGlobalIndex(t *testing.T) {
	f := newWebFixture(t, 10)

	body := do(t, f.h, "/static/app.js").Body.String()

	require.Contains(t, body, "#lightbox")
	require.Contains(t, body, "urlAt", "DOMではなく通し番号でURLを引く")
	require.Contains(t, body, "touchstart", "スワイプ操作を実装している")
	require.NotContains(t, body, "tiles().indexOf",
		"DOM上のタイル一覧に依存する実装は残さない")
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `CGO_ENABLED=0 go test ./internal/web/ -run Lightbox -v`
Expected: FAIL（`tiles().indexOf` が残っている）

- [ ] **Step 3: ライトボックスのIIFEを全置換する**

`app.js` の既存のライトボックスIIFE（`(() => { const box = document.querySelector('#lightbox'); ... })();`）を、以下で置き換える:

```js
// ライトボックス。仮想スクロールではDOM上に可視範囲のタイルしか無いため、
// 全体の通し番号で動かす。そうしないと窓枠の端でスワイプが止まる。
(() => {
  const box = document.querySelector('#lightbox');
  if (!box || !famifo) return;

  const img = box.querySelector('img');
  const SWIPE_X = 50; // 左右送りとみなす最小移動量(px)
  const SWIPE_Y = 80; // 下スワイプで閉じる最小移動量(px)

  let idx = -1;

  async function show(i) {
    if (i < 0 || i >= famifo.total) return;
    const url = await famifo.urlAt(i);
    if (!url) return;
    idx = i;
    img.src = url;
    famifo.ensureChunk(i + 1); // 次を先読みしておく
    famifo.ensureChunk(i - 1);
  }

  async function open(i) {
    await show(i);
    if (idx < 0) return;
    box.hidden = false;
    document.body.classList.add('locked');
  }

  function close() {
    box.hidden = true;
    img.removeAttribute('src');
    document.body.classList.remove('locked');
  }

  document.addEventListener('click', (e) => {
    const tile = e.target.closest('#window .tile');
    if (!tile) return;
    e.preventDefault();
    // 窓枠内で何番目か + 貼り付けの先頭 = 全体の通し番号
    const within = [...tile.parentElement.querySelectorAll('.tile')].indexOf(tile);
    open(famifo.pastedIndex() + within);
  });

  box.addEventListener('click', (e) => {
    if (e.target.closest('.lb-prev')) { show(idx - 1); return; }
    if (e.target.closest('.lb-next')) { show(idx + 1); return; }
    close();
  });

  document.addEventListener('keydown', (e) => {
    if (box.hidden) return;
    if (e.key === 'Escape') close();
    else if (e.key === 'ArrowRight') show(idx + 1);
    else if (e.key === 'ArrowLeft') show(idx - 1);
  });

  let startX = 0;
  let startY = 0;
  let tracking = false;

  box.addEventListener('touchstart', (e) => {
    // 2本指はピンチズーム。ブラウザに任せる
    tracking = e.touches.length === 1;
    if (!tracking) return;
    startX = e.touches[0].clientX;
    startY = e.touches[0].clientY;
  }, { passive: true });

  box.addEventListener('touchend', (e) => {
    if (!tracking) return;
    tracking = false;
    const t = e.changedTouches[0];
    const dx = t.clientX - startX;
    const dy = t.clientY - startY;

    if (Math.abs(dx) > SWIPE_X && Math.abs(dx) > Math.abs(dy)) {
      show(dx < 0 ? idx + 1 : idx - 1);
    } else if (dy > SWIPE_Y && Math.abs(dy) > Math.abs(dx)) {
      close();
    }
  }, { passive: true });
})();
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `CGO_ENABLED=0 go test ./internal/web/ -v`
Expected: 全PASS

- [ ] **Step 5: コミット**

```bash
git add internal/web/static/app.js internal/web/static_test.go
git commit -m "feat: navigate the lightbox by global photo index"
```

---

### Task 9: 日付スクラバー

**Files:**
- Modify: `internal/web/static/app.js`
- Test: `internal/web/static_test.go`

**Interfaces:**
- Consumes: `GET /dates`, `famifo.total`, `famifo.gallery`
- Produces: なし（ブラウザ側のみ）

**設計メモ:** ドラッグ位置の割合 → 写真オフセット → スクロール位置、と変換する。吹き出しの年月は `/dates` を二分探索して求める。`/dates` の取得に失敗してもスクラバー自体は動く（ラベルが出ないだけ）。

- [ ] **Step 1: 失敗するテストを書く**

```go
func TestAppJSImplementsScrubber(t *testing.T) {
	f := newWebFixture(t, 10)

	body := do(t, f.h, "/static/app.js").Body.String()

	require.Contains(t, body, "#scrubber")
	require.Contains(t, body, "/dates")
	require.Contains(t, body, "scrub-label", "ドラッグ中に年月を表示する")
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `CGO_ENABLED=0 go test ./internal/web/ -run Scrubber -v`
Expected: FAIL

- [ ] **Step 3: app.js の末尾にスクラバーを実装する**

```js
// 日付スクラバー。ドラッグで全期間の任意の位置へ飛ぶ。
(() => {
  const bar = document.querySelector('#scrubber');
  if (!bar || !famifo || famifo.total === 0) return;

  const thumb = bar.querySelector('.scrub-thumb');
  const label = bar.querySelector('.scrub-label');
  const gallery = famifo.gallery;

  let months = []; // [{m:"2022-11", o:1777}, ...] 新しい順
  let dragging = false;
  let hideTimer = 0;

  fetch('/dates')
    .then((r) => (r.ok ? r.json() : []))
    .then((data) => { months = data; })
    .catch(() => { months = []; }); // ラベルが出ないだけでドラッグは効く

  bar.hidden = false;

  // オフセットが属する月を二分探索で求める
  function monthAt(offset) {
    if (months.length === 0) return '';
    let lo = 0;
    let hi = months.length - 1;
    let found = months[0];
    while (lo <= hi) {
      const mid = (lo + hi) >> 1;
      if (months[mid].o <= offset) { found = months[mid]; lo = mid + 1; } else { hi = mid - 1; }
    }
    const [y, m] = found.m.split('-');
    return `${y}年${Number(m)}月`;
  }

  function maxScroll() {
    return Math.max(1, gallery.scrollHeight - gallery.clientHeight);
  }

  function show() {
    bar.classList.add('visible');
    clearTimeout(hideTimer);
    if (!dragging) {
      hideTimer = setTimeout(() => bar.classList.remove('visible'), 1500);
    }
  }

  // スクロール位置からつまみの位置を更新する
  function sync() {
    const frac = gallery.scrollTop / maxScroll();
    const top = frac * (bar.clientHeight - thumb.offsetHeight);
    thumb.style.top = `${top}px`;
    show();
  }

  function seek(clientY) {
    const rect = bar.getBoundingClientRect();
    const frac = Math.min(1, Math.max(0, (clientY - rect.top) / rect.height));
    gallery.scrollTop = frac * maxScroll();

    const offset = Math.floor(frac * famifo.total);
    const text = monthAt(offset);
    if (text) {
      label.textContent = text;
      label.hidden = false;
      label.style.top = `${Math.min(rect.height - 24, Math.max(0, clientY - rect.top - 12))}px`;
    }
  }

  function startDrag(clientY) {
    dragging = true;
    bar.classList.add('visible');
    clearTimeout(hideTimer);
    seek(clientY);
  }

  function endDrag() {
    dragging = false;
    label.hidden = true;
    show();
  }

  bar.addEventListener('pointerdown', (e) => {
    bar.setPointerCapture(e.pointerId);
    startDrag(e.clientY);
  });
  bar.addEventListener('pointermove', (e) => { if (dragging) seek(e.clientY); });
  bar.addEventListener('pointerup', endDrag);
  bar.addEventListener('pointercancel', endDrag);

  gallery.addEventListener('scroll', sync, { passive: true });
  sync();
})();
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `CGO_ENABLED=0 go test ./internal/web/ -v`
Expected: 全PASS

- [ ] **Step 5: コミット**

```bash
git add internal/web/static/app.js internal/web/static_test.go
git commit -m "feat: add a draggable date scrubber"
```

---

### Task 10: カーソル方式と htmx の撤去

**Files:**
- Modify: `internal/store/store.go`（`ListPage`, `Cursor` を削除）
- Modify: `internal/store/store_test.go`（該当テストを削除）
- Modify: `internal/web/handlers.go`（`buildPage`, `parseCursor`, `cursorView`, `itemsView.Next` を削除）
- Delete: `internal/web/static/htmx.min.js`
- Modify: `docs/design.md`

**Interfaces:**
- Consumes: なし
- Produces: なし（撤去のみ）

**設計メモ:** ここまでで利用者がいなくなったものをまとめて消す。**先に `grep` で参照が残っていないことを確認してから消すこと。** `//go:embed templates static` は `static/` に `app.css` と `app.js` が残るので、htmx を消してもビルドは通る。

- [ ] **Step 1: 参照が残っていないことを確認する**

```bash
grep -rn "ListPage\|store.Cursor\|parseCursor\|cursorView\|buildPage\|htmx" \
  --include='*.go' --include='*.html' internal/ main.go
```

Expected: `store.go` / `store_test.go` / `handlers.go` の定義本体とテストだけがヒットし、**新しい呼び出し元が無いこと**。想定外の参照があれば、消さずに報告する。

- [ ] **Step 2: store から削除する**

`internal/store/store.go` から `Cursor` 型と `ListPage` メソッドを削除する。
`internal/store/store_test.go` から `TestListPagePaginatesNewestFirst`、`TestListPageBreaksTiesByID`、`TestListRangeMatchesListPageOrdering` を削除する。

`TestListRangeMatchesListPageOrdering` は移行の正しさを確認するための一時的なテストで、移行元が消える以上役目を終える。代わりに以下を追加して、並び順そのものは引き続き固定する:

```go
func TestListRangeOrdersNewestFirstWithIDTiebreak(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	same := time.Unix(1600000000, 0)
	for _, name := range []string{"a", "b", "c"} {
		require.NoError(t, s.Upsert(ctx, photoAt("/photos/"+name+".jpg", same)))
	}

	// 撮影日時が同じでも、1件ずつ辿って重複も欠落も起きないこと
	var seen []string
	for i := range 3 {
		page, err := s.ListRange(ctx, i, 1)
		require.NoError(t, err)
		require.Len(t, page, 1)
		seen = append(seen, page[0].Path)
	}

	require.ElementsMatch(t,
		[]string{"/photos/a.jpg", "/photos/b.jpg", "/photos/c.jpg"}, seen)
}
```

- [ ] **Step 3: web から削除する**

`internal/web/handlers.go` から `cursorView` 型、`itemsView` の `Next` フィールド、`buildPage`、`parseCursor` を削除する。`"time"` の import が未使用になれば外す。

```bash
rm internal/web/static/htmx.min.js
```

- [ ] **Step 4: ビルドとテストが通ることを確認する**

Run: `CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go test ./... && go vet ./...`
Expected: 全7パッケージPASS、vet 警告なし

- [ ] **Step 5: docs/design.md を更新する**

「表示」節の「HTMLフラグメントパターン + htmx」の記述を、以下に差し替える:

```markdown
- **バックエンド/フロントエンド境界**: HTMLフラグメントパターン
  - サーバが一覧をHTML断片として返し、クライアントがDOMに差し込む
  - マークアップの管理場所を Go テンプレート側に一元化し、二重実装を避ける
  - 当初は htmx で無限スクロールを実現していたが、仮想スクロールへの移行に伴い
    htmx は撤去した（経緯は docs/superpowers/specs/2026-08-20-virtual-scroll-design.md）
```

「一覧構成」の項に1行追加:

```markdown
  - 全期間を移動できるよう、仮想スクロールと日付スクラバーを備える
```

- [ ] **Step 6: gofmt とコミット**

```bash
gofmt -w internal/store internal/web
git add -A
git commit -m "refactor: drop cursor pagination and htmx"
```

---

### Task 11: 実機での確認

**Files:**
- Modify: `README.md`

**Interfaces:**
- Consumes: すべて
- Produces: 動作確認の記録

**設計メモ:** ここまでのテストはサーバ側しか検証できていない。仮想スクロールの本体（行の計算、キャッシュ、スワイプ、スクラバー）はブラウザが無いと確認できず、**この手順が唯一の検証手段**である。

- [ ] **Step 1: ビルドして実際の写真ライブラリで起動する**

```bash
CGO_ENABLED=0 go build -o famifo-proto .
./famifo-proto -dir ~/Pictures -data /tmp/famifo-check -addr 127.0.0.1:8080
```

初回はサムネイル生成に時間がかかる。スキャン完了のログを待つこと。

- [ ] **Step 2: サーバ側の応答を確認する**

```bash
curl -s 'localhost:8080/' | grep -o 'data-total="[0-9]*"'
curl -s 'localhost:8080/items?offset=1740&limit=10' | grep -c 'class="tile"'
curl -s 'localhost:8080/dates' | head -c 200
```

Expected: `data-total` が実際の枚数、`/items` が10件、`/dates` が月ごとの開始位置のJSON

- [ ] **Step 3: ブラウザで以下を目視確認する**

1. スクロールバーが全期間を表していること（読み込み済みの分だけではない）
2. 右端のスクラバーをドラッグして2022年へ移動できること
3. ドラッグ中に年月の吹き出しが追従すること
4. 移動先で写真が正しく並び、重複や欠落がないこと
5. ライトボックスを開き、**窓枠の境界を越えて**左右スワイプできること（20枚以上連続で送る）
6. ブラウザの幅を変えても位置とレイアウトが保たれること
7. コンソールにエラーが出ていないこと

- [ ] **Step 4: スマホ実機で確認する**

同一LANの端末から `http://<PCのIP>:8080` を開き、以下を確認する。

1. スクラバーがスクロール中に現れ、停止後に消えること
2. ドラッグで一気に移動できること
3. タップで拡大、左右スワイプで送り、下スワイプで閉じられること
4. 画面を回転させてもレイアウトが崩れないこと

- [ ] **Step 5: README に操作を追記する**

「使い方」節の末尾に追加:

```markdown
## 一覧の操作

- スクロールバーは全期間を表す。任意の位置へ直接移動できる
- 画面右端のスクラバーをドラッグすると、年月を見ながら一気に移動できる
- タイルをタップすると拡大表示。左右スワイプで送り、下スワイプで閉じる
```

- [ ] **Step 6: コミット**

```bash
git add README.md
git commit -m "docs: describe the gallery navigation"
```

---

## 実装後の確認

```bash
CGO_ENABLED=0 go build ./...
CGO_ENABLED=0 go test ./...
CGO_ENABLED=1 go test ./internal/index/ -race -count=1
go vet ./...
gofmt -l .          # 出力が空であること
```

## 仕様との対応

| 設計文書の項目 | 実装箇所 |
|---|---|
| オフセット取得 | Task 1 `ListRange` |
| 月ごとの開始位置（ローカル時刻） | Task 2 `MonthOffsets` |
| `/items?offset=&limit=` | Task 3 |
| `/dates` | Task 4 |
| 初回ページに総数と先頭の塊 | Task 5 |
| `#spacer` / `#window` 構造 | Task 5, 6 |
| 列数はブラウザの計算結果から読む | Task 7 `measure()` |
| 塊単位のキャッシュと先読み | Task 7 |
| 塊から `data-full` を控える | Task 7 `fetchChunk` |
| ライトボックスを通し番号で動かす | Task 8 |
| 日付スクラバー | Task 9 |
| htmx の撤去 | Task 10 |
| カーソル方式の削除 | Task 10 |
| 実機確認 | Task 11 |
