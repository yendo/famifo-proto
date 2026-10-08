// @ts-check

// 日付スクラバー。ドラッグで全期間の任意の位置へ飛ぶ。

import { gallery } from "./gallery.js";
import { dayAtY } from "./layout.js";

(() => {
	/** @type {HTMLElement | null} */
	const bar = document.querySelector("#scrubber");
	if (!bar || !gallery || gallery.total === 0) return;

	/** @type {HTMLElement} */
	const thumb = bar.querySelector(".scrub-thumb");
	/** @type {HTMLElement} */
	const label = bar.querySelector(".scrub-label");

	let dragging = false;
	let hideTimer = 0;

	bar.hidden = false;

	function show() {
		bar.classList.add("visible");
		clearTimeout(hideTimer);
		if (!dragging) {
			hideTimer = setTimeout(() => bar.classList.remove("visible"), 1500);
		}
	}

	// スクロール位置からつまみの位置を更新する
	function sync() {
		const frac = gallery.scroller.scrollTop / gallery.maxScroll();
		const top = frac * (bar.clientHeight - thumb.offsetHeight);
		thumb.style.top = `${top}px`;
		show();
	}

	/** @param {number} clientY */
	function seek(clientY) {
		const rect = bar.getBoundingClientRect();
		const frac = Math.min(1, Math.max(0, (clientY - rect.top) / rect.height));
		const y = frac * gallery.maxScroll();
		gallery.scroller.scrollTop = y;

		// 行の高さが日ごとに違うため、割合×総枚数では位置を求められない。
		// スクロール位置そのものからレイアウトを引く。
		// 横に並んだ日は同じyを共有するため、dayAtY はその行の最後の
		// エントリ（並びが新しい順なので一番古い日）を返す。表示は月なので、
		// 1つの行が月をまたぐときにしか差は出ない。承知のうえで許容する。
		const L = gallery.current();
		const d = L ? dayAtY(L, gallery.toLayoutY(y)) : "";
		if (d) {
			// ドラッグは17年ぶんを一気に動かすので、日まで出すとちらつく。月で止める。
			const [yy, mm] = d.split("-");
			label.textContent = `${yy}年${Number(mm)}月`;
			label.hidden = false;
			label.style.top = `${Math.min(rect.height - 24, Math.max(0, clientY - rect.top - 12))}px`;
		}
	}

	/** @param {number} clientY */
	function startDrag(clientY) {
		dragging = true;
		bar.classList.add("visible");
		clearTimeout(hideTimer);
		seek(clientY);
	}

	function endDrag() {
		dragging = false;
		label.hidden = true;
		show();
	}

	bar.addEventListener("pointerdown", (e) => {
		bar.setPointerCapture(e.pointerId);
		startDrag(e.clientY);
	});
	bar.addEventListener("pointermove", (e) => {
		if (dragging) seek(e.clientY);
	});
	bar.addEventListener("pointerup", endDrag);
	bar.addEventListener("pointercancel", endDrag);

	window.addEventListener("scroll", sync, { passive: true });
	sync();
})();
