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

    // グリッドは貼り付けた最初のタイルを列0に置くため、塊の先頭が行頭でないと
    // 横方向にずれる。開始列を明示して以降の自動配置をそこから流す。
    const firstTile = win.firstElementChild;
    if (firstTile) {
      firstTile.style.gridColumnStart = (pastedFrom % cols) + 1;
    }
  }

  function onResize() {
    renderedKey = ''; // 列数が変われば貼り直しが必要
    measure();
    render();
  }

  seedFirstChunk();
  measure();
  render();
  window.addEventListener('scroll', render, { passive: true });
  window.addEventListener('resize', onResize);

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

// ギャラリーのライトボックス。htmxが後から差し込むタイルにも効くよう、
// クリックはdocumentへの委譲で捕まえる。
(() => {
  const box = document.querySelector('#lightbox');
  if (!box) return;

  const img = box.querySelector('img');
  const SWIPE_X = 50;  // 左右送りとみなす最小移動量(px)
  const SWIPE_Y = 80;  // 下スワイプで閉じる最小移動量(px)

  let urls = [];
  let idx = -1;

  const tiles = () => Array.from(document.querySelectorAll('#gallery .tile[data-full]'));

  function open(i) {
    urls = tiles().map((a) => a.dataset.full);
    if (i < 0 || i >= urls.length) return;
    idx = i;
    img.src = urls[idx];
    box.hidden = false;
    document.body.classList.add('locked');
  }

  function close() {
    box.hidden = true;
    img.removeAttribute('src');
    document.body.classList.remove('locked');
  }

  function step(delta) {
    const next = idx + delta;
    if (next < 0 || next >= urls.length) return;
    idx = next;
    img.src = urls[idx];
  }

  document.addEventListener('click', (e) => {
    const tile = e.target.closest('#gallery .tile');
    if (!tile) return;
    e.preventDefault();
    open(tiles().indexOf(tile));
  });

  box.addEventListener('click', (e) => {
    if (e.target.closest('.lb-prev')) { step(-1); return; }
    if (e.target.closest('.lb-next')) { step(1); return; }
    close();
  });

  document.addEventListener('keydown', (e) => {
    if (box.hidden) return;
    if (e.key === 'Escape') close();
    else if (e.key === 'ArrowRight') step(1);
    else if (e.key === 'ArrowLeft') step(-1);
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
      step(dx < 0 ? 1 : -1);
    } else if (dy > SWIPE_Y && Math.abs(dy) > Math.abs(dx)) {
      close();
    }
  }, { passive: true });
})();
