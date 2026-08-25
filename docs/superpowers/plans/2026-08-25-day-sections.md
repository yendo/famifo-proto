# 日ごとの区切りを入れた一覧 実装計画

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 写真一覧を日ごとに区切り、1行に収まる日は横に並べる。

**Architecture:** 仮想スクロールの「通し番号 → 位置」を、一本の式（`floor(i / cols) * rowH`）から、日ごとの累積和と二分探索に置き換える。占める列数は `min(枚数, 列数)` の一本の規則で決まり、これが CSS Grid の自動配置の規則とそのまま一致するため、JSの計算とブラウザの実際の配置が定義上ずれない。どの日が横に並ぶかは列数で決まるためサーバーはレイアウト済みHTMLを返せず、カードと日付ラベルの markup はクライアント側が持つ。

**Tech Stack:** Go 1.x（標準ライブラリ + `modernc.org/sqlite` + `stretchr/testify`）、vanilla JS、CSS Grid、chromedp（ブラウザテスト）。**新しい依存は一切追加しない。**

**Spec:** `docs/superpowers/specs/2026-08-25-day-sections-design.md`

## Global Constraints

- 新しい依存を追加しない。Node も含む。`CGO_ENABLED=0 go build` が通り続けること
- 日付の切り出しは必ず Go 側で `time.Local` に変換して行う。SQLの `strftime` は UTC で切るため使わない
- ラベルの高さの定義は `app.css` の `--label-h` の1箇所だけ。JSは `getComputedStyle` で読む
- 列幅・列数の定義は `app.css` の `grid-template-columns` の1箇所だけ。JSは `getComputedStyle` で読む
- タイルは正方形（`aspect-ratio: 1`）である前提を維持する。タイル高 = 列幅
- ブラウザテストは `//go:build browser` の下に置く。`go test ./...` には含めない
- コミットメッセージは日本語の本文 + 英語の件名（既存の履歴に合わせる）

## famifo の公開インターフェース（タスク間の契約）

`app.js` の `famifo` オブジェクトが公開するもの。タスク4以降が依存するので、名前と型はここで確定させる。

```js
famifo.total          // number  総枚数（変更なし）
famifo.chunkSize      // number  塊のサイズ（変更なし）
famifo.urlAt(i)       // Promise<string|null>（変更なし）
famifo.ensureChunk(i) // void（変更なし）
famifo.scroller       // Element（変更なし）
famifo.maxScroll()    // number（変更なし）
famifo.render()       // void（変更なし）

// 削除する
famifo.pastedIndex()  // → pastedRange() に置き換え（タスク10）

// 追加する
famifo.pastedRange()  // {from, to}  いま貼り付けてある通し番号の範囲（to は含まない）
famifo.current()      // いまのレイアウト（下記 L）。未測定なら null

// 以下4つは純粋関数。モジュールの状態を読まないので、ブラウザテストから
// 直接叩いて検証できる。L は famifo.layout の戻り値。
famifo.layout(groups, cols, tileH, labelH, gap)  // → L
famifo.yForIndex(L, i)          // number  通し番号 i の写真が属する段の上端
famifo.dayAtY(L, y)             // string  y の位置にある日（"2006-01-02"）。空なら ""
famifo.visibleWindow(L, top, bottom)  // {pieces, pasteY, from, to} または null
```

`famifo.layout` の戻り値 L:

```js
{
  height,    // number  全体の高さ
  cols,      // number  引数をそのまま持ち回る（lookup関数が使う）
  tileH,     // number
  labelH,    // number
  gap,       // number
  entries: [ // 日ごとに1件。新しい順
    { d,      // string "2006-01-02"
      y,      // number このカードが属するストライプの上端
      h,      // number このカードの高さ
      start,  // number その日の先頭写真の通し番号
      n,      // number 枚数
      span,   // number 占める列数 = min(n, cols)
      col,    // number ストライプの何列目から始まるか（0起点）
      rows }  // number = ceil(n / span)
  ]
}
```

## File Structure

| ファイル | 変更 | 責務 |
|---|---|---|
| `internal/store/store.go` | 変更 | `MonthOffsets` を `DayGroups` に置き換え |
| `internal/store/store_test.go` | 変更 | 同上のテスト |
| `internal/web/handlers.go` | 変更 | `/dates` 削除、`galleryView` に日ごとの表を追加、`photoView` に日付を追加 |
| `internal/web/server.go` | 変更 | `/dates` のルート削除 |
| `internal/web/templates/items.html` | 変更 | タイルに `data-date` |
| `internal/web/templates/gallery.html` | 変更 | 日ごとの表を JSON で埋め込む |
| `internal/web/static/app.js` | 変更 | レイアウト計算・カード組み立て・既存機能の追従 |
| `internal/web/static/app.css` | 変更 | `--label-h`・`.daycard`・`.daylabel` |
| `internal/web/gallery_test.go` | 変更 | `/dates` のテストを埋め込みのテストに置き換え |
| `internal/web/static_test.go` | 変更 | `/dates` 参照チェックの更新 |
| `internal/web/browser_test.go` | 変更 | corpus 作り直し、位置計算の差し替え、テスト追加・削除 |
| `docs/design.md` | 変更 | HTMLフラグメントパターンの決定を更新 |

`app.js` は355行から500行程度に増える見込み。仮想スクロール・ライトボックス・スクラバーの3つのIIFEという既存の構成は維持する。レイアウト計算は仮想スクロールのIIFE内に置き、`famifo.layout` として公開する。

---

### Task 1: store.DayGroups

**Files:**
- Modify: `internal/store/store.go:236-268`（`MonthOffset` / `MonthOffsets`）
- Test: `internal/store/store_test.go:218-270`

**Interfaces:**
- Consumes: なし
- Produces: `store.DayGroup{Date string, Count int}`、`(*Store).DayGroups(ctx) ([]DayGroup, error)`

- [ ] **Step 1: 失敗するテストを書く**

`internal/store/store_test.go` の `TestMonthOffsetsMarksMonthBoundaries` / `TestMonthOffsetsUsesLocalTime` / `TestMonthOffsetsEmptyStore` を、次の4本に置き換える。

```go
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
	require.Equal(t, []DayGroup{
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
			require.Equal(t, g.Date, all[offset].TakenAt.Format("2006-01-02"),
				"offset=%d の写真は %s のはず", offset, g.Date)
			offset++
		}
	}
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/store/ -run TestDayGroups -v`
Expected: FAIL（`undefined: DayGroup`, `s.DayGroups undefined` でコンパイルエラー）

- [ ] **Step 3: 実装する**

`internal/store/store.go` の `MonthOffset` / `MonthOffsets`（236行目以降）を丸ごと次に置き換える。

```go
// DayGroup は、その日に撮った写真の枚数。一覧の区切りに使う。
type DayGroup struct {
	Date  string // "2006-01-02" 形式。ローカル時刻で判定する
	Count int
}

// DayGroups は日ごとの枚数を新しい順に返す。一覧の区切りとスクラバーの目盛りに使う。
//
// SQLの strftime は UTC で日を切るため使わない。ローカルで未明に撮った写真が
// 前日に分類されてしまう。Go 側で time.Local に変換して数える。
func (s *Store) DayGroups(ctx context.Context) ([]DayGroup, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT taken_at FROM photos ORDER BY taken_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("撮影日時を取得できません: %w", err)
	}
	defer rows.Close()

	var out []DayGroup
	for rows.Next() {
		var at int64
		if err := rows.Scan(&at); err != nil {
			return nil, fmt.Errorf("撮影日時を読めません: %w", err)
		}
		d := time.Unix(at, 0).Format("2006-01-02")
		if len(out) > 0 && out[len(out)-1].Date == d {
			out[len(out)-1].Count++
			continue
		}
		out = append(out, DayGroup{Date: d, Count: 1})
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./internal/store/ -v`
Expected: PASS（`internal/web` はまだ `MonthOffsets` を呼んでいるのでビルドが通らない。それは次のステップで直す）

- [ ] **Step 5: 呼び出し側を一時的に繋ぐ**

`internal/web/handlers.go:128` の `s.st.MonthOffsets(r.Context())` を `s.st.DayGroups(r.Context())` に変え、`monthView` の組み立てを日から月へ丸める形に直す。**これはタスク3で `/dates` ごと消えるので、ここではビルドを通すためだけの繋ぎ**である。

```go
	days, err := s.st.DayGroups(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	out := make([]monthView, 0)
	offset := 0
	for _, d := range days {
		m := d.Date[:7]
		if len(out) == 0 || out[len(out)-1].M != m {
			out = append(out, monthView{M: m, O: offset})
		}
		offset += d.Count
	}
```

- [ ] **Step 6: 全体が通ることを確認する**

Run: `go build ./... && go test ./...`
Expected: PASS

- [ ] **Step 7: コミット**

```bash
git add internal/store/store.go internal/store/store_test.go internal/web/handlers.go
git commit -m "$(cat <<'MSG'
refactor: count photos per day instead of per month

日ごとの区切りには日単位の枚数が要る。MonthOffsets を DayGroups に
置き換え、月は日から導出する。UTCで切ると未明の写真が前日に落ちる
という MonthOffsets の知見はそのまま引き継いだ。日単位ではこの問題が
より頻繁に効く。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
MSG
)"
```

---

### Task 2: /items の各タイルに data-date

**Files:**
- Modify: `internal/web/handlers.go:17-22`（`photoView`）、`internal/web/handlers.go:41-58`（`buildRange`）
- Modify: `internal/web/templates/items.html`
- Test: `internal/web/gallery_test.go`

