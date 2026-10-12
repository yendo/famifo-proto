// @ts-check

// 仮想スクロール。全枚数分の高さを #spacer が持ち、そこから切り取った範囲
// （可視範囲と上下の余分）だけを #window に展開する。無限スクロールと違い、
// 任意の位置へ即座に飛べる。
import { chunks } from "./chunk.js";
import { Idiomorph } from "./idiomorph.esm.js";
import { layout, windowAt, yForIndex } from "./layout.js";

/** @typedef {import("./layout.js").Layout} Layout */
/** @typedef {import("./layout.js").CardRowRange} CardRowRange */

/**
 * 仮想スクロールが他のモジュールに見せるもの。
 * @typedef {object} Gallery
 * @property {number} openIndex /item/<id> で開かれたときの通し番号。無ければ -1
 * @property {(i: number) => void} jumpTo その写真が見える位置までスクロールする
 * @property {Element} scroller スクロールしている要素（文書全体）
 * @property {() => number} maxScroll スクロールできる最大量
 * @property {() => Layout | null} current いまのレイアウト。未計測なら null
 * @property {(docY: number) => number} toLayoutY 文書座標をレイアウト座標へ
 */

/** @type {Gallery | null} */
export const gallery = (() => {
	/** @type {HTMLElement | null} */
	const root = document.querySelector("#gallery");
	/** @type {HTMLElement | null} */
	const spacer = document.querySelector("#spacer");
	/** @type {HTMLElement | null} */
	const win = document.querySelector("#window");
	const probe = document.querySelector("#layoutprobe");
	if (!root || !spacer || !win || !probe || !chunks) return null;

	const { tileCount } = chunks;
	// 写真ごとのURL(/item/<id>)で開かれたとき、サーバーが埋めた「開く写真の
	// 通し番号」。閉じたまま開く場合は -1。
	const openIndex = Number(root.dataset.open);
	// DayGroups は初回HTMLに埋め込まれている。これが無いと1枚も描けない。
	const daysEl = document.querySelector("#daygroups");
	const days = daysEl ? JSON.parse(daysEl.textContent) : [];
	const OVERSCAN_ROWS = 4; // 可視範囲の上下に余分に描く行数
	const RETRY_MS = 3000; // 取得に失敗したチャンクを取り直すまでの待ち時間

	// スクロールしているのは #gallery ではなく文書全体。
	// ライトボックスの body.locked { overflow: hidden } もこれを前提にしている。
	const scroller = document.scrollingElement || document.documentElement;
	const maxScroll = () =>
		Math.max(1, scroller.scrollHeight - window.innerHeight);

	/** @type {Layout | null} */
	let L = null;
	let spacerTop = 0; // #spacer の文書上のオフセット。レイアウト座標との差
	let renderedKey = ""; // 「どのチャンクを何個貼ったか」。同じなら描き直さない

	// レイアウトのy座標は #spacer の上端が0。文書のスクロール位置とは、
	// 上部バー（sticky でも流れの中で場所を占める）とギャラリーの余白の
	// ぶんだけずれる。変換はこの2つに集約する。
	/** @param {number} docY */
	function toLayoutY(docY) {
		return docY - spacerTop;
	}
	/** @param {number} layoutY */
	function toDocY(layoutY) {
		return layoutY + spacerTop;
	}

	// 列数・列幅・gap・ラベル高はCSSの計算結果から読む。auto-fill の計算を自前で
	// 再現するとCSSのbreakpointと二重管理になる。ラベル高も定義はCSS側の1箇所だけ。
	//
	// #window ではなく #layoutprobe（子を持たない空のグリッド）から測る。#window
	// には直前のレイアウトのカードが残っており、そのカードの grid-column:
	// span N が実際に収まる数を超えていると、CSS Grid はそれを収めるために
	// 暗黙トラックを追加してトラック列を押し広げる。#window はそれを常に
	// フルに収める側（span を測った cols に合わせて描く側）なので、狭めた
	// 直後は「一番大きい span」に押し上げられた列数を読んでしまい、本当の
	// 列数（=このビューポート幅に自然に収まる数）より多く出る。中身が無い
	// #layoutprobe ならその影響を受けない。
	function updateLayout() {
		const cs = getComputedStyle(probe);
		const tracks = cs.gridTemplateColumns
			.split(" ")
			.filter((t) => t.length > 0);
		const cols = Math.max(1, tracks.length);
		const tileW = parseFloat(tracks[0]);
		if (!(tileW > 0)) {
			L = null; // スタイル未適用。次に onResize から呼ばれたときに測り直す
			return;
		}
		const gap = parseFloat(cs.rowGap) || 0;
		const labelH = parseFloat(cs.paddingTop) || 0; // --label-h を写したもの（app.css）

		L = layout(days, cols, tileW, labelH, gap); // タイルは正方形なので幅がそのまま高さ
		spacer.style.height = `${L.height}px`;
		// toLayoutY / toDocY が使う、文書座標とレイアウト座標の差を測る。
		spacerTop = spacer.getBoundingClientRect().top + scroller.scrollTop;
	}

	function render() {
		if (!L || L.height <= 0 || tileCount === 0) return;

		const over = OVERSCAN_ROWS * (L.tileH + L.gap);
		const top = toLayoutY(scroller.scrollTop);
		const w = windowAt(L, top - over, top + window.innerHeight + over);
		if (!w) return;

		// DayGroups と総枚数はサーバが別々に読むため、開いたまま新着が入ると
		// DayGroups の合計のほうが多くなりうる。総枚数で抑えないと、存在しない
		// チャンクを待ち続けて末尾が永久に更新されなくなる。
		const from = w.from;
		const to = Math.min(tileCount, w.to);
		if (from >= to) return;

		const firstChunk = chunks.chunkOf(from);
		const lastChunk = chunks.chunkOf(to - 1);

		// 切り取った範囲の前後1チャンクも先読みしておく。切り出す範囲は広げない。
		const fetchFrom = Math.max(0, firstChunk - 1);
		const fetchTo = Math.min(chunks.chunkOf(tileCount - 1), lastChunk + 1);
		chunks.abandonOutside(fetchFrom, fetchTo);
		// 失敗した取得は少し待って描き直す。render が呼ばれるのはスクロールと
		// リサイズのときだけなので、そうしないと手を止めている間は空白のまま残る。
		// 描き直しはその時点で要るチャンクだけを取りに行くので、範囲から外れたチャンクは
		// 取り直さない。こちらが中断した取得（AbortError）は取り直さない。
		for (let ci = fetchFrom; ci <= fetchTo; ci++) {
			if (!chunks.hasChunk(ci))
				chunks
					.loadChunk(ci)
					.then(render)
					.catch((e) => {
						if (e.name !== "AbortError") setTimeout(render, RETRY_MS);
					});
		}

		// 必要なチャンクが1つでも欠けていると穴の空いたカードになるので、揃うまで描かない
		for (let ci = firstChunk; ci <= lastChunk; ci++) {
			if (!chunks.hasChunk(ci)) return;
		}

		// 貼る内容が前回と同じなら、HTMLの組み立ても差分の計算も省く。
		const key = `${from}:${to}:${L.cols}`;
		if (key === renderedKey) return;

		// 先に組み立て、1枚でも欠けていたら renderedKey を据え置いたまま抜ける。
		// 確定を先にすると、欠けたまま貼った状態がキャッシュされて直らない。
		const parts = [];
		for (const range of w.ranges) {
			const rangeFrom = range.from;
			const rangeTo = Math.min(to, range.to);
			if (rangeFrom >= rangeTo) continue;
			const html = cardHTML(range, rangeFrom, rangeTo);
			if (!html) return;
			parts.push(html);
		}
		if (parts.length === 0) return;

		renderedKey = key;
		// 全置換ではなく差分で当てる。作り直すと、中身も寸法も同時に変わった
		// 合成レイヤーになる。WebKitはこのときバッキングストアを捨てて描き直す
		// ため、再ペイントが間に合わないフレームで背景色が露出する（iPhone /
		// iPad でスクロール中に表示領域全体が一瞬真っ黒になる）。Blinkは新しい
		// ラスタが揃うまで古い内容を描き続けるので、そちらでは表面化しない。
		// 残る写真の要素をそのまま使い回せば、その状況自体を作らない。
		// タイルとカードの id 属性が対応付けの手がかりになる。
		Idiomorph.morph(win, parts.join(""), { morphStyle: "innerHTML" });
		win.style.transform = `translateY(${w.pasteY}px)`;

		// 各タイルに通し番号を書く。切り出す範囲は連続しているのでDOM順と一致する。
		/** @type {NodeListOf<HTMLElement>} */
		const tiles = win.querySelectorAll(".tile");
		for (let k = 0; k < tiles.length; k++)
			tiles[k].dataset.i = String(from + k);
	}

	// 1枚のカード。占める列数はレイアウトが決め、ラベルの文言はタイル自身の
	// data-date から作る。DayGroups が古くても、ラベルはそのカードに実際に
	// 写っている日を指す。
	/**
	 * @param {CardRowRange} range
	 * @param {number} from
	 * @param {number} to
	 * @returns {string} 1枚でも未取得なら空文字列
	 */
	function cardHTML(range, from, to) {
		const tiles = [];
		for (let i = from; i < to; i++) {
			const t = chunks.tileAt(i);
			if (!t) return "";
			tiles.push(t.html);
		}
		// 段の途中から貼るとき（大きい日をスクロールしている最中）はラベルを落とす
		const head = chunks.tileAt(from);
		const label =
			range.firstRow > 0
				? ""
				: `<div class="daylabel">${formatDay(head.date)}</div>`;
		return (
			`<div class="daycard" id="d-${range.card.d}" style="grid-column:span ${range.card.span};` +
			`grid-template-columns:repeat(${range.card.span},1fr)">${label}${tiles.join("")}</div>`
		);
	}

	// "2026-02-08" → "2026年2月8日"。今年なら年を省く。
	// 最狭の1列(CSSの最小値110px)に収めるため、これ以上長い表記にはしない。
	/**
	 * @param {string} d
	 * @returns {string}
	 */
	function formatDay(d) {
		if (!d) return "";
		const [y, m, day] = d.split("-");
		const head = Number(y) === new Date().getFullYear() ? "" : `${y}年`;
		return `${head}${Number(m)}月${Number(day)}日`;
	}

	// jumpTo は通し番号の写真が見える位置までスクロールする。写真ごとのURLで
	// 開かれたときに、背後の一覧をその写真の位置に合わせるために使う。
	/** @param {number} i */
	function jumpTo(i) {
		if (!L || L.height <= 0) return;
		scroller.scrollTop = toDocY(yForIndex(L, i));
		render();
	}

	function onResize() {
		// 回転やリサイズで列数が変わるとレイアウト全体の高さが変わるため、
		// scrollTop をそのまま残すと別の写真の位置に飛ぶ。いま先頭に見えていた
		// 写真の通し番号を、測り直す前のレイアウトで求めておいて復元する。
		const prev = L;
		const topIndex = topIndexOnScreen();

		updateLayout();

		// ResizeObserver は #window 自身の高さの変化でも発火する。貼り付ける量は
		// スクロール中に増減するため、通常のスクロールでも呼ばれる。列数もタイル
		// 高も変わっていなければ、位置の復元も貼り直しの強制も要らない。ただし
		// 高さだけが伸びた場合は見える範囲が広がるので、描き直し（render）は
		// どちらの場合も呼ぶ。
		if (!prev || !L || prev.cols !== L.cols || prev.tileH !== L.tileH) {
			renderedKey = ""; // 列数が変われば貼り直しが必要
			if (L && L.height > 0) {
				// yForIndex が返すのはレイアウト座標。scrollTop は文書座標なので戻す。
				scroller.scrollTop = toDocY(yForIndex(L, topIndex));
			}
		}
		render();
	}

	// いま画面の上端にある写真の通し番号。レイアウトが無ければ0。
	//
	// 貼ってある範囲の先頭は使わない。そこは OVERSCAN のぶん画面外まで
	// 含んでいるので、復元すると毎回4行ぶん手前に着地する。
	//
	// 高さ0の窓で問い合わせると、ストライプ間の隙間(gap)にちょうど当たった
	// ときに空振りして null が返り、先頭に落ちる。1行ぶんの高さを持たせて、
	// 必ずどこかのストライプに当てる。隙間に当たった場合は次のストライプが
	// 返るが、そこが実際に最初に見える内容なので正しい。
	function topIndexOnScreen() {
		if (!L) return 0;
		const top = toLayoutY(scroller.scrollTop);
		const at = windowAt(L, top, top + L.tileH + L.gap);
		return at ? at.from : 0;
	}

	updateLayout();
	render();
	window.addEventListener("scroll", render, { passive: true });
	window.addEventListener("resize", onResize);
	// スクロールバーの出現で #window の幅が変わっても resize は発火しない。
	// 要素そのものを監視して、列数とタイル高を測り直す。
	new ResizeObserver(onResize).observe(win);

	return {
		openIndex,
		jumpTo,
		scroller,
		maxScroll,
		current: () => L,
		toLayoutY,
	};
})();
