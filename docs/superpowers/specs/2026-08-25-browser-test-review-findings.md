# ブラウザテストハーネス レビュー所見

`02119d9 test: verify app.js in a containerized headless browser` で追加した
`internal/web/browser_test.go`（8テスト）を、書いた本人以外のレビュアーが
ミューテーション検証（app.js / app.css に意図的な退行を注入し、テストが
FAIL するかを実測する）で点検した結果。

## 結論

**8本のうち3本は、守っているはずのバグを注入しても FAIL しない。**
加えて、Docker が無い環境では同パッケージの非ブラウザテスト約25本ごと
実行されずに `ok` と表示される。

## 検証済みの事実

以下はすべて実際に実行して確認した（推測ではない）。

| # | 注入した退行 | 対象テスト | 結果 |
|---|---|---|---|
| A | `app.js` 全体を無効化（仮想スクロール完全停止） | TestInitialRenderFillsViewport | **PASS のまま** |
| B | `onResize` の no-change ガードを削除 | TestNoRepaintOnPlainScroll | **PASS のまま** |
| C | `translateY` を常に 0（画面が空白になる全面崩壊） | TestScrollPositionSurvivesReload | **PASS のまま** |
| D | 2塊目以降で1枚ずれた写真を返す（off-by-one） | TestLightboxCrossesChunkBoundary | **PASS のまま** |
| E | `.scrubber` の帯幅を 32px→8px（バグは残したまま） | TestTileTapOpensLightbox | **PASS のまま** |
| F | サムネイル URL を全て 404 に | 8本すべて | **8/8 PASS** |
| G | 原寸 URL を全て 404 に | ライトボックス系2本 | **2/2 PASS** |
| H | `docker` を PATH から外す | 同パッケージ全33本 | `=== RUN` ゼロで **EXIT=0** |

歯があることを確認できた注入（正しく FAIL した）:

- `gridColumnStart` の削除／off-by-one → TestGridAlignmentPastFirstChunk
- `onResize` の `scrollTop` 復元を削除 → TestScrollAnchoredOnResize
- スクラバー seek の frac を両端 30% 圧縮 → TestScrubberReachesBothEnds
- `.scrubber { pointer-events: auto }`（元のバグ） → TestTileTapOpensLightbox
- spacer 高さ確定を 900ms 遅延 → TestScrollPositionSurvivesReload の1つ目のアサーション

## 個別の要件

### F1. Docker 不在時に全パッケージが静かに消える（深刻度: 高）

`TestMain` が `m.Run()` を呼ばずに `os.Exit(0)` する。`-tags browser` を付けて
Docker 不在の環境で走らせると、ブラウザテスト8本だけでなく
`gallery_test.go`(13) / `handlers_test.go`(7) / `static_test.go`(5) の
**計25本も実行されず**、終了コード 0 になる。CI は緑になる。

`--network host` は Docker Desktop（mac/Windows）では既定で
`127.0.0.1:9222` に届かないため、そうした環境では恒常的にこの状態に入る。

**要件:** 環境判定を各テスト冒頭に移し、非ブラウザテストは常に実行されること。
CI 用に、スキップを失敗として扱う切り替えを持つこと。

### F2. TestInitialRenderFillsViewport が JS に依存していない（深刻度: 高）

サーバが最初の塊 60 枚を `#window` の中に直接描画して返すため
（`gallery.html` の `{{template "items" .}}`）、1600×2000 では
7列×9行 ≒ 2077px となり、**JS が一切動かなくても** viewportH=2000 を超える。
このテストが測っているのはサーバ側テンプレートの出力だけ。

しかも余裕は 77px（3.8%）しかなく、`testPageSize` を下げる・ビューポート高を
変える・CSS の breakpoint を触るだけで、正しい実装のまま FAIL に転ぶ。
「常に通る」と「すぐ壊れる」を同時に満たしている。

**要件:** JS が動いたことに依存する量を検査すること。マージンは行高から導出すること。

### F3. TestNoRepaintOnPlainScroll が守っている当のバグを検出しない（深刻度: 高）

退行は実在して測定可能（再描画 3回 → 7回）だが、テストの観測点がそれを跨いで
いない。印付けは塊境界の跨ぎが完全に落ち着いた後に行われ、その後の
`scrollTop += 40`（1行 226px に対して 40px）は貼り付け範囲を変えないため
`#window` の高さが変わらず、ResizeObserver のフィードバックループの入口に
届かない。3つの `chromedp.Sleep`（300/500/300ms）は
**何も起きないことを待っているだけ**。

**要件:** 「印が残るか」ではなく、貼り替えの発生回数を数えること。
固定 Sleep ではなく条件でポーリングすること。

### F4. TestScrollPositionSurvivesReload の2つ目のアサーションが弱い（深刻度: 中）

`intersecting > 0` という閾値のため、全面崩壊しても端に食い込んだ4枚で通る。
1600×900・7列なら本来 35枚前後が交差する。

さらに:
- 末尾の `require.Greater(t, intersecting, 0, ...)` は**到達不能**。
  直前の `chromedp.Poll(intersectCountJS+" > 0")` が同じ条件を保証するため、
  失敗時は必ず Poll 側（`timeout`、メッセージ無し）で止まる。
