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