**Interfaces:**
- Consumes: なし
- Produces: `/items` と `/` が返す各 `.tile` に `data-date="2006-01-02"` が付く

- [ ] **Step 1: 失敗するテストを書く**

`internal/web/gallery_test.go` に追加する。

```go
func TestItemsTagsEachTileWithLocalDate(t *testing.T) {
	f := newWebFixture(t, 60)
	// TZ=UTC の環境でも回帰を検出できるよう、テスト中だけ固定オフセットにする。
	orig := time.Local
	time.Local = time.FixedZone("JST", 9*60*60)
	t.Cleanup(func() { time.Local = orig })

	// ローカルで2月8日の未明。UTCに直すと2月7日になる時刻。
	f.addPhoto(t, "a.jpg", time.Date(2026, 2, 8, 0, 30, 0, 0, time.Local), true)

	body := do(t, f.h, "/items?offset=0&limit=60").Body.String()

	require.Contains(t, body, `data-date="2026-02-08"`,
		"UTCで切ると2026-02-07になる。ローカル時刻で分類すること")
}

func TestGalleryTagsFirstChunkWithDates(t *testing.T) {
	f := newWebFixture(t, 60)
	f.addPhoto(t, "a.jpg", time.Date(2026, 2, 8, 12, 0, 0, 0, time.Local), true)

	body := do(t, f.h, "/").Body.String()

	require.Contains(t, body, `data-date="2026-02-08"`,
		"初回HTMLの先頭の塊にも日付が要る")
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/web/ -run 'TestItemsTagsEachTile|TestGalleryTagsFirstChunk' -v`
Expected: FAIL（`data-date=` が本文に無い）

- [ ] **Step 3: 実装する**

`internal/web/handlers.go` の `photoView` に `Date` を足す。

```go
// photoView は1枚分のテンプレート入力。
type photoView struct {
	ID       string
	ThumbURL string
	FullURL  string
	Date     string // "2006-01-02"。ローカル時刻。クライアントが日の区切りに使う
}
```

`buildRange` のループ内を次に変える。

```go
	for _, p := range photos {
		pv := photoView{
			ID:      p.ID,
			FullURL: "/photo/" + p.ID,
			ThumbURL: "/photo/" + p.ID,
			Date:    p.TakenAt.Format("2006-01-02"),
		}
		if p.HasThumb {
			pv.ThumbURL = "/thumb/" + p.ID
		}
		v.Photos = append(v.Photos, pv)
	}
```

`internal/web/templates/items.html` を次に変える。

```html
{{define "items"}}
{{range .Photos}}
<a class="tile" href="{{.FullURL}}" data-full="{{.FullURL}}" data-date="{{.Date}}">
  <img src="{{.ThumbURL}}" alt="" loading="lazy" decoding="async">
</a>
{{end}}
{{end}}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./internal/web/ -v`
Expected: PASS

- [ ] **Step 5: コミット**

```bash
git add internal/web/handlers.go internal/web/templates/items.html internal/web/gallery_test.go
git commit -m "$(cat <<'MSG'
feat: tag each tile with its local capture date

日ごとの表は初回HTMLの時点で固定されるため、開いたまま新着が入ると
表と実際の並びがずれる。ズレを検出して直すのではなく、サーバーが日付を
送ることでラベルの正しさを予測ではなく事実に載せる。表が古くても
狂うのはスクロールバーの目盛りだけになる。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
MSG
)"
```

---

### Task 3: 日ごとの表を埋め込み、/dates を削除する

**Files:**
- Modify: `internal/web/handlers.go:24-39`（`galleryView` / `monthView`）、`:76-119`（`handleGallery`）、`:122-146`（`handleDates` を削除）
- Modify: `internal/web/server.go:50`
- Modify: `internal/web/templates/gallery.html`
- Modify: `internal/web/static/app.js:277-286`（スクラバーの `/dates` 取得）
- Test: `internal/web/gallery_test.go:146-176`（`/dates` の2本を置き換え）、`internal/web/static_test.go:52-60`

**Interfaces:**
- Consumes: `store.DayGroups`（タスク1）
- Produces: 初回HTMLに `<script type="application/json" id="daygroups">[{"d":"2026-02-08","n":37},...]</script>` が入る。`/dates` は404になる

- [ ] **Step 1: 失敗するテストを書く**

`internal/web/gallery_test.go` の `TestDatesReturnsMonthOffsets` と `TestDatesOnEmptyLibraryReturnsEmptyArray` を削除し、次の3本に置き換える。

```go
// embeddedDayGroups は初回HTMLに埋め込まれた日ごとの表を取り出す。
func embeddedDayGroups(t *testing.T, body string) []struct {
	D string `json:"d"`
	N int    `json:"n"`
} {
	t.Helper()
	const open = `<script type="application/json" id="daygroups">`
	i := strings.Index(body, open)
	require.GreaterOrEqual(t, i, 0, "日ごとの表が埋め込まれていない")
	rest := body[i+len(open):]
	j := strings.Index(rest, "</script>")
	require.GreaterOrEqual(t, j, 0, "script タグが閉じていない")

	var out []struct {
		D string `json:"d"`
		N int    `json:"n"`
	}
	require.NoError(t, json.Unmarshal([]byte(rest[:j]), &out))
	return out
}

func TestGalleryEmbedsDayGroups(t *testing.T) {
	f := newWebFixture(t, 60)
	// 新しい順に: 2026-02-08 が2枚、2026-02-03 が1枚
	f.addPhoto(t, "a.jpg", time.Date(2026, 2, 8, 18, 0, 0, 0, time.Local), true)
	f.addPhoto(t, "b.jpg", time.Date(2026, 2, 8, 10, 0, 0, 0, time.Local), true)
	f.addPhoto(t, "c.jpg", time.Date(2026, 2, 3, 10, 0, 0, 0, time.Local), true)

	got := embeddedDayGroups(t, do(t, f.h, "/").Body.String())

	require.Len(t, got, 2)
	require.Equal(t, "2026-02-08", got[0].D)
	require.Equal(t, 2, got[0].N)
	require.Equal(t, "2026-02-03", got[1].D)
	require.Equal(t, 1, got[1].N)
}

func TestGalleryEmbedsEmptyDayGroupsForEmptyLibrary(t *testing.T) {
	f := newWebFixture(t, 60)

	got := embeddedDayGroups(t, do(t, f.h, "/").Body.String())

	require.Empty(t, got, "空でも配列として埋め込むこと（JSON.parse が落ちないように）")
}

func TestDatesEndpointIsGone(t *testing.T) {
	f := newWebFixture(t, 60)
	f.addPhoto(t, "a.jpg", time.Date(2026, 2, 8, 10, 0, 0, 0, time.Local), true)

	rec := do(t, f.h, "/dates")

	require.Equal(t, http.StatusNotFound, rec.Code,
		"日ごとの表は初回HTMLに埋め込むので、この口は持たない")
}
```

`internal/web/static_test.go` の `TestAppJSImplementsScrubber` を次に変える。

```go
func TestAppJSImplementsScrubber(t *testing.T) {
	f := newWebFixture(t, 10)

	body := do(t, f.h, "/static/app.js").Body.String()

	require.Contains(t, body, "#scrubber")
	require.Contains(t, body, "daygroups", "日ごとの表は埋め込みから読む")
	require.NotContains(t, body, "/dates", "エンドポイントは廃止した")
	require.Contains(t, body, "scrub-label", "ドラッグ中に年月を表示する")
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/web/ -run 'TestGalleryEmbeds|TestDatesEndpointIsGone|TestAppJSImplementsScrubber' -v`
Expected: FAIL（`日ごとの表が埋め込まれていない` / `/dates` が200を返す）

- [ ] **Step 3: サーバー側を実装する**

`internal/web/handlers.go` の `monthView` を消し、`galleryView` を次に変える。

```go
// dayView は埋め込む日ごとの表の1要素。
// 転送量を抑えるためキー名を短くしている（数千日ぶんになりうる）。
type dayView struct {
	D string `json:"d"` // "2006-01-02"
	N int    `json:"n"` // その日の枚数
}

// galleryView は gallery.html の入力。itemsViewを埋め込むので
// {{template "items" .}} にそのまま渡せる。
type galleryView struct {
	itemsView
	Total     int
	ChunkSize int
	// DayGroups は日ごとの枚数のJSON配列。html/template に再エスケープさせず
	// そのまま出すため template.JS で渡す。中身は日付と数値だけなので
	// "</script>" は構造上現れない。
	DayGroups template.JS
}
```

`handleGallery` の `total` 取得の直後に次を足し、`view` の組み立てを差し替える。

```go
	days, err := s.st.DayGroups(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	dv := make([]dayView, 0, len(days))
	for _, d := range days {
		dv = append(dv, dayView{D: d.Date, N: d.Count})
	}
	raw, err := json.Marshal(dv)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	view := galleryView{
		itemsView: items, Total: total, ChunkSize: s.pageSize,
		DayGroups: template.JS(raw),
	}
```

`handleDates` 関数を丸ごと削除する。`internal/web/server.go:50` の `mux.HandleFunc("GET /dates", s.handleDates)` の行を削除する。`handlers.go` の import から `encoding/json` は残し（`json.Marshal` で使う）、`html/template` を足す。

`internal/web/templates/gallery.html` の `</main>` の直後に次を足す。

```html
<script type="application/json" id="daygroups">{{.DayGroups}}</script>
```

- [ ] **Step 4: クライアント側を実装する**