- `Math.abs(scrollTop - before) < 200` の 200 は1行（226px）に近く、
  1行分のずれを見逃す。

**要件:** 交差枚数の下限を列数から導出すること。許容差を行高から導出すること。
到達不能なアサーションを残さないこと。

### F5. TestLightboxCrossesChunkBoundary が写真の同一性を見ていない（深刻度: 中）

`src !== prev` しか検査しないため、境界の継ぎ目で1枚ずれる off-by-one を
検出しない。`require.GreaterOrEqual(len(distinct), 20)` の 20 は steps=130 と
無関係の魔法の数字で、A,B,A,B... の2循環でも 130 回進める。
`require.NotEmpty(t, firstSrc)` だけで、最初に開いたのが写真0番かを見ていない。

**要件:** 期待される写真の並びと**完全一致**で突き合わせること。

### F6. TestTileTapOpensLightbox の座標がハードコード（深刻度: 中）

`x := r.Right - r.Width*0.1` はタイル基準で、スクラバー帯の位置と無関係。
実測の余裕は 8.9px。バグを残したまま帯幅を 8px にすると、タップ点が帯を
外れて PASS した（fail も skip もせず、静かに無意味化する）。

**要件:** タップ点をスクラバー矩形とタイル矩形の**重なり**から導出すること。
重なりが空なら前提条件不成立として停止すること。

### F7. 画像が表示されているかを誰も見ていない（深刻度: 中）

レイアウトは CSS の `aspect-ratio: 1` で決まるため、写真が1枚も表示されない
状態が全テストを通過する（上表 F・G）。

**要件:** サムネイルと原寸の両方について、実際にデコードされたことを
`naturalWidth > 0` で確認すること。

### F8. 恒真式と魔法の数字（深刻度: 低）

- `require.Equal(t, res.Cols, res.DistinctLefts, "横方向のずれ")` は**恒真式**。
  バグ注入下でも `distinctLefts=7, cols=7` のままだった（実測）。CSS Grid は
  `gridColumnStart` に関係なく必ず cols 本のトラック上に置くため、タイルが
  cols 枚以上あれば常に成立する。検出力ゼロ。
- 導出すべき定数: `scrollToPhotoJS(150)`、`< 200`、`len(distinct) >= 20`、
  `scrollTop += 40`。
- `TestScrubberReachesBothEnds` は `drag()` 直後に待たずに `scrollTop` を
  読んでおり、seek が rAF スロットルされた瞬間に flaky 化する。
- コンテナ名とポート 9222 が固定で、起動時に無条件で `docker rm -f` するため、
  同一マシンでの並行実行は互いのコンテナを殺す。

## スコープ外（今回は直さない）

- スクラバーの月ラベル（`monthAt` の二分探索、`/dates` 連携）の検証
- タッチスワイプ、Escape での閉じ、`.lb-prev`/`.lb-next`、`body.locked`
- コンテナ名／ポートの動的化（F8末尾）

これらは新規テストの追加であって、既存テストの偽陽性を潰す作業ではない。
別途スコープを切る。

## テストデータについて

問題なし。200枚 / chunkSize 60 で塊は4つ（60/60/60/20）、境界を3回跨げる。
撮影日は 2025-06-16〜2026-01-01 の1日刻みで8ヶ月にまたがる。
生成が決定的でユーザーの実ライブラリに依存しない設計も妥当。

## 修正後の再検証（2026-08-25）

Task 1〜7 の修正を入れた `dcf9a19` に対して、上表 A〜H の注入を1件ずつ流し直した。
各件は「注入 → `CGO_ENABLED=0 go test -tags browser ./internal/web/ -count=1 -v` →
`git checkout -- internal/web/static internal/web/templates` で復元 →
`git diff --stat -- internal/web` が空であることを確認」の手順を守っている。

基準（無傷）: `--- PASS` 33本、EXIT=0。

