// @ts-check

// 絞り込みのパネル。チェックを変えたらフォームを送る。狭い画面ではシートとして
// 開け閉めし、開いている間の変更は閉じたときにまとめて送る。広い画面ではボタンが
// 出ず、パネルは CSS がサイドバーとして常に出す。

(() => {
	/** @type {HTMLElement | null} */
	const btn = document.querySelector(".filter-toggle");
	/** @type {HTMLFormElement | null} */
	const panel = document.querySelector("#filters");
	if (!panel) return;

	// チェックボックスは変えただけでは何も送らない。変えるたびに送り、選んだ
	// 条件の一覧に読み直す。シートを開いている間は溜めておく。送るとページごと
	// 読み直してシートが閉じるので、1つ選ぶたびに開き直させないため。
	let changed = false;
	panel.addEventListener("change", () => {
		if (panel.classList.contains("open")) {
			changed = true;
			return;
		}
		panel.requestSubmit();
	});

	if (!btn) return;

	btn.addEventListener("click", () => {
		const open = panel.classList.toggle("open");
		btn.setAttribute("aria-expanded", String(open));
		if (!open && changed) panel.requestSubmit();
	});
})();