`internal/web/static/app.js` のスクラバーのIIFE内、`/dates` を fetch している箇所（277-286行あたり）を次に置き換える。

```js
  // 日ごとの表は初回HTMLに埋め込まれている。これが無いと1枚も描けないため、
  // 非同期で取りに行く形にはしない。
  const daysEl = document.querySelector('#daygroups');
  const days = daysEl ? JSON.parse(daysEl.textContent) : [];

  // 月の境目を日ごとの表から導出する。月専用の口は持たない。
  const months = [];
  let offset = 0;
  for (const g of days) {
    const m = g.d.slice(0, 7);
    if (months.length === 0 || months[months.length - 1].m !== m) {
      months.push({ m, o: offset });
    }
    offset += g.n;
  }

  bar.hidden = false;
```

`let months = [];` の宣言と `fetch('/dates')...` のブロック、および元の `bar.hidden = false;` を削除する（上の置き換えに含まれている）。`monthAt` 関数はそのまま使える。

- [ ] **Step 5: テストが通ることを確認する**

Run: `go test ./internal/web/ -v`
Expected: PASS

- [ ] **Step 6: 実際に動かして確認する**

```bash
CGO_ENABLED=0 go build -o famifo-proto . && ./famifo-proto -dir ~/Pictures
```

ブラウザで開き、スクラバーをドラッグして年月ラベルが出ることを確認する。確認後 Ctrl-C で止める。

- [ ] **Step 7: コミット**

```bash
git add internal/web/ 
git commit -m "$(cat <<'MSG'
refactor: embed the day table instead of serving /dates

日ごとの表は「スクラバーのラベル用の飾り」から「これが無いと1枚も
描けない必須データ」に変わる。非同期取得のままでは初回表示が空白に
なるため、先頭の塊と同じ理由で初回HTMLに埋め込む。月は日から導出できる
ので月専用の口は持たない。ラウンドトリップも1本減る。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
MSG
)"
```

---

### Task 4: タイルに通し番号を書き、ライトボックスをそれに載せる

このタスクは**描画の切り替え（タスク6）より前**に置く。今の平らな描画のままでも成立するので、切り替え時にライトボックスが壊れない状態を先に作っておく。

**Files:**
- Modify: `internal/web/static/app.js:114-135`（`render`）、`:239-247`（タイルのクリック）
- Modify: `internal/web/static_test.go:40-50`

**Interfaces:**
- Consumes: なし
- Produces: 貼り付けられた各 `.tile` に `data-i="<通し番号>"` が付く

- [ ] **Step 1: 失敗するテストを書く**

`internal/web/static_test.go` の `TestAppJSLightboxUsesGlobalIndex` を次に変える。

```go
func TestAppJSLightboxUsesGlobalIndex(t *testing.T) {
	f := newWebFixture(t, 10)

	body := do(t, f.h, "/static/app.js").Body.String()

	require.Contains(t, body, "#lightbox")
	require.Contains(t, body, "urlAt", "DOMではなく通し番号でURLを引く")
	require.Contains(t, body, "touchstart", "スワイプ操作を実装している")
	require.Contains(t, body, "dataset.i", "タイルに書いた通し番号をそのまま読む")
	require.NotContains(t, body, "tiles().indexOf",
		"DOM上のタイル一覧に依存する実装は残さない")
	require.NotContains(t, body, "parentElement.querySelectorAll",
		"DOM上の位置を数える実装は残さない。カードに入ると数えられなくなる")
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/web/ -run TestAppJSLightboxUsesGlobalIndex -v`
Expected: FAIL（`dataset.i` が無く、`parentElement.querySelectorAll` が残っている）

- [ ] **Step 3: 実装する**

`app.js` の `render()` の末尾、`firstTile.style.gridColumnStart = ...` を設定しているブロックの直後に次を足す。

```js
    // 各タイルに通し番号を書く。ライトボックスはDOM上の位置を数えるのではなく
    // これを読む。位置を数える方式はタイルがカードに入ると成立しない。
    const tiles = win.querySelectorAll('.tile');
    for (let k = 0; k < tiles.length; k++) {
      tiles[k].dataset.i = pastedFrom + k;
    }
```

ライトボックスのクリックハンドラ（239-247行あたり）を次に変える。

```js
  document.addEventListener('click', (e) => {
    const tile = e.target.closest('#window .tile');
    if (!tile) return;
    e.preventDefault();
    open(Number(tile.dataset.i)).catch(() => {});
  });
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./internal/web/ -v`
Expected: PASS

- [ ] **Step 5: ブラウザテストで壊れていないことを確認する**

Run: `go test -tags browser ./internal/web/ -run 'TestTileTapOpensLightbox|TestLightboxCrossesChunkBoundary' -v`
Expected: PASS（Docker が無ければ SKIP。その場合は `docker pull chromedp/headless-shell:latest` してから再実行する）

- [ ] **Step 6: コミット**

```bash
git add internal/web/static/app.js internal/web/static_test.go
git commit -m "$(cat <<'MSG'
refactor: read the tile index from the tile itself

これまでは窓枠内でのDOM上の位置を数えて通し番号を復元していたが、
タイルが日ごとのカードに入ると parentElement がカードになり、その日の
中での位置しか数えられなくなる。組み立てるのはこちら側なので、位置を
数えるのをやめて通し番号を直接書く。間接計算が1つ消える。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
MSG
)"
```

---

### Task 5: レイアウト計算（純粋関数）

このタスクは**追加だけ**で、まだ誰も呼ばない。描画の切り替えはタスク6で行う。中核アルゴリズムを先に単体で固めてから載せる。

**Files:**
- Modify: `internal/web/static/app.js`（仮想スクロールのIIFE内に追加、`famifo` に公開）
- Test: `internal/web/browser_test.go`（新規テストを追加）

**Interfaces:**
- Consumes: なし
- Produces:
  - `famifo.layout(groups, cols, tileH, labelH, gap)` → `{entries, height, cols, tileH, labelH, gap}`
  - `famifo.yForIndex(L, i)` → number
  - `famifo.dayAtY(L, y)` → string
  - `famifo.visibleWindow(L, top, bottom)` → `{pieces, pasteY, from, to}` または `null`
  - `entries` の各要素: `{d, y, h, start, n, span, col, rows}`

  4つとも純粋関数で、モジュール内の状態を読まない。テストから直接叩くため。

- [ ] **Step 1: 失敗するテストを書く**

`internal/web/browser_test.go` の末尾に追加する。

```go
// --- Task: レイアウト計算 ---
//
// 貪欲詰めと二分探索は純粋関数だが、この repo にはJSを単体テストする手段が
// 無い（Node を足さない方針のため）。実ブラウザ上で関数を直接呼んで検証する。

// layoutEntry は famifo.layout が返す entries の1要素。
type layoutEntry struct {
	D     string  `json:"d"`
	Y     float64 `json:"y"`
	H     float64 `json:"h"`
	Start int     `json:"start"`
	N     int     `json:"n"`
	Span  int     `json:"span"`
	Col   int     `json:"col"`
	Rows  int     `json:"rows"`
}

type layoutResult struct {
	Entries []layoutEntry `json:"entries"`
	Height  float64       `json:"height"`
}

// evalLayout は famifo.layout をブラウザ上で呼ぶ。
// tileH=100, labelH=20, gap=4 に固定して、期待値を手計算できるようにする。
func evalLayout(t *testing.T, ctx context.Context, groupsJSON string, cols int) layoutResult {
	t.Helper()
	var got layoutResult
	err := chromedp.Run(ctx, chromedp.Evaluate(
		fmt.Sprintf(`famifo.layout(%s, %d, 100, 20, 4)`, groupsJSON, cols), &got))
	require.NoError(t, err)
	return got
}

func TestLayoutPacksDaysThatFitOneRow(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	err := chromedp.Run(ctx, chromedp.Navigate(baseURL), waitForTiles(10*time.Second))
	require.NoError(t, err)

	// 列数6。1枚 + 4枚 = 5列で同じストライプに載り、次の3枚は入らず次へ。
	got := evalLayout(t, ctx,
		`[{d:"2026-02-08",n:1},{d:"2026-02-03",n:4},{d:"2026-01-20",n:3}]`, 6)

	require.Len(t, got.Entries, 3)

	require.Equal(t, 0, got.Entries[0].Col)
	require.Equal(t, 1, got.Entries[0].Span)
	require.Equal(t, float64(0), got.Entries[0].Y)
	require.Equal(t, 0, got.Entries[0].Start)

	require.Equal(t, 1, got.Entries[1].Col, "1枚の日の右隣に載ること")
	require.Equal(t, 4, got.Entries[1].Span)
	require.Equal(t, float64(0), got.Entries[1].Y, "同じストライプなのでyが等しいこと")
	require.Equal(t, 1, got.Entries[1].Start)

	// 3枚目は残り1列に4列は載らないので次のストライプ。
	// ストライプ高 = labelH(20) + gap(4) + tileH(100) = 124。次のy = 124 + gap(4) = 128
	require.Equal(t, 0, got.Entries[2].Col, "入らないので次のストライプの先頭から")
	require.Equal(t, float64(128), got.Entries[2].Y)
	require.Equal(t, 5, got.Entries[2].Start)

	require.Equal(t, float64(128+124), got.Height)
}

func TestLayoutGivesWholeRowsToBigDays(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	err := chromedp.Run(ctx, chromedp.Navigate(baseURL), waitForTiles(10*time.Second))
	require.NoError(t, err)

	// 列数6。13枚は6列を占め、3段(6+6+1)になる。
	got := evalLayout(t, ctx, `[{d:"2026-02-08",n:13},{d:"2026-02-03",n:2}]`, 6)

	require.Len(t, got.Entries, 2)
	require.Equal(t, 6, got.Entries[0].Span, "列数を超える日は行を占有すること")
	require.Equal(t, 3, got.Entries[0].Rows)
	// h = 20 + 4 + 3*100 + 2*4 = 332
	require.Equal(t, float64(332), got.Entries[0].H)

	require.Equal(t, 0, got.Entries[1].Col, "行を占有した日の後は必ず次のストライプ")
	require.Equal(t, float64(332+4), got.Entries[1].Y)
	require.Equal(t, 13, got.Entries[1].Start)
}

func TestLayoutHandlesEmptyLibrary(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	err := chromedp.Run(ctx, chromedp.Navigate(baseURL), waitForTiles(10*time.Second))
	require.NoError(t, err)

	got := evalLayout(t, ctx, `[]`, 6)

	require.Empty(t, got.Entries)
	require.Equal(t, float64(0), got.Height, "空でも高さは0で、NaNにならないこと")
}

func TestLayoutLookupsAgreeWithEntries(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	err := chromedp.Run(ctx, chromedp.Navigate(baseURL), waitForTiles(10*time.Second))
	require.NoError(t, err)

	// yForIndex と dayAtY が entries と食い違わないこと。
	// 実装が二分探索なので、境界（各グループの先頭・末尾）を総当たりで確かめる。
	var mismatches []string
	err = chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const groups = [{d:"2026-02-08",n:1},{d:"2026-02-03",n:4},
		                {d:"2026-01-20",n:13},{d:"2026-01-05",n:2}];
		const L = famifo.layout(groups, 6, 100, 20, 4);
		const bad = [];
		for (const e of L.entries) {
			for (const i of [e.start, e.start + e.n - 1]) {
				const row = Math.floor((i - e.start) / e.span);
				const want = e.y + L.labelH + L.gap + row * (L.tileH + L.gap);
				const got = famifo.yForIndex(L, i);
				if (got !== want) bad.push('yForIndex(' + i + ')=' + got + ' want=' + want);
				const d = famifo.dayAtY(L, want);
				if (d !== e.d) bad.push('dayAtY(' + want + ')=' + d + ' want=' + e.d);
			}
		}
		return bad;
	})()`, &mismatches))
	require.NoError(t, err)
	require.Empty(t, mismatches, "二分探索が entries と食い違っている")
}