| # | 注入内容 | 対象テスト | 結果 | 落ちたアサーションとメッセージの要約 |
|---|---|---|---|---|
| A | `app.js` 全体を無効化（先頭 `if (true) { } else {` / 末尾 `}`） | TestInitialRenderFillsViewport | **FAIL（期待どおり）** | `browser_test.go:506` タイル枚数の Poll。「#windowのタイルが60枚のまま増えない。サーバが埋めた最初の1塊(60枚)のままで、仮想スクロールが追加の塊を貼っていない（app.jsが動いていない疑い）」 |
| B | `onResize` の no-change ガード3行を削除 | TestNoRepaintOnPlainScroll | **FAIL（期待どおり）** | `browser_test.go:820` `require.Equal`。「1行分のスクロールで、貼り付ける内容が変わっていないのにDOMが貼り替えられた: スクロール前=4 後=6 期待=5（貼り付け先頭 60→120）。onResize の…ガードが効いていない疑い」 |
| C | `render()` の `translateY(…)` を `translateY(0px)` 固定に | TestScrollPositionSurvivesReload | **FAIL（期待どおり）** | `browser_test.go:640` 交差枚数の Poll。「リロード後、可視範囲と交差するタイルが4枚しかない（14枚=7列×2行を期待）。窓枠の位置合わせ(translateY)が効いていない疑い」 |
| D | `urlAt` が2塊目以降で1枚ずれた URL を返す | TestLightboxCrossesChunkBoundary | **FAIL（期待どおり）** | `browser_test.go:909` `require.Equal`。「60枚目の写真が期待と違う（塊の境界=60での継ぎ目のずれの疑い）: got=/photo/73cd1b7d… want=/photo/b8ca00ab…」 |
| E | `.scrubber` から `pointer-events: none` を削除し、かつ `width: 32px`→`8px` | TestTileTapOpensLightbox | **FAIL（期待どおり）** | `browser_test.go:1106` ライトボックス表示の Poll。「スクラバー帯と重なる位置(x=779, y=144)のタイルをタップしてもライトボックスが開かなかった（スクラバーに奪われている疑い）」。前段の `browser_test.go:1095` が「重なり幅=4.0px 重なり高さ=191.2px（タイル右端781, 帯左端777）」を出力しており、帯を細くしてもタップ点が重なりの中に入ることを確認できる |
| F | `items.html` の `src="{{.ThumbURL}}"` を `/thumb/does-not-exist` に | TestInitialRenderFillsViewport | **FAIL（期待どおり）** | `browser_test.go:524` デコード枚数の Poll。「可視範囲のサムネイルが1行分(7枚)もデコードされなかった（画像が表示されていない）」 |
| G | `items.html` の `data-full="{{.FullURL}}"` を `/photo/does-not-exist` に | TestLightboxCrossesChunkBoundary | **FAIL（期待どおり）** | `browser_test.go:886` 原寸デコードの Poll。「ライトボックスの原寸画像がデコードされなかった（画像が表示されていない）」 |
| H | `docker` を PATH から外す（`PATH=/nonexistent` でビルド済みバイナリを実行） | 同パッケージ全33本 | **期待どおり** | 非ブラウザ25本が PASS、ブラウザ8本が SKIP、EXIT=0。SKIP 理由は「ブラウザテスト環境が無いためスキップします: dockerが見つかりません: exec: "docker": executable file not found in $PATH」。`FAMIFO_BROWSER_TESTS=required` を足すと同じ8本が FAIL し EXIT=1（「ブラウザテスト環境を用意できませんでした（FAMIFO_BROWSER_TESTS=required のためスキップせず失敗させます）: …」） |

**8件すべてが期待どおり。**`-count=3` での再確認が要る件（期待どおりに落ちなかった件）は無かった。

### 巻き添えで落ちたテスト（事実として記録）

退行の影響範囲が対象テストより広いこと自体は問題ではないが、実測値を残す。

| # | PASS本数 | FAIL したテスト |
|---|---|---|
| A | 25 | ブラウザ8本すべて（対象外の7本は `ReferenceError: famifo is not defined` などで停止） |
| B | 32 | TestNoRepaintOnPlainScroll のみ |
| C | 31 | TestScrollPositionSurvivesReload、TestScrollAnchoredOnResize |
| D | 32 | TestLightboxCrossesChunkBoundary のみ |
| E | 32 | TestTileTapOpensLightbox のみ |
| F | 29 | TestInitialRenderFillsViewport、TestScrollPositionSurvivesReload、TestGalleryRendersTiles、TestGalleryUsesOriginalAsThumbForHEIC |
| G | 31 | TestLightboxCrossesChunkBoundary、TestGalleryRendersTiles |

### 診断情報の質

`waiting for function failed: timeout` や `context deadline exceeded` **だけ**で終わった件はゼロ。
A・C・E・F・G は `chromedp.Poll` の素のエラーが `timeout` だが、いずれも
`require.NoError` の `Messages:` に日本語の説明が付いており、どのアサーションで
何が期待外れだったかが読める。ただし実測値の出方に差がある。

- 実測値まで出るもの: A（60枚のまま）、B（4→6、期待5）、C（4枚、14枚を期待）、D（got/want の URL）、E（タップ座標と重なり矩形）
- 期待値のみで実測値が出ないもの: F・G のデコード枚数（「7枚もデコードされなかった」「デコードされなかった」で、実際に何枚だったかは出ない）

F・G は退行の切り分けには足りるが、部分的にしかデコードされない中間状態が起きたときに
枚数が分からない。深刻ではないので今回は直さない。

### 最終確認の実測値（2026-08-25、HEAD=dcf9a19）

```
CGO_ENABLED=0 go test ./...                                  → 全パッケージ ok
CGO_ENABLED=0 go test -tags browser ./internal/web/ -count=1 -v
  | grep -cE '^--- PASS'                                     → 33（非ブラウザ25＋ブラウザ8）、EXIT=0
CGO_ENABLED=0 go vet ./... && gofmt -l .                     → 出力なし
git diff --stat -- internal/web                              → 空（製品コードは無改変）
git status --short                                           → 未追跡のドキュメントと .claude/ のみ
docker ps -a --filter name=famifo-browser-test-chrome        → 残骸なし
```
