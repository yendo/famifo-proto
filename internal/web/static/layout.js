// @ts-check

// レイアウト計算。仮想スクロールが「どの写真をどこに描くか」を決める。
//
// 日ごとに「占める列数 = min(枚数, 列数)」を割り当て、順に詰める。入らな
// ければ次のストライプへ送る。これは CSS Grid の自動配置（dense を付けない
// 場合）の規則そのものなので、同じ順でカードを流し込めばブラウザはここと
// 同じ答えを出す。列位置をJSが指定して回る必要はない。
//
// どれも純粋関数で、DOMにもモジュールの状態にも触らない。

/**
 * DayGroups の1要素。サーバが初回HTMLに埋め込む。
 * @typedef {object} DayGroup
 * @property {string} d 日付（"2026-02-08"）
 * @property {number} n その日の枚数
 */

/**
 * 1日ぶんのカード。座標はすべて #spacer の上端を0とするレイアウト座標。
 * @typedef {object} Entry
 * @property {string} d 日付
 * @property {number} y カードの上端
 * @property {number} h カードの高さ（ラベル込み）
 * @property {number} start 先頭の写真の通し番号
 * @property {number} n 枚数
 * @property {number} span 占める列数
 * @property {number} rows 段数
 */

/**
 * レイアウト全体。
 * @typedef {object} Layout
 * @property {Entry[]} entries 並び順どおりのカード
 * @property {number} height 全体の高さ
 * @property {number} cols 列数
 * @property {number} tileH タイルの高さ（正方形なので幅と同じ）
 * @property {number} labelH 日付ラベルの高さ
 * @property {number} gap 行と列の間隔
 */

/**
 * 可視範囲にかかるカードと、その中で描く段の範囲（両端を含む）。
 * @typedef {object} Piece
 * @property {Entry} e
 * @property {number} r0
 * @property {number} r1
 */

/**
 * visibleWindow の結果。
 * @typedef {object} VisibleWindow
 * @property {Piece[]} pieces
 * @property {number} pasteY 貼り付ける位置（先頭のカードか段の上端）
 * @property {number} from 先頭の写真の通し番号
 * @property {number} to 末尾の次の写真の通し番号
 */

/**
 * @param {DayGroup[]} groups
 * @param {number} cols
 * @param {number} tileH
 * @param {number} labelH
 * @param {number} gap
 * @returns {Layout}
 */
export function layout(groups, cols, tileH, labelH, gap) {
	const entries = [];
	let y = 0; // いま組み立て中のストライプの上端
	let stripeH = 0; // その高さ。0なら未開始
	let rem = 0; // その残り列数
	let start = 0; // 次のグループの先頭写真の通し番号

	for (const g of groups) {
		const span = Math.min(g.n, cols);
		const rows = Math.ceil(g.n / span);
		const h = labelH + gap + rows * tileH + (rows - 1) * gap;

		if (span < cols && rem >= span) {
			// いまのストライプに載る。横並びになるのはこの経路だけ。
			// 1行に収まる日は必ず rows===1 なので、高さはストライプと一致する。
			rem -= span;
		} else {
			if (stripeH > 0) y += stripeH + gap; // 前のストライプを閉じる
			stripeH = h;
			rem = cols - span; // 行を占有した日(span===cols)なら0になり、次は必ず新しい行
		}
		entries.push({ d: g.d, y, h, start, n: g.n, span, rows });
		start += g.n;
	}

	return {
		entries,
		height: stripeH > 0 ? y + stripeH : 0,
		cols,
		tileH,
		labelH,
		gap,
	};
}

// key(e) が v 以下である最後の要素の添字。無ければ0。
/**
 * @template T
 * @param {T[]} entries
 * @param {number} v
 * @param {(e: T) => number} key
 * @returns {number}
 */
function lastAtMost(entries, v, key) {
	let lo = 0;
	let hi = entries.length - 1;
	let found = 0;
	while (lo <= hi) {
		const mid = (lo + hi) >> 1;
		if (key(entries[mid]) <= v) {
			found = mid;
			lo = mid + 1;
		} else {
			hi = mid - 1;
		}
	}
	return found;
}

// 通し番号 i の写真が属する段の上端。
/**
 * @param {Layout} L
 * @param {number} i
 * @returns {number}
 */
export function yForIndex(L, i) {
	if (L.entries.length === 0) return 0;
	const e = L.entries[lastAtMost(L.entries, i, (x) => x.start)];
	const row = Math.floor((i - e.start) / e.span);
	return e.y + L.labelH + L.gap + row * (L.tileH + L.gap);
}

// y の位置にある日。スクラバーのラベルが使う。
/**
 * @param {Layout} L
 * @param {number} y
 * @returns {string}
 */
export function dayAtY(L, y) {
	if (L.entries.length === 0) return "";
	return L.entries[lastAtMost(L.entries, y, (x) => x.y)].d;
}

// [top, bottom] に重なる範囲を切り出す。
//
// 詰めたストライプは丸ごと描く。ラベルを落とすと高さが変わり、同じ
// ストライプに並ぶ他のカードと段が合わなくなるため。1ストライプは
// 高々 labelH + gap + tileH しかないので丸ごとでも安い。
// 列数を超える日だけは段単位で切り、ラベルが上に流れていれば落とす。
/**
 * @param {Layout} L
 * @param {number} top
 * @param {number} bottom
 * @returns {VisibleWindow | null}
 */
export function visibleWindow(L, top, bottom) {
	const es = L.entries;
	if (es.length === 0) return null;

	let i = lastAtMost(es, top, (x) => x.y);
	while (i > 0 && es[i - 1].y === es[i].y) i--; // ストライプの先頭まで戻る

	const pieces = [];
	for (; i < es.length; i++) {
		const e = es[i];
		if (e.y > bottom) break;

		const tileTop = e.y + L.labelH + L.gap;
		let r0 = 0;
		let r1 = e.rows - 1;
		if (e.rows > 1) {
			r0 = Math.max(0, Math.floor((top - tileTop) / (L.tileH + L.gap)));
			r1 = Math.min(
				e.rows - 1,
				Math.floor((bottom - tileTop) / (L.tileH + L.gap)),
			);
			if (r1 < r0) continue; // まるごと範囲外
		} else if (e.y + e.h < top) {
			continue;
		}
		pieces.push({ e, r0, r1 });
	}
	if (pieces.length === 0) return null;

	const f = pieces[0];
	const l = pieces[pieces.length - 1];
	return {
		pieces,
		// 先頭が段の途中から始まるならその段の上端、そうでなければカードの上端
		pasteY:
			f.r0 > 0 ? f.e.y + L.labelH + L.gap + f.r0 * (L.tileH + L.gap) : f.e.y,
		from: f.e.start + f.r0 * f.e.span,
		to: Math.min(l.e.start + l.e.n, l.e.start + (l.r1 + 1) * l.e.span),
	};
}