func TestVisibleWindowClipsBigDaysToRows(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)
	err := chromedp.Run(ctx, chromedp.Navigate(baseURL), waitForTiles(10*time.Second))
	require.NoError(t, err)

	// 100枚の日（6列で17段）の途中だけを切り出せること。
	// ラベル(20)+gap(4)の下に段が並ぶので、y=124+3*104=436 は4段目の先頭。
	var got struct {
		From   int     `json:"from"`
		To     int     `json:"to"`
		PasteY float64 `json:"pasteY"`
		Pieces int     `json:"pieces"`
	}
	err = chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const L = famifo.layout([{d:"2026-02-08",n:100}], 6, 100, 20, 4);
		const w = famifo.visibleWindow(L, 436, 436 + 200);
		return {from: w.from, to: w.to, pasteY: w.pasteY, pieces: w.pieces.length};
	})()`, &got))
	require.NoError(t, err)

	require.Equal(t, 1, got.Pieces)
	require.Equal(t, 18, got.From, "4段目の先頭 = 3*6")
	require.Equal(t, float64(436), got.PasteY, "貼り付け位置は切り出した段の上端")
	require.Greater(t, got.To, got.From)
	require.Less(t, got.To, 100, "100枚まるごとではなく可視ぶんだけ切り出すこと")
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test -tags browser ./internal/web/ -run 'TestLayout|TestVisibleWindow' -v`
Expected: FAIL（`famifo.layout is not a function`）。Docker が無ければ SKIP になるので、その場合は `docker pull chromedp/headless-shell:latest` してから再実行する

- [ ] **Step 3: 実装する**

`app.js` の仮想スクロールIIFE内、`measure()` の直前に次を足す。

```js
  // --- レイアウト計算 ---
  //
  // 日ごとに「占める列数 = min(枚数, 列数)」を割り当て、順に詰める。入らな
  // ければ次のストライプへ送る。これは CSS Grid の自動配置（dense を付けない
  // 場合）の規則そのものなので、同じ順でカードを流し込めばブラウザはここと
  // 同じ答えを出す。列位置をJSが指定して回る必要はない。
  //
  // ここから下の4つは純粋関数。モジュールの状態を読まないので、ブラウザ
  // テストから直接叩いて検証できる。
  function layout(groups, cols, tileH, labelH, gap) {
    const entries = [];
    let y = 0;       // いま組み立て中のストライプの上端
    let stripeH = 0; // その高さ。0なら未開始
    let rem = 0;     // その残り列数
    let start = 0;   // 次のグループの先頭写真の通し番号

    for (const g of groups) {
      const span = Math.min(g.n, cols);
      const rows = Math.ceil(g.n / span);
      const h = labelH + gap + rows * tileH + (rows - 1) * gap;

      if (span < cols && rem >= span) {
        // いまのストライプに載る。横並びになるのはこの経路だけ。
        // 1行に収まる日は必ず rows===1 なので、高さはストライプと一致する。
        entries.push({ d: g.d, y, h, start, n: g.n, span, col: cols - rem, rows });
        rem -= span;
      } else {
        if (stripeH > 0) y += stripeH + gap; // 前のストライプを閉じる
        entries.push({ d: g.d, y, h, start, n: g.n, span, col: 0, rows });
        stripeH = h;
        rem = cols - span; // 行を占有した日(span===cols)なら0になり、次は必ず新しい行
      }
      start += g.n;
    }

    return { entries, height: stripeH > 0 ? y + stripeH : 0, cols, tileH, labelH, gap };
  }

  // key(e) が v 以下である最後の要素の添字。無ければ0。
  function lastAtMost(entries, v, key) {
    let lo = 0;
    let hi = entries.length - 1;
    let found = 0;
    while (lo <= hi) {
      const mid = (lo + hi) >> 1;
      if (key(entries[mid]) <= v) { found = mid; lo = mid + 1; } else { hi = mid - 1; }
    }
    return found;
  }

  // 通し番号 i の写真が属する段の上端。
  function yForIndex(L, i) {
    if (L.entries.length === 0) return 0;
    const e = L.entries[lastAtMost(L.entries, i, (x) => x.start)];
    const row = Math.floor((i - e.start) / e.span);
    return e.y + L.labelH + L.gap + row * (L.tileH + L.gap);
  }

  // y の位置にある日。スクラバーのラベルが使う。
  function dayAtY(L, y) {
    if (L.entries.length === 0) return '';
    return L.entries[lastAtMost(L.entries, y, (x) => x.y)].d;
  }

  // [top, bottom] に重なる範囲を切り出す。
  //
  // 詰めたストライプは丸ごと描く。ラベルを落とすと高さが変わり、同じ
  // ストライプに並ぶ他のカードと段が合わなくなるため。1ストライプは
  // 高々 labelH + gap + tileH しかないので丸ごとでも安い。
  // 列数を超える日だけは段単位で切り、ラベルが上に流れていれば落とす。
  function visibleWindow(L, top, bottom) {
    const es = L.entries;
    if (es.length === 0) return null;

    let i = lastAtMost(es, top, (x) => x.y);
    while (i > 0 && es[i - 1].y === es[i].y) i--; // ストライプの先頭まで戻る

    const pieces = [];
    for (; i < es.length; i++) {
      const e = es[i];
      if (e.y > bottom) break;

      const tileTop = e.y + L.labelH + L.gap;
      let r0 = 0;
      let r1 = e.rows - 1;
      if (e.rows > 1) {
        r0 = Math.max(0, Math.floor((top - tileTop) / (L.tileH + L.gap)));
        r1 = Math.min(e.rows - 1, Math.floor((bottom - tileTop) / (L.tileH + L.gap)));
        if (r1 < r0) continue; // まるごと範囲外
      } else if (e.y + e.h < top) {
        continue;
      }
      pieces.push({ e, r0, r1 });
    }
    if (pieces.length === 0) return null;

    const f = pieces[0];
    const l = pieces[pieces.length - 1];
    return {
      pieces,
      // 先頭が段の途中から始まるならその段の上端、そうでなければカードの上端
      pasteY: f.r0 > 0 ? f.e.y + L.labelH + L.gap + f.r0 * (L.tileH + L.gap) : f.e.y,
      from: f.e.start + f.r0 * f.e.span,
      to: Math.min(l.e.start + l.e.n, l.e.start + (l.r1 + 1) * l.e.span),
    };
  }
```

`famifo` の返り値（`return { total, chunkSize, ... }` のオブジェクト）に次を足す。

```js
    layout,
    yForIndex,
    dayAtY,
    visibleWindow,
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test -tags browser ./internal/web/ -run 'TestLayout|TestVisibleWindow' -v`
Expected: PASS（5本）

- [ ] **Step 5: 既存が壊れていないことを確認する**

Run: `go test ./... && go test -tags browser ./internal/web/`
Expected: PASS（このタスクは追加だけで、まだ誰も `layout` を呼んでいない）

- [ ] **Step 6: コミット**

