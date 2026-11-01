// @ts-check

// 絞り込みのパネル。チェックを変えたらフォームを送る。狭い画面ではシートとして
// 開け閉めする。広い画面ではボタンが出ず、パネルは CSS がサイドバーとして常に出す。

(() => {
	/** @type {HTMLElement | null} */
	const btn = document.querySelector(".filter-toggle");
	/** @type {HTMLFormElement | null} */
	const panel = document.querySelector("#filters");
	if (!panel) return;

	// チェックボックスは変えただけでは何も送らない。変えるたびに送り、選んだ
	// 条件の一覧に読み直す。
	panel.addEventListener("change", () => panel.requestSubmit());

	if (!btn) return;

	btn.addEventListener("click", () => {
		const open = panel.classList.toggle("open");
		btn.setAttribute("aria-expanded", String(open));
	});
})();
