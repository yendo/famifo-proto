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

  // スクロールしているのは #gallery ではなく文書全体。
  // ライトボックスの body.locked { overflow: hidden } もこれを前提にしている。
  const scroller = document.scrollingElement || document.documentElement;
  const maxScroll = () => Math.max(1, scroller.scrollHeight - window.innerHeight);

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
      renderedKey = '0:1';
    }
  }

  // 列数とタイル高をブラウザの計算結果から読む。
  // auto-fill の計算を自前で再現すると CSS の breakpoint と二重管理になる。
  function measure() {
    const cs = getComputedStyle(win);
    const tracks = cs.gridTemplateColumns.split(' ').filter((t) => t.length > 0);
    cols = Math.max(1, tracks.length);
    const tileW = parseFloat(tracks[0]);
    if (!(tileW > 0)) {
      rowH = 0; // スタイル未適用。次の resize/scroll で測り直す
      return;
    }
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

    const viewRows = Math.ceil(window.innerHeight / rowH);
    const firstRow = Math.max(0, Math.floor(scroller.scrollTop / rowH) - OVERSCAN_ROWS);
    const lastRow = Math.min(Math.ceil(total / cols) - 1, firstRow + viewRows + OVERSCAN_ROWS * 2);

    const from = firstRow * cols;
    const to = Math.min(total, (lastRow + 1) * cols);
    const firstChunk = Math.floor(from / chunkSize);
    const lastChunk = Math.floor((to - 1) / chunkSize);

    // 可視範囲の前後1塊も先読みしておく。貼り付ける範囲は広げない。
    const fetchFrom = Math.max(0, firstChunk - 1);
    const fetchTo = Math.min(Math.floor((total - 1) / chunkSize), lastChunk + 1);
    for (let ci = fetchFrom; ci <= fetchTo; ci++) {
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

    // グリッドは貼り付けた最初のタイルを列0に置くため、塊の先頭が行頭でないと
    // 横方向にずれる。開始列を明示して以降の自動配置をそこから流す。
    const firstTile = win.firstElementChild;
    if (firstTile) {
      firstTile.style.gridColumnStart = (pastedFrom % cols) + 1;
    }
  }

  function onResize() {
    // 回転やリサイズで列数が変わると spacer の高さが変わるため、
    // scrollTop をそのまま残すと別の写真の位置に飛ぶ。
    // 先頭に見えていた写真の通し番号を保持して復元する。
    const topIndex = rowH > 0 ? Math.floor(scroller.scrollTop / rowH) * cols : 0;
    const prevCols = cols;
    const prevRowH = rowH;

    measure();

    // ResizeObserver は #window 自身の高さの変化でも発火する。
    // 貼り付ける塊の数はスクロール中に増減するため、通常のスクロールでも呼ばれる。
    // 実際に列数もタイル高も変わっていないなら、貼り直しも位置の復元も不要。
    if (cols === prevCols && rowH === prevRowH) {
      return;
    }

    renderedKey = ''; // 列数が変われば貼り直しが必要
    if (rowH > 0 && cols > 0) {
      scroller.scrollTop = Math.floor(topIndex / cols) * rowH;
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
    pastedIndex: () => pastedFrom, // 貼り付け先頭の通し番号。Task 8 が使う
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
    // 窓枠内で何番目か + 貼り付けの先頭 = 全体の通し番号
    const within = [...tile.parentElement.querySelectorAll('.tile')].indexOf(tile);
    open(famifo.pastedIndex() + within).catch(() => {});
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
    famifo.scroller.scrollTop = frac * famifo.maxScroll();

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

  window.addEventListener('scroll', sync, { passive: true });
  sync();
})();