```bash
git add internal/web/static/app.js internal/web/browser_test.go
git commit -m "$(cat <<'MSG'
feat: add the day-packing layout calculation

「占める列数 = min(枚数, 列数)」で貪欲に詰め、累積和と二分探索で
「通し番号 → y」「y → 日」「範囲 → 切り出し」を引けるようにする。
この規則は CSS Grid の自動配置（denseなし）そのものなので、同じ順で
流し込めばブラウザの実配置とずれない。

まだ誰も呼んでいない。純粋関数として先に固め、描画は次で載せ替える。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
MSG
)"
```

---

### Task 6: 描画をレイアウトに載せ替える（山場）

一覧の見た目が変わるのはこのタスク。ここまでの5つは準備で、見た目は変わっていない。

**Files:**
- Modify: `internal/web/static/app.js`（`seedFirstChunk` / `measure` / `fetchChunk` / `render`）
- Modify: `internal/web/static/app.css`
- Modify: `internal/web/static_test.go:27-38`

**Interfaces:**
- Consumes: `famifo.layout` / `visibleWindow`（タスク5）、各タイルの `data-date`（タスク2）、埋め込みの日ごとの表（タスク3）
- Produces: `famifo.current()` → いまのレイアウト（`layout()` の戻り値、未測定なら `null`）、`famifo.pastedRange()` → `{from, to}`。`famifo.pastedIndex()` は削除

- [ ] **Step 1: 失敗するテストを書く**

`internal/web/static_test.go` の `TestAppJSImplementsVirtualScroll` を次に変える。

```go
func TestAppJSImplementsVirtualScroll(t *testing.T) {
	f := newWebFixture(t, 10)

	body := do(t, f.h, "/static/app.js").Body.String()

	require.Contains(t, body, "#spacer")
	require.Contains(t, body, "#window")
	require.Contains(t, body, "gridTemplateColumns", "列数はブラウザの計算結果から読む")
	require.Contains(t, body, "--label-h", "ラベル高はCSSの1箇所から読む")
	require.Contains(t, body, "/items?offset=")
	require.Contains(t, body, "data-full", "塊からURLを控えてライトボックスに渡す")
	require.Contains(t, body, "daycard", "日ごとのカードを組み立てる")
	require.Contains(t, body, "famifo", "他のスクリプトから使える形で公開する")
	require.NotContains(t, body, "gridColumnStart",
		"塊単位で貼って列位置を補正する方式は廃した")
	require.NotContains(t, body, "pastedIndex",
		"貼り付け先頭ではなく範囲を公開する")
}

func TestAppCSSDefinesDayCards(t *testing.T) {
	f := newWebFixture(t, 10)

	body := do(t, f.h, "/static/app.css").Body.String()

	require.Contains(t, body, "--label-h", "ラベル高の定義はここ1箇所だけ")
	require.Contains(t, body, ".daycard")
	require.Contains(t, body, ".daylabel")
	require.Contains(t, body, "1 / -1", "ラベルはカードの全幅を占める")
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/web/ -run 'TestAppJSImplementsVirtualScroll|TestAppCSSDefinesDayCards' -v`
Expected: FAIL

- [ ] **Step 3: CSSを足す**

`internal/web/static/app.css` の `:root` に `--label-h` を足す。

```css
  /* 日付ラベルの高さ。JSがレイアウト計算に使うため、定義はここ1箇所だけ。 */
  --label-h: 20px;
```

`.tile` の定義の直前に次を足す。

```css
/* 日ごとのカード。占める列数はJSがインラインで書く（min(枚数, 列数)）。
   repeat(var(--n), 1fr) はCSSの仕様上書けないため custom property に寄せられない。
   内側は外側と同じgapの等幅グリッドなので、中の1枚は外側の1列と一致する。 */
.daycard {
  display: grid;
  gap: var(--gap);
}

.daylabel {
  grid-column: 1 / -1; /* 全幅を占め、以降のタイルを次の段へ送る */
  height: var(--label-h);
  font-size: 0.75rem;
  line-height: var(--label-h);
  opacity: 0.65;
}
```

- [ ] **Step 4: 塊のキャッシュをタイル単位にする**

`app.js` の `seedFirstChunk` と `fetchChunk` を次に置き換える。

```js
  // サーバが返したHTML断片を、タイル1枚ずつに割る。取得時に1回だけパースし、
  // 以降はここから必要な範囲を切り出して組み立てる。
  function parseTiles(html) {
    const tmp = document.createElement('div');
    tmp.innerHTML = html;
    return [...tmp.querySelectorAll('.tile')].map((a) => ({
      html: a.outerHTML,
      url: a.dataset.full,
      date: a.dataset.date,
    }));
  }

  // 初回ページはサーバが先頭の塊を埋めて返しているので、取得済みとして控える。
  function seedFirstChunk() {
    const tiles = parseTiles(win.innerHTML);
    if (tiles.length > 0) chunks.set(0, tiles);
  }
```

`fetchChunk` の中の `const entry = { html, urls }` 以下を次に変える。

```js
      const tiles = parseTiles(html);
      chunks.set(ci, tiles);
      return tiles;
```

`urlAt` を次に変える。

```js
  async function urlAt(i) {
    if (i < 0 || i >= total) return null;
    const tiles = await fetchChunk(Math.floor(i / chunkSize));
    return tiles[i % chunkSize]?.url ?? null;
  }

  // 取得済みの塊からタイルを引く。未取得なら null。
  function tileAt(i) {
    const tiles = chunks.get(Math.floor(i / chunkSize));
    return tiles ? tiles[i % chunkSize] ?? null : null;
  }
```

- [ ] **Step 5: measure と render を置き換える**

`app.js` の `measure()` を次に置き換える。`let cols = 1; let rowH = 0; let pastedFrom = 0;` の宣言を `let L = null; let pasted = { from: 0, to: 0 };` に差し替える。

```js
  // 日ごとの表は初回HTMLに埋め込まれている。これが無いと1枚も描けない。
  const daysEl = document.querySelector('#daygroups');
  const days = daysEl ? JSON.parse(daysEl.textContent) : [];

  // 列数・列幅・gap はCSSの計算結果から読む。auto-fill の計算を自前で再現すると
  // CSSのbreakpointと二重管理になる。ラベル高も定義はCSS側の1箇所だけ。
  function measure() {
    const cs = getComputedStyle(win);
    const tracks = cs.gridTemplateColumns.split(' ').filter((t) => t.length > 0);
    const cols = Math.max(1, tracks.length);
    const tileW = parseFloat(tracks[0]);
    if (!(tileW > 0)) {
      L = null; // スタイル未適用。次の resize/scroll で測り直す
      return;
    }
    const gap = parseFloat(cs.rowGap) || 0;
    const labelH = parseFloat(
      getComputedStyle(document.documentElement).getPropertyValue('--label-h')) || 0;

    L = layout(days, cols, tileW, labelH, gap); // タイルは正方形なので幅がそのまま高さ
    spacer.style.height = `${Math.max(0, L.height)}px`;
  }
```

`render()` を次に置き換える。

```js
  function render() {
    if (!L || L.height <= 0 || total === 0) return;

    const over = OVERSCAN_ROWS * (L.tileH + L.gap);
    const w = visibleWindow(L,
      scroller.scrollTop - over,
      scroller.scrollTop + window.innerHeight + over);
    if (!w) return;

    // 可視範囲の前後1塊も先読みしておく。切り出す範囲は広げない。
    const firstChunk = Math.floor(w.from / chunkSize);
    const lastChunk = Math.floor((w.to - 1) / chunkSize);
    const fetchFrom = Math.max(0, firstChunk - 1);
    const fetchTo = Math.min(Math.floor((total - 1) / chunkSize), lastChunk + 1);
    for (let ci = fetchFrom; ci <= fetchTo; ci++) {
      if (!chunks.has(ci)) fetchChunk(ci).then(render).catch(() => {});
    }

    // 必要な塊が1つでも欠けていると穴の空いたカードになるので、揃うまで描かない
    for (let ci = firstChunk; ci <= lastChunk; ci++) {
      if (!chunks.has(ci)) return;
    }

    // 貼る内容が前回と同じなら触らない。スクロールのたびに innerHTML を
    // 書き換えると画像の再読み込みが起きる。
    const key = `${w.from}:${w.to}:${L.cols}`;
    if (key === renderedKey) return;
    renderedKey = key;
    pasted = { from: w.from, to: w.to };

    const parts = [];
    for (const p of w.pieces) {
      const from = p.e.start + p.r0 * p.e.span;
      const to = Math.min(p.e.start + p.e.n, p.e.start + (p.r1 + 1) * p.e.span);
      parts.push(cardHTML(p, from, to));
    }
    win.innerHTML = parts.join('');
    win.style.transform = `translateY(${w.pasteY}px)`;

    // 各タイルに通し番号を書く。切り出す範囲は連続しているのでDOM順と一致する。
    const tiles = win.querySelectorAll('.tile');
    for (let k = 0; k < tiles.length; k++) tiles[k].dataset.i = w.from + k;
  }

  // 1枚のカード。占める列数はレイアウトが決め、ラベルの文言はタイル自身の
  // data-date から作る。日ごとの表が古くても、ラベルはそのカードに実際に
  // 写っている日を指す。
  function cardHTML(piece, from, to) {
    const tiles = [];
    for (let i = from; i < to; i++) {
      const t = tileAt(i);
      if (!t) return '';
      tiles.push(t.html);
    }
    // 段の途中から貼るとき（大きい日をスクロールしている最中）はラベルを落とす
    const head = tileAt(from);
    const label = piece.r0 > 0 || !head ? ''
      : `<div class="daylabel">${formatDay(head.date)}</div>`;
    return `<div class="daycard" style="grid-column:span ${piece.e.span};`
      + `grid-template-columns:repeat(${piece.e.span},1fr)">${label}${tiles.join('')}</div>`;
  }

  // "2026-02-08" → "2026年2月8日"。今年なら年を省く。
  // 最狭の1列(約120px)に収めるため、これ以上長い表記にはしない。
  function formatDay(d) {
    if (!d) return '';
    const [y, m, day] = d.split('-');
    const head = Number(y) === new Date().getFullYear() ? '' : `${y}年`;
    return `${head}${Number(m)}月${Number(day)}日`;
  }
