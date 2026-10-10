// @ts-check

// 仮想スクロール。全枚数分の高さを #spacer が持ち、可視範囲だけを
// #window に展開する。無限スクロールと違い、任意の位置へ即座に飛べる。
import { Idiomorph } from "./idiomorph.esm.js";
import { layout, visibleWindow, yForIndex } from "./layout.js";

/** @typedef {import("./layout.js").Layout} Layout */
/** @typedef {import("./layout.js").Piece} Piece */

/**
 * タイル1枚。サーバが返したHTML断片から取り出す。
 * @typedef {object} Tile
 * @property {string} html タイルのHTML
 * @property {string} url フルビューのURL（/full/<id>）
 * @property {string} page その1件のページのURL（/item/<id>）
 * @property {string} date 撮影日（"2026-02-08"）
 * @property {boolean} video 動画か
 */

/**
 * 仮想スクロールが他のモジュールに見せるもの。
 * @typedef {object} Gallery
 * @property {number} total 全枚数
 * @property {number} chunkSize 1回に取る塊の枚数
 * @property {number} openIndex /item/<id> で開かれたときの通し番号。無ければ -1
 * @property {(i: number) => Promise<string | null>} urlAt 原本のURL。未取得なら取りに行く
 * @property {(i: number) => string | null} pageAt その1件のページのURL
 * @property {(i: number) => boolean} isVideoAt 動画か
 * @property {(i: number) => void} jumpTo その写真が見える位置までスクロールする
 * @property {(i: number) => void} ensureChunk その写真を含む塊を先読みする
 * @property {Element} scroller スクロールしている要素（文書全体）
 * @property {() => number} maxScroll スクロールできる最大量
 * @property {() => void} render 可視範囲を貼り直す
 * @property {() => Layout | null} current いまのレイアウト。未計測なら null
 * @property {() => { from: number, to: number }} pastedRange 貼ってある範囲
 * @property {(docY: number) => number} toLayoutY 文書座標をレイアウト座標へ
 * @property {(layoutY: number) => number} toDocY レイアウト座標を文書座標へ
 */

