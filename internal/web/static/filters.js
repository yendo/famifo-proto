// @ts-check

// 絞り込みのパネルを、狭い画面でシートとして開け閉めする。広い画面ではボタンが
// 出ず、パネルは CSS がサイドバーとして常に出す。項目はただのリンクなので、
// 選んだ後の処理はここには無い。

(() => {
	/** @type {HTMLElement | null} */
	const btn = document.querySelector(".filter-toggle");
	/** @type {HTMLElement | null} */
	const panel = document.querySelector("#filters");
	if (!btn || !panel) return;

	btn.addEventListener("click", () => {
		const open = panel.classList.toggle("open");
		btn.setAttribute("aria-expanded", String(open));
	});
})();