```

`render()` の末尾にあった `firstTile.style.gridColumnStart = ...` のブロックと、タスク4で足した `data-i` のループは、上の新しい `render()` に統合済みなので削除する。

`famifo` の返り値から `pastedIndex` を削除し、次を足す。

```js
    current: () => L,
    pastedRange: () => pasted,
```

- [ ] **Step 6: onResize を暫定で通す**

`onResize` は `cols` / `rowH` を参照しているのでコンパイルが通らない。**位置復元の正しい実装はタスク8**なので、ここでは最小限にする。

```js
  function onResize() {
    const prev = L;
    measure();
    // ResizeObserver は #window 自身の高さの変化でも発火する。貼り付ける量は
    // スクロール中に増減するため、通常のスクロールでも呼ばれる。実際に列数も
    // タイル高も変わっていないなら、貼り直しは不要。
    if (prev && L && prev.cols === L.cols && prev.tileH === L.tileH) return;
    renderedKey = ''; // 列数が変われば貼り直しが必要
    render();
  }
```

- [ ] **Step 7: テストが通ることを確認する**

Run: `go test ./internal/web/ -v`
Expected: PASS

- [ ] **Step 8: 実際に動かして目で確かめる**

```bash
CGO_ENABLED=0 go build -o famifo-proto . && ./famifo-proto -dir ~/Pictures
```

確認すること:
- 日付の見出しが出ている
- 1枚しかない日が横に並んでいる（古い年代までスクロールすると出る）
- 枚数の多い日が行を占有している
- スクロールしても写真がずれた位置に出ない
- タイルをタップすると正しい写真が開く

確認後 Ctrl-C で止める。

- [ ] **Step 9: ブラウザテストの状態を記録する**

Run: `go test -tags browser ./internal/web/ -v`
Expected: **一部 FAIL**。corpus が1日1枚のままで、位置計算も旧方式のため。落ちたテスト名を控えておく（タスク9・10で直す）

- [ ] **Step 10: コミット**

```bash
git add internal/web/static/app.js internal/web/static/app.css internal/web/static_test.go
git commit -m "$(cat <<'MSG'
feat: render the gallery in day-grouped cards

一覧を日ごとに区切り、1行に収まる日は横に並べる。貼る単位が塊から行に
変わり、塊をまるごと貼って gridColumnStart でずれを補正する細工は消えた。
ラベルの文言はタイル自身の data-date から作るので、日ごとの表が古くても
ラベルは実際に写っている日を指す。

ブラウザテストは corpus が1日1枚のままなので一部落ちる。タスク9・10で直す。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
MSG
)"
```

---

### Task 7: スクラバーをレイアウトに載せ替える

**Files:**
- Modify: `internal/web/static/app.js`（スクラバーのIIFE）

**Interfaces:**
- Consumes: `famifo.current()` / `famifo.dayAtY()`（タスク5・6）
- Produces: なし

- [ ] **Step 1: 失敗するテストを書く**

`internal/web/static_test.go` の `TestAppJSImplementsScrubber` に2行足す。

```go
	require.Contains(t, body, "dayAtY", "位置から日を引く。枚数の比例では求まらない")
	require.NotContains(t, body, "frac * famifo.total",
		"行の高さが不均一なので、割合×総枚数では位置を求められない")
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/web/ -run TestAppJSImplementsScrubber -v`
Expected: FAIL

- [ ] **Step 3: 実装する**

スクラバーのIIFEから、タスク3で足した `#daygroups` の parse と `months` の導出、および `monthAt` 関数を削除する。`seek` を次に変える。

```js
  function seek(clientY) {
    const rect = bar.getBoundingClientRect();
    const frac = Math.min(1, Math.max(0, (clientY - rect.top) / rect.height));
    const y = frac * famifo.maxScroll();
    famifo.scroller.scrollTop = y;

    // 行の高さが日ごとに違うため、割合×総枚数では位置を求められない。
    // スクロール位置そのものからレイアウトを引く。
    const L = famifo.current();
    const d = L ? famifo.dayAtY(L, y) : '';
    if (d) {
      // ドラッグは17年ぶんを一気に動かすので、日まで出すとちらつく。月で止める。
      const [yy, mm] = d.split('-');
      label.textContent = `${yy}年${Number(mm)}月`;
      label.hidden = false;
      label.style.top = `${Math.min(rect.height - 24, Math.max(0, clientY - rect.top - 12))}px`;
    }
  }
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./internal/web/ -v`
Expected: PASS

- [ ] **Step 5: 実際に動かして確かめる**

アプリを起動し、スクラバーをドラッグして年月ラベルが実際に見えている写真と合っていることを確認する。

- [ ] **Step 6: コミット**

```bash
git add internal/web/static/app.js internal/web/static_test.go
git commit -m "$(cat <<'MSG'
fix: look up the scrubber month from the scroll position

これまでは「割合 × 総枚数」で位置を月に換算していたが、行の高さが日ごとに
変わった時点でこの比例関係は成立しない。スクロール位置からレイアウトを
二分探索して日を引き、その月を出す。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
MSG
)"
```

---

### Task 8: リサイズ時の位置復元

**Files:**
- Modify: `internal/web/static/app.js`（`onResize`）

**Interfaces:**
- Consumes: `famifo.yForIndex`（タスク5）、`famifo.pastedRange()`（タスク6）
- Produces: なし

- [ ] **Step 1: 失敗するテストを書く**

`internal/web/browser_test.go` の `TestScrollAnchoredOnResize` は既にこの振る舞いを検証している。corpus の作り直し（タスク9）まで正しく動かないため、ここでは実装のみ行い、検証はタスク10で行う。代わりに `static_test.go` に置く。

```go
func TestAppJSRestoresPositionByPhotoIndex(t *testing.T) {
	f := newWebFixture(t, 10)

	body := do(t, f.h, "/static/app.js").Body.String()

	require.Contains(t, body, "yForIndex",
		"復元先は通し番号から引く。行の高さが不均一なので掛け算では出ない")
	require.NotContains(t, body, "Math.floor(topIndex / cols) * rowH",
		"均一な行を前提にした復元は残さない")
	require.NotContains(t, body, "pasted.from :",
		"アンカーに貼り付け範囲の先頭を使わない。OVERSCAN のぶん手前に着地する")
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/web/ -run TestAppJSRestoresPositionByPhotoIndex -v`
Expected: FAIL

- [ ] **Step 3: 実装する**

タスク6で暫定にした `onResize` を次に置き換える。

```js
  function onResize() {
    // 回転やリサイズで列数が変わるとレイアウト全体の高さが変わるため、
    // scrollTop をそのまま残すと別の写真の位置に飛ぶ。いま先頭に見えていた
    // 写真の通し番号を保持して復元する。
    //
    // アンカーに pasted.from は使わない。あれは OVERSCAN のぶん画面外まで
    // 含んだ範囲の先頭なので、復元すると毎回4行ぶん手前に着地する。
    // オーバースキャン抜きの、いま実際に画面上端にある写真を取る。
    const prev = L;
    const at = prev ? visibleWindow(prev, scroller.scrollTop, scroller.scrollTop) : null;
    const topIndex = at ? at.from : 0;

    measure();

    // ResizeObserver は #window 自身の高さの変化でも発火する。貼り付ける量は
    // スクロール中に増減するため、通常のスクロールでも呼ばれる。実際に列数も
    // タイル高も変わっていないなら、貼り直しも位置の復元も不要。
    if (prev && L && prev.cols === L.cols && prev.tileH === L.tileH) return;

    renderedKey = ''; // 列数が変われば貼り直しが必要
    if (L && L.height > 0) {
      scroller.scrollTop = yForIndex(L, topIndex);
    }
    render();
  }
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./internal/web/ -v`
Expected: PASS

- [ ] **Step 5: 実際に動かして確かめる**

アプリを開いて奥までスクロールし、ブラウザ窓の幅を変えて、同じあたりの写真が見えたままであることを確認する。

- [ ] **Step 6: コミット**

```bash
git add internal/web/static/app.js internal/web/static_test.go
git commit -m "$(cat <<'MSG'
fix: restore the resize anchor through the layout

列数が変わったときの復元先を、均一な行を前提にした掛け算ではなく
「通し番号 → y」の二分探索で引く。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
MSG
)"
```

---

### Task 9: テスト用 corpus を日ごとの枚数が混ざる形にする

**Files:**
- Modify: `internal/web/browser_test.go:54-60`（定数）、`:250-273`（`seedCorpus`）

**Interfaces:**
- Consumes: なし
- Produces: `testDayCounts []int`、`testPhotoCount`（合計）、`dayOfPhoto(i) string`、`dayStartIndex(day int) int`

- [ ] **Step 1: 定数を差し替える**

`browser_test.go` の const ブロックから `testPhotoCount = 200` を削除し、その位置に次を置く。