/** @type {Gallery | null} */
export const gallery = (() => {
	/** @type {HTMLElement | null} */
	const root = document.querySelector("#gallery");
	/** @type {HTMLElement | null} */
	const spacer = document.querySelector("#spacer");
	/** @type {HTMLElement | null} */
	const win = document.querySelector("#window");
	const probe = document.querySelector("#colprobe");
	if (!root || !spacer || !win || !probe) return null;

	const total = Number(root.dataset.total || 0);
	const chunkSize = Number(root.dataset.chunk || 60);
	// 写真ごとのURL(/item/<id>)で開かれたとき、サーバーが埋めた「開く写真の
	// 通し番号」。閉じたまま開く場合は -1。
	const openIndex = Number(root.dataset.open ?? -1);
	const OVERSCAN_ROWS = 4; // 可視範囲の上下に余分に描く行数

	// スクロールしているのは #gallery ではなく文書全体。
	// ライトボックスの body.locked { overflow: hidden } もこれを前提にしている。
	const scroller = document.scrollingElement || document.documentElement;
	const maxScroll = () =>
		Math.max(1, scroller.scrollHeight - window.innerHeight);

	/** @type {Map<number, Tile[]>} 塊番号 -> その塊のタイル */
	const chunks = new Map();
	/** @type {Map<number, { job: Promise<Tile[]>, controller: AbortController, keep: boolean }>} 取得中の塊だけが入る */
	const inFlight = new Map();

	/** @type {Layout | null} */
	let L = null;
	let spacerTop = 0; // #spacer の文書上のオフセット。レイアウト座標との差
	let pasted = { from: 0, to: 0 };
	let renderedKey = ""; // 「どの塊を何個貼ったか」。同じなら描き直さない

	// サーバが返したHTML断片を、タイル1枚ずつに割る。取得時に1回だけパースし、
	// 以降はここから必要な範囲を切り出して組み立てる。data-full 属性が原本のURL、
	// data-date 属性が日付、data-video 属性が動画かどうか、href がその1件のページ
	// （ライトボックスを開いたときにアドレス欄へ出すURL）。
	/**
	 * @param {string} html
	 * @returns {Tile[]}
	 */
	function parseTiles(html) {
		const tmp = document.createElement("div");
		tmp.innerHTML = html;
		/** @type {NodeListOf<HTMLElement>} */
		const tiles = tmp.querySelectorAll(".tile");
		return [...tiles].map((a) => ({
			html: a.outerHTML,
			url: a.dataset.full,
			page: a.getAttribute("href"),
			date: a.dataset.date,
			video: a.dataset.video === "1",
		}));
	}

	// 初回ページはサーバが先頭の塊を埋めて返しているので、取得済みとして控える。
	function seedFirstChunk() {
		const tiles = parseTiles(win.innerHTML);
		if (tiles.length > 0) chunks.set(0, tiles);
	}

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

	// 日ごとの表は初回HTMLに埋め込まれている。これが無いと1枚も描けない。
	const daysEl = document.querySelector("#daygroups");
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
		const tracks = cs.gridTemplateColumns
			.split(" ")
			.filter((t) => t.length > 0);
		const cols = Math.max(1, tracks.length);
		const tileW = parseFloat(tracks[0]);
		if (!(tileW > 0)) {
			L = null; // スタイル未適用。次の resize/scroll で測り直す
			return;
		}
		const gap = parseFloat(cs.rowGap) || 0;
		const labelH =
			parseFloat(
				getComputedStyle(document.documentElement).getPropertyValue(
					"--label-h",
				),
			) || 0;

		L = layout(days, cols, tileW, labelH, gap); // タイルは正方形なので幅がそのまま高さ
		spacer.style.height = `${Math.max(0, L.height)}px`;
		// レイアウトのy座標は #spacer の上端が0。文書のスクロール位置とは、
		// 上部バー（sticky でも流れの中で場所を占める）とギャラリーの余白の
		// ぶんだけずれる。その差をここで1回だけ測る。
		spacerTop = spacer.getBoundingClientRect().top + scroller.scrollTop;
	}

	// keep を立てた取得は abandonChunksOutside の対象から外す。ライトボックスは
	// 一覧の可視範囲と無関係な位置の写真を取りに行くため、貼り替えの都合で
	// 切られると送った先の写真がいつまでも出ない。
	/**
	 * @param {number} ci 塊番号
	 * @param {{ keep?: boolean }} [opts]
	 * @returns {Promise<Tile[]>}
	 */
	async function fetchChunk(ci, { keep = false } = {}) {
		if (chunks.has(ci)) return chunks.get(ci);
		const pending = inFlight.get(ci);
		if (pending) {
			if (keep) pending.keep = true; // 一覧が始めた取得でも、途中から守る側に回る
			return pending.job;
		}

		const controller = new AbortController();
		const job = (async () => {
			const res = await fetch(
				`/tiles?chunk=${ci}`,
				{ signal: controller.signal },
			);
			// 401 はセッションが切れたということ。ここで握り潰すと、タイルが永久に
			// 埋まらないまま理由の分からない画面が残る。読み直せば未認証の GET / が
			// /login へ導いてくれる。
			if (res.status === 401) {
				location.reload();
				return new Promise(() => {}); // 再読み込みまで呼び出し側を待たせる
			}
			if (!res.ok) throw new Error(`items ${res.status}`);
			const html = await res.text();
			const tiles = parseTiles(html);
			chunks.set(ci, tiles);
			return tiles;
		})().finally(() => inFlight.delete(ci));

		inFlight.set(ci, { job, controller, keep });
		return job;
	}

	// abandonChunksOutside は [from, to] の外で取得中の塊を諦める。
	// keep が立っているもの（ライトボックスが要求したもの）は触らない。
	//
	// スクロール中は通過した位置ごとに取得が始まる。切らずに置くと、止まった
	// 場所の塊が「通り過ぎただけの塊」の後ろに並ぶ。塊が1つでも欠けている間
	// render() は貼らずに帰るため、配信が遅いと画面は長く止まったままになる
	// （フルスキャン中のNASで実測: 20,000枚を下まで降りて28秒）。
	// 中断された取得は chunks に何も残さないので、後で必要になれば取り直す。
	/**
	 * @param {number} from
	 * @param {number} to
	 */
	function abandonChunksOutside(from, to) {
		for (const [ci, pending] of inFlight) {
			if (!pending.keep && (ci < from || ci > to)) pending.controller.abort();
		}
	}

	// 全体の通し番号から写真のURLを引く。未取得なら取りに行く。
	/**
	 * @param {number} i
	 * @returns {Promise<string | null>}
	 */
	async function urlAt(i) {
		if (i < 0 || i >= total) return null;
		const tiles = await fetchChunk(Math.floor(i / chunkSize), { keep: true });
		return tiles[i % chunkSize]?.url ?? null;
	}

	// 取得済みの塊からタイルを引く。未取得なら null。
	/**
	 * @param {number} i
	 * @returns {Tile | null}
	 */
	function tileAt(i) {
		const tiles = chunks.get(Math.floor(i / chunkSize));
		return tiles ? (tiles[i % chunkSize] ?? null) : null;
	}

	// その1件のページURL。取得済みの塊からしか引けないので、表示中のものに
	// 対してだけ使う。
	/**
	 * @param {number} i
	 * @returns {string | null}
	 */
	function pageAt(i) {
		return tileAt(i)?.page ?? null;
	}

	// その1件が動画か。pageAt と同じく取得済みの塊からしか引けない。urlAt を
	// 待った後なら塊は必ず揃っている。
	/**
	 * @param {number} i
	 * @returns {boolean}
	 */
	function isVideoAt(i) {
		return tileAt(i)?.video === true;
	}

	/** @param {number} i */
	function ensureChunk(i) {
		if (i < 0 || i >= total) return;
		fetchChunk(Math.floor(i / chunkSize), { keep: true }).catch(() => {});
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
		const fetchTo = Math.min(
			Math.floor((total - 1) / chunkSize),
			lastChunk + 1,
		);
		abandonChunksOutside(fetchFrom, fetchTo);
		for (let ci = fetchFrom; ci <= fetchTo; ci++) {
			if (!chunks.has(ci))
				fetchChunk(ci)
					.then(render)
					.catch(() => {});
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
			const pTo = Math.min(
				to,
				p.e.start + p.e.n,
				p.e.start + (p.r1 + 1) * p.e.span,
			);
			if (pFrom >= pTo) continue;
			const html = cardHTML(p, pFrom, pTo);
			if (!html) return;
			parts.push(html);
		}
		if (parts.length === 0) return;

		renderedKey = key;
		pasted = { from, to };
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
	// data-date から作る。日ごとの表が古くても、ラベルはそのカードに実際に
	// 写っている日を指す。
	/**
	 * @param {Piece} piece
	 * @param {number} from
	 * @param {number} to
	 * @returns {string} 1枚でも未取得なら空文字列
	 */
	function cardHTML(piece, from, to) {
		const tiles = [];
		for (let i = from; i < to; i++) {
			const t = tileAt(i);
			if (!t) return "";
			tiles.push(t.html);
		}
		// 段の途中から貼るとき（大きい日をスクロールしている最中）はラベルを落とす
		const head = tileAt(from);
		const label =
			piece.r0 > 0 || !head
				? ""
				: `<div class="daylabel">${formatDay(head.date)}</div>`;
		return (
			`<div class="daycard" id="d-${piece.e.d}" style="grid-column:span ${piece.e.span};` +
			`grid-template-columns:repeat(${piece.e.span},1fr)">${label}${tiles.join("")}</div>`
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

		renderedKey = ""; // 列数が変われば貼り直しが必要
		if (L && L.height > 0) {
			// yForIndex が返すのはレイアウト座標。scrollTop は文書座標なので戻す。
			scroller.scrollTop = toDocY(yForIndex(L, topIndex));
		}
		render();
	}

	seedFirstChunk();
	measure();
	render();
	window.addEventListener("scroll", render, { passive: true });
	window.addEventListener("resize", onResize);
	// スクロールバーの出現で #window の幅が変わっても resize は発火しない。
	// 要素そのものを監視して、列数とタイル高を測り直す。
	new ResizeObserver(onResize).observe(win);

	return {
		total,
		chunkSize,
		openIndex,
		urlAt,
		pageAt,
		isVideoAt,
		jumpTo,
		ensureChunk,
		scroller,
		maxScroll,
		render,
		current: () => L,
		pastedRange: () => pasted,
		toLayoutY,
		toDocY,
	};
})();
