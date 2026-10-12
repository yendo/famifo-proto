// @ts-check

// 塊の取得と保持。写真は通し番号 chunkSize 枚ごとの塊で取りに行き、取れた
// 塊はタイル1枚ずつに割って持っておく。i 番の写真は塊 floor(i / chunkSize)
// の i % chunkSize 番目。レイアウトや座標には触らない。

/**
 * タイル1枚。サーバが返したHTML断片から取り出す。
 * @typedef {object} Tile
 * @property {string} html タイルのHTML
 * @property {string} fullUrl フルビューのURL（/full/<id>）
 * @property {string} pageUrl その1件のページのURL（/item/<id>）
 * @property {string} date 撮影日（"2026-02-08"）
 * @property {boolean} video 動画か
 */

/**
 * 塊の取得と保持が他のモジュールに見せるもの。
 *
 * 名前は返すもので分ける。「名詞 + At」は取得済みのものからその場で引き、
 * 無ければ null を返す（取りに行かない）。「load + 名詞」はその名詞の
 * Promise を返し、無ければ取りに行く。動詞だけのものは何も返さない。
 * ci は塊の番号、i は写真の通し番号。
 * @typedef {object} Chunks
 * @property {number} tileCount 写真（動画も含む）の枚数
 * @property {(i: number) => number} chunkOf その写真を含む塊の番号
 * @property {(ci: number) => boolean} hasChunk その塊を取得済みか
 * @property {(i: number) => Tile | null} tileAt 取得済みならタイル
 * @property {(ci: number) => Promise<Tile[]>} loadChunk その塊を返す。一覧が範囲外に出れば中断されうる
 * @property {(i: number) => Promise<Tile | null>} loadTile その写真のタイルを返す。中断されない
 * @property {(i: number) => void} prefetch その写真を含む塊を先読みする。中断されない
 * @property {(from: number, to: number) => void} abandonOutside 塊の番号が範囲外の取得を中断する
 */

/** @type {Chunks | null} */
export const chunks = (() => {
	/** @type {HTMLElement | null} */
	const root = document.querySelector("#gallery");
	const win = document.querySelector("#window");
	if (!root || !win) return null;

	const tileCount = Number(root.dataset.total);
	const chunkSize = Number(root.dataset.chunk);

	/** @type {Map<number, Tile[]>} 塊番号 -> その塊のタイル */
	const loaded = new Map();
	/** @type {Map<number, { job: Promise<Tile[]>, controller: AbortController, keep: boolean }>} 取得中の塊だけが入る */
	const inFlight = new Map();

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
			fullUrl: a.dataset.full,
			pageUrl: a.getAttribute("href"),
			date: a.dataset.date,
			video: a.dataset.video === "1",
		}));
	}

	// 初回ページはサーバが先頭の塊を埋めて返しているので、取得済みとして控える。
	function loadEmbeddedChunk() {
		const tiles = parseTiles(win.innerHTML);
		if (tiles.length > 0) loaded.set(0, tiles);
	}

	/** @param {number} i */
	function chunkOf(i) {
		return Math.floor(i / chunkSize);
	}

	/** @param {number} ci */
	function hasChunk(ci) {
		return loaded.has(ci);
	}

	// 塊を取りに行く。取得済みならそれを、取得中ならその取得を返す。
	//
	// keep を立てた取得は abandonOutside の対象から外す。ライトボックスは
	// 一覧の可視範囲と無関係な位置の写真を取りに行くため、貼り替えの都合で
	// 切られると送った先の写真がいつまでも出ない。
	/**
	 * @param {number} ci 塊番号
	 * @param {boolean} keep
	 * @returns {Promise<Tile[]>}
	 */
	async function request(ci, keep) {
		if (loaded.has(ci)) return loaded.get(ci);
		const pending = inFlight.get(ci);
		if (pending) {
			if (keep) pending.keep = true; // 一覧が始めた取得でも、途中から守る側に回る
			return pending.job;
		}

		const controller = new AbortController();
		const job = (async () => {
			const res = await fetch(`/tiles?chunk=${ci}`, {
				signal: controller.signal,
			});
			// 401 はセッションが切れたということ。ここで握り潰すと、タイルが永久に
			// 埋まらないまま理由の分からない画面が残る。読み直せば未認証の GET / が
			// /login へ導いてくれる。
			if (res.status === 401) {
				location.reload();
				return new Promise(() => {}); // 再読み込みまで呼び出し側を待たせる
			}
			if (!res.ok) throw new Error(`tiles ${res.status}`);
			const html = await res.text();
			const tiles = parseTiles(html);
			loaded.set(ci, tiles);
			return tiles;
		})().finally(() => inFlight.delete(ci));

		inFlight.set(ci, { job, controller, keep });
		return job;
	}

	// abandonOutside は塊の番号が [from, to] の外の取得を諦める。
	// keep が立っているもの（loadTile と prefetch が始めたもの）は触らない。
	//
	// スクロール中は通過した位置ごとに取得が始まる。切らずに置くと、止まった
	// 場所の塊が「通り過ぎただけの塊」の後ろに並ぶ。塊が1つでも欠けている間
	// render() は貼らずに帰るため、配信が遅いと画面は長く止まったままになる
	// （フルスキャン中のNASで実測: 20,000枚を下まで降りて28秒）。
	// 中断された取得は loaded に何も残さないので、後で必要になれば取り直す。
	/**
	 * @param {number} from
	 * @param {number} to
	 */
	function abandonOutside(from, to) {
		for (const [ci, pending] of inFlight) {
			if (!pending.keep && (ci < from || ci > to)) pending.controller.abort();
		}
	}

	// 取得済みの塊からタイルを引く。未取得なら null。
	/**
	 * @param {number} i
	 * @returns {Tile | null}
	 */
	function tileAt(i) {
		const tiles = loaded.get(chunkOf(i));
		return tiles ? (tiles[i % chunkSize] ?? null) : null;
	}

	// 一覧が貼るための取得。一覧の範囲から外れれば abandonOutside で中断される。
	/**
	 * @param {number} ci
	 * @returns {Promise<Tile[]>}
	 */
	function loadChunk(ci) {
		return request(ci, false);
	}

	// ライトボックスのための取得。一覧の範囲とは無関係なので中断されない。
	/**
	 * @param {number} i
	 * @returns {Promise<Tile | null>}
	 */
	async function loadTile(i) {
		if (i < 0 || i >= tileCount) return null;
		const tiles = await request(chunkOf(i), true);
		return tiles[i % chunkSize] ?? null;
	}

	/** @param {number} i */
	function prefetch(i) {
		if (i < 0 || i >= tileCount) return;
		request(chunkOf(i), true).catch(() => {});
	}

	loadEmbeddedChunk();

	return {
		tileCount,
		chunkOf,
		hasChunk,
		tileAt,
		loadChunk,
		loadTile,
		prefetch,
		abandonOutside,
	};
})();