```go
	testPageSize = 60

	// deepScrollIndex は「複数の塊を跨いだ、十分に奥」の位置。
	// 塊を2つ跨いだうえで、境界のちょうど上に止まらないよう半端に足す。
	deepScrollIndex = testPageSize*2 + 30
)

// testDayCounts は日ごとの枚数（新しい順）。1枚の日・数枚の日・列数を超える
// 大きい日を混ぜる。日ごとの枚数がそのままレイアウトを決めるため、以前の
// 「1日1枚」のcorpusでは横並びも行占有も検証できない。
// デスクトップ幅(1600px)は7列なので、8枚以上の日が行を占有する側になる。
var testDayCounts = []int{
	20, 1, 3, 1, 9, 2, 1, 5, 14, 1,
	1, 4, 30, 2, 1, 7, 1, 3, 25, 1,
	6, 1, 2, 11, 1, 1, 4, 18, 3, 1,
	20,
}

// testPhotoCount は testDayCounts の合計（200枚）。
var testPhotoCount = func() int {
	n := 0
	for _, c := range testDayCounts {
		n += c
	}
	return n
}()

// dayOfPhoto は通し番号iの写真が属する日を "2006-01-02" で返す。
func dayOfPhoto(i int) string {
	seen := 0
	for d, c := range testDayCounts {
		if i < seen+c {
			return corpusBase.AddDate(0, 0, -d).Format("2006-01-02")
		}
		seen += c
	}
	return ""
}

// dayStartIndex は d 日目（0起点、新しい順）の先頭写真の通し番号。
func dayStartIndex(d int) int {
	n := 0
	for k := 0; k < d; k++ {
		n += testDayCounts[k]
	}
	return n
}

// corpusBase は最も新しい日。seedCorpus と上記2つが共有する。
var corpusBase = time.Date(2026, 1, 1, 12, 0, 0, 0, time.Local)
```

- [ ] **Step 2: seedCorpus を書き換える**

`seedCorpus` の `base := ...` の行と `for i := 0; i < n; i++ {` のループを次に置き換える（`n` 引数は使わなくなるので `_ int` にはせず、シグネチャから外して呼び出し側 `seedCorpus(st, gen, photoDir)` も直す）。

```go
// seedCorpus は testDayCounts のとおりに実画像ファイルを生成して登録する。
// ユーザーの実ライブラリを読むと実行環境ごとに結果が変わりCIで再現できない
// ため、写真は常にこの場で作る。1日の中では撮影時刻を1分ずつ古くするので、
// 通し番号iがそのままギャラリー上の並び順(新しい順)に対応する。
func seedCorpus(st *store.Store, gen *thumb.Generator, photoDir string) error {
	ctx := context.Background()

	i := 0
	for d, count := range testDayCounts {
		for k := 0; k < count; k++ {
			name := fmt.Sprintf("p%04d.jpg", i)
			path := filepath.Join(photoDir, name)
			if err := writeTestJPEG(path, i); err != nil {
				return fmt.Errorf("テスト画像を書けません (%s): %w", name, err)
			}

			id := store.IDFor(path)
			hasThumb := gen.Generate(path, id) == nil

			// 分単位で戻す。最大30枚なので日をまたがない。
			takenAt := corpusBase.AddDate(0, 0, -d).Add(-time.Duration(k) * time.Minute)
			fi, statErr := os.Stat(path)
			if statErr != nil {
				return fmt.Errorf("テスト画像を統計できません (%s): %w", name, statErr)
			}
			p := store.Photo{
				ID: id, Path: path, TakenAt: takenAt, ModTime: takenAt,
				Size: fi.Size(), Ext: ".jpg", HasThumb: hasThumb,
			}
			if err := st.Upsert(ctx, p); err != nil {
				return fmt.Errorf("写真を登録できません (%s): %w", name, err)
			}
			i++
		}
	}
	return nil
}
```

- [ ] **Step 3: corpus が意図どおりであることを確認するテストを足す**

```go
// corpus 自体が壊れると、他のブラウザテストが理由不明で落ちる。先に押さえる。
func TestCorpusHasMixedDaySizes(t *testing.T) {
	require.Equal(t, 200, testPhotoCount)
	small, big, single := 0, 0, 0
	for _, c := range testDayCounts {
		switch {
		case c == 1:
			single++
		case c <= 7: // デスクトップ(1600px)は7列。ここまでが横に並ぶ側
			small++
		default:
			big++
		}
	}
	require.Greater(t, single, 2, "1枚だけの日が要る（横並びの検証に使う）")
	require.Greater(t, small, 2, "数枚の日が要る")
	require.Greater(t, big, 2, "列数を超える日が要る（行占有の検証に使う）")
	require.Equal(t, 0, dayStartIndex(0))
	require.Equal(t, testDayCounts[0], dayStartIndex(1))
	require.Equal(t, dayOfPhoto(0), dayOfPhoto(testDayCounts[0]-1), "同じ日に入ること")
	require.NotEqual(t, dayOfPhoto(0), dayOfPhoto(testDayCounts[0]), "次は別の日")
}
```

このテストはブラウザを使わないが、`browser_test.go` に置くため `-tags browser` が要る。corpus の定義と同じファイルに置くほうが、片方だけ変えて食い違う事故を防げる。

- [ ] **Step 4: 確認する**

Run: `go test -tags browser ./internal/web/ -run TestCorpusHasMixedDaySizes -v`
Expected: PASS

- [ ] **Step 5: コミット**

```bash
git add internal/web/browser_test.go
git commit -m "$(cat <<'MSG'
test: give the browser corpus a mix of day sizes

これまでの corpus は撮影日時を1日ずつずらしており、全日が1枚だった。
日ごとの枚数がそのままレイアウトを決めるようになったため、これでは
横並びも行占有も一度も通らない。1枚の日・数枚の日・列数を超える日を
混ぜた200枚に作り直す。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
MSG
)"
```

---

### Task 10: 既存ブラウザテストをレイアウトに載せ替える

**Files:**
- Modify: `internal/web/browser_test.go`（`scrollToPhotoJS`、`TestNoRepaintOnPlainScroll`、`TestGridAlignmentPastFirstChunk` の削除）

**Interfaces:**
- Consumes: `famifo.current()` / `yForIndex()` / `pastedRange()`（タスク5・6）、`dayOfPhoto`（タスク9）
- Produces: なし

- [ ] **Step 1: TestGridAlignmentPastFirstChunk を削除する**

`browser_test.go:349-445` あたりの、コメントブロック `// --- Task: TestGridAlignmentPastFirstChunk ---` から関数の閉じ括弧までを削除する。

これは「塊の先頭が行頭でないと横方向にずれる」不具合を守るテストだった。**面倒だから消すのではなく、守る対象が構造的に消えたから消す。** 貼る単位が塊から行に変わり、行は必ずカードの境界で始まるため、列位置の補正という概念そのものが無くなった（`gridColumnStart` を設定するコードは残っていない。`TestAppJSImplementsVirtualScroll` がその不在を検証している）。

- [ ] **Step 2: scrollToPhotoJS を差し替える**

```go
// scrollToPhotoJS は写真indexが可視範囲の先頭に来るまでスクロールするJS式を返す。
//
// 以前はタイルの実測高から行を逆算していたが、行の高さが日ごとに変わるため
// 成立しない。実装が公開している「通し番号 → y」をそのまま呼ぶ。テストが
// レイアウト規則を写経すると、実装と一緒に間違えても気づけない。
func scrollToPhotoJS(index int) string {
	return fmt.Sprintf(`(() => {
		famifo.scroller.scrollTop = famifo.yForIndex(famifo.current(), %d);
	})()`, index)
}
```

- [ ] **Step 3: TestNoRepaintOnPlainScroll の静定条件を差し替える**

`allChunksPastedJS` の定数と、それを使う `chromedp.Poll` / `allChunksPollErr` のブロック、および `famifo.pastedIndex() >= testPageSize` の Poll を、次に置き換える。

```go
	// 貼り付け範囲が動かなくなったことを静定とみなす。
	//
	// 以前は「貼り付けタイル数 == total - chunkSize」で静定を判定していたが、
	// この等式は貼り付け窓が最終行まで届くことに依存しており、corpus や
	// viewport を変えると無言のPollタイムアウトで落ちる（元のコメント参照）。
	// 総枚数から逆算するのをやめれば、その脆さごと消える。
	const rangeSettledJS = `(() => {
		const r = famifo.pastedRange();
		const k = r.from + ':' + r.to;
		if (window.__lastRange === k) {
			return (window.__rangeStable = (window.__rangeStable || 0) + 1) >= 3;
		}
		window.__lastRange = k;
		window.__rangeStable = 0;
		return false;
	})()`
```

`chromedp.Run` の最後の Poll を `chromedp.Poll(rangeSettledJS, nil, chromedp.WithPollingTimeout(10*time.Second))` に変え、続く `allChunksPollErr` のブロック（`tileCount` / `wantTileCount` を取る箇所を含む）を削除する。`settledJS` による再描画の静定待ちはそのまま残す。

- [ ] **Step 4: 走らせて残りの失敗を潰す**

Run: `go test -tags browser ./internal/web/ -v`
Expected: PASS（全テスト）

落ちるものがあれば、**テストの期待値を実装に合わせて緩めるのではなく**、なぜ落ちるかを先に突き止める。位置が数px単位でずれるなら、それはタスク11で検証する「予測と実際の一致」が破れているということで、実装側の不具合である。

