// @ts-check

// チャンクの取得と保持。写真は通し番号 chunkSize 枚ごとのチャンクで取りに
// 行き、取れたチャンクはタイル1枚ずつに割って持っておく。i 番の写真は
// チャンク floor(i / chunkSize) の i % chunkSize 番目。レイアウトや座標には
// 触らない。

/**
 * チャンクの取得と保持が他のモジュールに見せるもの。
 *
 * 名前は返すもので分ける。「名詞 + Of」は状態に関係ない計算の結果を返す。
 * 「has + 名詞」は取得済みかを真偽値で返す。「名詞 + At」は取得済みのもの
 * からその場で引き、無ければ null を返す（取りに行かない）。「load + 名詞」
 * はその名詞の Promise を返し、無ければ取りに行く。動詞だけのものは何も
 * 返さない。ci はチャンクの番号、i は写真の通し番号。
 * @typedef {object} Chunks
 * @property {number} tileCount 写真（動画も含む）の枚数
 * @property {(i: number) => number} chunkOf その写真を含むチャンクの番号
 * @property {(ci: number) => boolean} hasChunk そのチャンクを取得済みか
 * @property {(i: number) => Tile | null} tileAt 取得済みならタイル
 * @property {(ci: number) => Promise<Tile[]>} loadChunk そのチャンクを返す。一覧が範囲外に出れば中断されうる
 * @property {(i: number) => Promise<Tile | null>} loadTile その写真のタイルを返す。中断されない
 * @property {(i: number) => void} prefetch その写真を含むチャンクを先読みする。中断されない
 * @property {(from: number, to: number) => void} abandonOutside チャンクの番号が範囲外の取得を中断する
 */

/**
 * タイル1枚。サーバが /tiles で返した JSON の要素そのもの。
 * @typedef {object} Tile
 * @property {string} id 写真のID
 * @property {string} pageUrl その1件のページのURL（/item/<id>）
 * @property {string} fullUrl フルビューのURL（/full/<id>）
 * @property {string} thumbUrl サムネイルのURL（/thumb/<id>）
 * @property {string} date 撮影日（"2026-02-08"）
 * @property {boolean} video 動画か
 */

/** @type {Chunks | null} */
export const chunks = (() => {
	/** @type {HTMLElement | null} */
	const root = document.querySelector("#gallery");
	if (!root) return null;

	const tileCount = Number(root.dataset.total);
	const chunkSize = Number(root.dataset.chunk);

	/** @type {Map<number, Tile[]>} チャンク番号 -> そのチャンクのタイル */
	const loaded = new Map();
	/** @type {Map<number, { job: Promise<Tile[]>, controller: AbortController, keep: boolean }>} 取得中のチャンクだけが入る */
	const inFlight = new Map();

	/** @param {number} i */
	function chunkOf(i) {
		return Math.floor(i / chunkSize);
	}

	/** @param {number} ci */
	function hasChunk(ci) {
		return loaded.has(ci);
	}

	// 取得済みのチャンクからタイルを引く。未取得なら null。
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

	// abandonOutside はチャンクの番号が [from, to] の外の取得を諦める。
	// keep が立っているもの（loadTile と prefetch が始めたもの）は触らない。
	//
	// スクロール中は通過した位置ごとに取得が始まる。切らずに置くと、止まった
	// 場所のチャンクが「通り過ぎただけのチャンク」の後ろに並ぶ。チャンクが
	// 1つでも欠けている間 render() は貼らずに帰るため、配信が遅いと画面は
	// 長く止まったままになる（フルスキャン中のNASで実測: 20,000枚を下まで
	// 降りて28秒）。
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

	// チャンクを取りに行く。取得済みならそれを、取得中ならその取得を返す。
	//
	// keep を立てた取得は abandonOutside の対象から外す。ライトボックスは
	// 一覧の可視範囲と無関係な位置の写真を取りに行くため、貼り替えの都合で
	// 切られると送った先の写真がいつまでも出ない。
	/**
	 * @param {number} ci チャンク番号
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
			/** @type {Tile[]} */
			const tiles = await res.json();
			loaded.set(ci, tiles);
			return tiles;
		})().finally(() => inFlight.delete(ci));

		inFlight.set(ci, { job, controller, keep });
		return job;
	}

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
