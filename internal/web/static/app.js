// 仮想スクロール。全枚数分の高さを #spacer が持ち、可視範囲だけを
// #window に展開する。無限スクロールと違い、任意の位置へ即座に飛べる。
const famifo = (() => {
  const gallery = document.querySelector('#gallery');
  const spacer = document.querySelector('#spacer');
  const win = document.querySelector('#window');
  const probe = document.querySelector('#colprobe');
  if (!gallery || !spacer || !win || !probe) return null;

  const total = Number(gallery.dataset.total || 0);
  const chunkSize = Number(gallery.dataset.chunk || 60);
  const OVERSCAN_ROWS = 4; // 可視範囲の上下に余分に描く行数

  // スクロールしているのは #gallery ではなく文書全体。
  // ライトボックスの body.locked { overflow: hidden } もこれを前提にしている。
  const scroller = document.scrollingElement || document.documentElement;
  const maxScroll = () => Math.max(1, scroller.scrollHeight - window.innerHeight);

  // 塊番号 -> { html, urls }
  const chunks = new Map();
  const inFlight = new Map();

  let L = null;
  let spacerTop = 0; // #spacer の文書上のオフセット。レイアウト座標との差
  let pasted = { from: 0, to: 0 };
  let renderedKey = ''; // 「どの塊を何個貼ったか」。同じなら描き直さない

  // サーバが返したHTML断片を、タイル1枚ずつに割る。取得時に1回だけパースし、
  // 以降はここから必要な範囲を切り出して組み立てる。data-full 属性がURL、
  // data-date 属性が日付。
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

  // レイアウトのy座標は #spacer の上端が0。文書のスクロール位置とは、
  // 上部バー（sticky でも流れの中で場所を占める）とギャラリーの余白の
  // ぶんだけずれる。変換はこの2つに集約する。
  function toLayoutY(docY) { return docY - spacerTop; }
  function toDocY(layoutY) { return layoutY + spacerTop; }

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

  // 日ごとの表は初回HTMLに埋め込まれている。これが無いと1枚も描けない。
  const daysEl = document.querySelector('#daygroups');
  const days = daysEl ? JSON.parse(daysEl.textContent) : [];

  // 列数・列幅・gap はCSSの計算結果から読む。auto-fill の計算を自前で再現すると
  // CSSのbreakpointと二重管理になる。ラベル高も定義はCSS側の1箇所だけ。
  //
  // #window ではなく #colprobe（子を持たない空のグリッド）から測る。#window
  // には直前のレイアウトのカードが残っており、そのカードの grid-column:
  // span N が実際に収まる数を超えていると、CSS Grid はそれを収めるために
  // 暗黙トラックを追加してトラック列を押し広げる。#window はそれを常に
  // フルに収める側（span を測った cols に合わせて描く側）なので、狭めた
  // 直後は「一番大きい span」に押し上げられた列数を読んでしまい、本当の
  // 列数（=このビューポート幅に自然に収まる数）より多く出る。中身が無い
  // #colprobe ならその影響を受けない。
  function measure() {
    const cs = getComputedStyle(probe);
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
    // レイアウトのy座標は #spacer の上端が0。文書のスクロール位置とは、
    // 上部バー（sticky でも流れの中で場所を占める）とギャラリーの余白の
    // ぶんだけずれる。その差をここで1回だけ測る。
    spacerTop = spacer.getBoundingClientRect().top + scroller.scrollTop;
  }

  async function fetchChunk(ci) {
    if (chunks.has(ci)) return chunks.get(ci);
    if (inFlight.has(ci)) return inFlight.get(ci);

    const job = (async () => {
      const res = await fetch(`/items?offset=${ci * chunkSize}&limit=${chunkSize}`);
      if (!res.ok) throw new Error(`items ${res.status}`);
      const html = await res.text();
      const tiles = parseTiles(html);
      chunks.set(ci, tiles);
      return tiles;
    })().finally(() => inFlight.delete(ci));

    inFlight.set(ci, job);
    return job;
  }

  // 全体の通し番号から写真のURLを引く。未取得なら取りに行く。
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

  function ensureChunk(i) {
    if (i < 0 || i >= total) return;
    fetchChunk(Math.floor(i / chunkSize)).catch(() => {});
  }

  function render() {
    if (!L || L.height <= 0 || total === 0) return;

    const over = OVERSCAN_ROWS * (L.tileH + L.gap);
    const top = toLayoutY(scroller.scrollTop);
    const w = visibleWindow(L, top - over, top + window.innerHeight + over);
    if (!w) return;

    // 日ごとの表と総枚数はサーバが別々に読むため、開いたまま新着が入ると
    // 表のほうが多くなりうる。総枚数で抑えないと、存在しない塊を待ち続けて
    // 末尾が永久に更新されなくなる。
    const from = w.from;
    const to = Math.min(total, w.to);
    if (from >= to) return;

    const firstChunk = Math.floor(from / chunkSize);
    const lastChunk = Math.floor((to - 1) / chunkSize);

    // 可視範囲の前後1塊も先読みしておく。切り出す範囲は広げない。
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
    const key = `${from}:${to}:${L.cols}`;
    if (key === renderedKey) return;

    // 先に組み立て、1枚でも欠けていたら renderedKey を据え置いたまま抜ける。
    // 確定を先にすると、欠けたまま貼った状態がキャッシュされて直らない。
    const parts = [];
    for (const p of w.pieces) {
      const pFrom = p.e.start + p.r0 * p.e.span;
      const pTo = Math.min(to, p.e.start + p.e.n, p.e.start + (p.r1 + 1) * p.e.span);
      if (pFrom >= pTo) continue;
      const html = cardHTML(p, pFrom, pTo);
      if (!html) return;
      parts.push(html);
    }
    if (parts.length === 0) return;

    renderedKey = key;
    pasted = { from, to };
    win.innerHTML = parts.join('');
    win.style.transform = `translateY(${w.pasteY}px)`;

    // 各タイルに通し番号を書く。切り出す範囲は連続しているのでDOM順と一致する。
    const tiles = win.querySelectorAll('.tile');
    for (let k = 0; k < tiles.length; k++) tiles[k].dataset.i = from + k;
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
  // 最狭の1列(CSSの最小値110px)に収めるため、これ以上長い表記にはしない。
  function formatDay(d) {
    if (!d) return '';
    const [y, m, day] = d.split('-');
    const head = Number(y) === new Date().getFullYear() ? '' : `${y}年`;
    return `${head}${Number(m)}月${Number(day)}日`;
  }

  function onResize() {
    // 回転やリサイズで列数が変わるとレイアウト全体の高さが変わるため、
    // scrollTop をそのまま残すと別の写真の位置に飛ぶ。いま先頭に見えていた
    // 写真の通し番号を保持して復元する。
    //
    // アンカーに pasted.from は使わない。あれは OVERSCAN のぶん画面外まで
    // 含んだ範囲の先頭なので、復元すると毎回4行ぶん手前に着地する。
    // オーバースキャン抜きの、いま実際に画面上端にある写真を取る。
    const prev = L;
    const prevTop = spacerTop; // measure() で測り直される前の値
    // 高さ0の窓で問い合わせると、ストライプ間の隙間(gap)にちょうど当たった
    // ときに空振りして null が返り、アンカーが先頭に落ちる。1行ぶんの高さを
    // 持たせて、必ずどこかのストライプに当てる。隙間に当たった場合は次の
    // ストライプが返るが、そこが実際に最初に見える内容なので正しい。
    const anchorY = scroller.scrollTop - prevTop;
    const at = prev
      ? visibleWindow(prev, anchorY, anchorY + prev.tileH + prev.gap)
      : null;
    const topIndex = at ? at.from : 0;

    measure();

    // ResizeObserver は #window 自身の高さの変化でも発火する。貼り付ける量は
    // スクロール中に増減するため、通常のスクロールでも呼ばれる。実際に列数も
    // タイル高も変わっていないなら、貼り直しも位置の復元も不要。
    if (prev && L && prev.cols === L.cols && prev.tileH === L.tileH) {
      return;
    }

    renderedKey = ''; // 列数が変われば貼り直しが必要
    if (L && L.height > 0) {
      // yForIndex が返すのはレイアウト座標。scrollTop は文書座標なので戻す。
      scroller.scrollTop = toDocY(yForIndex(L, topIndex));
    }
    render();
  }

  seedFirstChunk();
  measure();
  render();
  window.addEventListener('scroll', render, { passive: true });
  window.addEventListener('resize', onResize);
  // スクロールバーの出現で #window の幅が変わっても resize は発火しない。
  // 要素そのものを監視して、列数とタイル高を測り直す。
  new ResizeObserver(onResize).observe(win);

  return {
    total,
    chunkSize,
    urlAt,
    ensureChunk,
    scroller,
    maxScroll,
    render,
    current: () => L,
    pastedRange: () => pasted,
    layout,
    yForIndex,
    dayAtY,
    visibleWindow,
    toLayoutY,
    toDocY,
  };
})();

// ライトボックス。仮想スクロールではDOM上に可視範囲のタイルしか無いため、
// 全体の通し番号で動かす。そうしないと窓枠の端でスワイプが止まる。
(() => {
  const box = document.querySelector('#lightbox');
  if (!box || !famifo) return;

  const img = box.querySelector('img');
  const SWIPE_X = 50; // 左右送りとみなす最小移動量(px)
  const SWIPE_Y = 80; // 下スワイプで閉じる最小移動量(px)

  let idx = -1;
  let requestSeq = 0; // 連続スワイプで古いurlAtの解決が新しいものを上書きしないための世代番号

  async function show(i) {
    if (i < 0 || i >= famifo.total) return;
    const mySeq = ++requestSeq;
    const url = await famifo.urlAt(i);
    if (!url || mySeq !== requestSeq) return; // 待っている間に追い越されたら破棄
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
    idx = -1;
    requestSeq++; // 閉じた後に届く古い解決を破棄する
  }

  document.addEventListener('click', (e) => {
    const tile = e.target.closest('#window .tile');
    if (!tile) return;
    e.preventDefault();
    const i = Number(tile.dataset.i);
    if (!Number.isInteger(i)) return; // 通し番号が無いタイルは無視する。
                                      // NaN は i < 0 も i >= total も満たさず、
                                      // offset=NaN のリクエストまで素通りする
    open(i).catch(() => {});
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

// 日付スクラバー。ドラッグで全期間の任意の位置へ飛ぶ。
(() => {
  const bar = document.querySelector('#scrubber');
  if (!bar || !famifo || famifo.total === 0) return;

  const thumb = bar.querySelector('.scrub-thumb');
  const label = bar.querySelector('.scrub-label');

  let dragging = false;
  let hideTimer = 0;

  bar.hidden = false;

  function show() {
    bar.classList.add('visible');
    clearTimeout(hideTimer);
    if (!dragging) {
      hideTimer = setTimeout(() => bar.classList.remove('visible'), 1500);
    }
  }

  // スクロール位置からつまみの位置を更新する
  function sync() {
    const frac = famifo.scroller.scrollTop / famifo.maxScroll();
    const top = frac * (bar.clientHeight - thumb.offsetHeight);
    thumb.style.top = `${top}px`;
    show();
  }

  function seek(clientY) {
    const rect = bar.getBoundingClientRect();
    const frac = Math.min(1, Math.max(0, (clientY - rect.top) / rect.height));
    const y = frac * famifo.maxScroll();
    famifo.scroller.scrollTop = y;

    // 行の高さが日ごとに違うため、割合×総枚数では位置を求められない。
    // スクロール位置そのものからレイアウトを引く。
    // 横に並んだ日は同じyを共有するため、dayAtY はその行の最後の
    // エントリ（並びが新しい順なので一番古い日）を返す。表示は月なので、
    // 1つの行が月をまたぐときにしか差は出ない。承知のうえで許容する。
    const L = famifo.current();
    const d = L ? famifo.dayAtY(L, famifo.toLayoutY(y)) : '';
    if (d) {
      // ドラッグは17年ぶんを一気に動かすので、日まで出すとちらつく。月で止める。
      const [yy, mm] = d.split('-');
      label.textContent = `${yy}年${Number(mm)}月`;
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

  window.addEventListener('scroll', sync, { passive: true });
  sync();
})();