- [ ] **Step 5: コミット**

```bash
git add internal/web/browser_test.go
git commit -m "$(cat <<'MSG'
test: drive the browser tests from the layout the app publishes

テストが floor(i/cols)*rowH でレイアウトを写経していたのをやめ、実装が
公開する「通し番号 → y」を呼ぶ。写経していると実装と一緒に間違えても
気づけない。

静定判定も「貼り付けタイル数 == total - chunkSize」から「貼り付け範囲が
動かなくなったこと」へ変える。前者は貼り付け窓が最終行に届くことに依存し、
corpusやviewportを変えると無言でタイムアウトする脆さがあった。

TestGridAlignmentPastFirstChunk は削除する。守っていた「塊の先頭が行頭で
ないと横にずれる」不具合は、貼る単位が行になったことで構造的に起きない。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
MSG
)"
```

---

### Task 11: 日ごとの区切りをブラウザで検証する

**Files:**
- Modify: `internal/web/browser_test.go`（テストを追加）

**Interfaces:**
- Consumes: `famifo.current()`（タスク6）、`dayOfPhoto` / `dayStartIndex` / `testDayCounts`（タスク9）
- Produces: なし

- [ ] **Step 1: テストを書く**

`browser_test.go` の末尾に追加する。

```go
// レイアウトが計算した位置と、ブラウザが実際に置いた位置が一致すること。
//
// これが今回いちばん壊れやすく、しかも壊れても静かに壊れる（写真が微妙に
// 違う場所に出るだけで、例外も空白も出ない）。CSS Grid の自動配置が貪欲
// 詰めと同じ規則であるという前提そのものを、実測で確かめる。
func TestCardPositionsMatchTheLayout(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)

	var mismatches []string
	err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1600, 900),
		chromedp.Navigate(baseURL),
		waitForTiles(10*time.Second),
		chromedp.Evaluate(`(() => {
			const L = famifo.current();
			const r = famifo.pastedRange();
			const spacerTop = document.querySelector('#spacer').getBoundingClientRect().top;
			const bad = [];
			for (const card of document.querySelectorAll('.daycard')) {
				const first = card.querySelector('.tile');
				if (!first) continue;
				const i = Number(first.dataset.i);
				// このカードが属するエントリを通し番号から引く
				const e = L.entries.find((x) => i >= x.start && i < x.start + x.n);
				if (!e) { bad.push('entry not found for i=' + i); continue; }
				const wantY = famifo.yForIndex(L, i);
				const gotY = card.getBoundingClientRect().top - spacerTop
					+ (card.querySelector('.daylabel') ? L.labelH + L.gap : 0);
				if (Math.abs(gotY - wantY) > 1) {
					bad.push('i=' + i + ' y got=' + gotY.toFixed(1) + ' want=' + wantY.toFixed(1));
				}
				const wantW = e.span * L.tileH + (e.span - 1) * L.gap;
				const gotW = card.getBoundingClientRect().width;
				if (Math.abs(gotW - wantW) > 1) {
					bad.push('i=' + i + ' width got=' + gotW.toFixed(1) + ' want=' + wantW.toFixed(1));
				}
			}
			return bad;
		})()`, &mismatches),
	)
	require.NoError(t, err)
	require.Empty(t, mismatches,
		"JSの計算とブラウザの実配置がずれている（pastedRangeの範囲で検査）")
}

// 1行に収まる日が実際に横に並ぶこと。
func TestSmallDaysSitSideBySide(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)

	var sharedRows int
	err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1600, 900),
		chromedp.Navigate(baseURL),
		waitForTiles(10*time.Second),
		chromedp.Evaluate(`(() => {
			// 上端が同じカードが2枚以上あるストライプの数
			const tops = new Map();
			for (const c of document.querySelectorAll('.daycard')) {
				const t = Math.round(c.getBoundingClientRect().top);
				tops.set(t, (tops.get(t) || 0) + 1);
			}
			return [...tops.values()].filter((n) => n >= 2).length;
		})()`, &sharedRows),
	)
	require.NoError(t, err)
	require.Greater(t, sharedRows, 0,
		"1行に収まる日が横に並んでいない（corpusに1枚・数枚の日が入っているか確認すること）")
}

// 列数を超える日が行を占有し、ラベルがその日を指すこと。
func TestBigDayTakesWholeRowsAndIsLabelled(t *testing.T) {
	requireBrowser(t)
	ctx := newTab(t)

	// testDayCounts[0] は20枚。1600px(7列)では行を占有する。
	var res struct {
		Span  int    `json:"span"`
		Label string `json:"label"`
		Full  bool   `json:"full"`
	}
	err := chromedp.Run(ctx,
		chromedp.EmulateViewport(1600, 900),
		chromedp.Navigate(baseURL),
		waitForTiles(10*time.Second),
		chromedp.Evaluate(`(() => {
			const L = famifo.current();
			const card = document.querySelector('.daycard');
			const win = document.querySelector('#window');
			return {
				span: L.entries[0].span,
				label: card.querySelector('.daylabel').textContent,
				full: Math.abs(card.getBoundingClientRect().width
				               - win.getBoundingClientRect().width) < 1,
			};
		})()`, &res),
	)
	require.NoError(t, err)

	require.Equal(t, 7, res.Span, "1600pxは7列。20枚の日は全列を占めること")
	require.True(t, res.Full, "行を占有する日はグリッドの全幅であること")

	// ラベルはタイルの data-date から作る。corpus の先頭の日と一致すること。
	day := dayOfPhoto(0) // "2026-01-01"
	parts := strings.Split(day, "-")
	wantMonth, wantDay := strings.TrimLeft(parts[1], "0"), strings.TrimLeft(parts[2], "0")
	require.Contains(t, res.Label, wantMonth+"月"+wantDay+"日",
		"ラベルが先頭の写真の日付と一致しない")
}
```

`strings` を import に足す（未追加なら）。

- [ ] **Step 2: 走らせる**

Run: `go test -tags browser ./internal/web/ -run 'TestCardPositions|TestSmallDays|TestBigDay' -v`
Expected: PASS

- [ ] **Step 3: テストが本当に効いていることを確かめる**

`app.js` の `layout()` の `const h = labelH + gap + rows * tileH + (rows - 1) * gap;` を `... + rows * tileH;`（gapを落とす）に一時的に書き換え、`TestCardPositionsMatchTheLayout` が **FAIL する**ことを確認する。確認後、必ず元に戻す。

Run: `go test -tags browser ./internal/web/ -run TestCardPositionsMatchTheLayout -v`
Expected: FAIL（変異を入れた状態）→ 元に戻して PASS

- [ ] **Step 4: 全体を走らせる**

Run: `go test ./... && go test -tags browser ./internal/web/`
Expected: PASS

- [ ] **Step 5: コミット**

```bash
git add internal/web/browser_test.go
git commit -m "$(cat <<'MSG'
test: verify the drawn layout against the computed one

JSが計算した位置・幅と、ブラウザが実際に置いた位置・幅が一致することを
実測で押さえる。ここは壊れても例外も空白も出ず、写真が微妙に違う場所に
出るだけなので静かに壊れる。横並びと行占有も併せて検証する。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
MSG
)"
```

---

### Task 12: ドキュメントを更新する

**Files:**
- Modify: `docs/design.md`
- Modify: `README.md`

**Interfaces:**
- Consumes: なし
- Produces: なし

- [ ] **Step 1: docs/design.md の「表示」節を更新する**

「一覧構成」の箇条書きを次に置き換える。

```markdown
- **一覧構成**: フォルダ階層を無視した、フラットな1つのギャラリー
  - 全期間を移動できるよう、仮想スクロールと日付スクラバーを備える
  - 日ごとに区切って表示する。その日が占める列数は `min(枚数, 列数)` で、
    1行に収まる日は横に並ぶ（経緯は
    docs/superpowers/specs/2026-08-25-day-sections-design.md）
```

- [ ] **Step 2: docs/design.md の「バックエンド/フロントエンド境界」を更新する**

該当の箇条書きの末尾に次を足す。

```markdown
  - ただし日ごとのカードと日付ラベルの markup はクライアント側が持つ。
    どの日が横に並ぶかは列数（画面幅）で決まり、サーバーはレイアウト済みの
    HTMLを返せないため。列数をサーバーに送る案は、レイアウト規則がGoとJSに
    二重実装されるので採らなかった（経緯は
    docs/superpowers/specs/2026-08-25-day-sections-design.md）
```

- [ ] **Step 3: README.md の「一覧の操作」を更新する**

箇条書きの先頭に次を足す。

```markdown
- 写真は撮影日ごとに区切って並ぶ。1行に収まる日は横に並ぶ
```

- [ ] **Step 4: README.md のブラウザテストの説明を更新する**

「`internal/web/static/app.js`（仮想スクロール・ライトボックス・日付スクラバー）を」の行を次に変える。

```markdown
`internal/web/static/app.js`（仮想スクロール・日ごとのレイアウト計算・
ライトボックス・日付スクラバー）を、
```

- [ ] **Step 5: 確認する**

Run: `go test ./... && go test -tags browser ./internal/web/ && CGO_ENABLED=0 go build -o famifo-proto .`
Expected: すべて PASS

- [ ] **Step 6: コミット**

```bash
git add docs/design.md README.md
git commit -m "$(cat <<'MSG'
docs: record the day-grouped gallery and its boundary change

日ごとの区切りと、それに伴う「カードとラベルの markup はクライアント側が
持つ」という境界の変更を design.md に残す。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
MSG
)"
```
