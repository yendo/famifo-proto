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
 * レイアウト全体。
 * @typedef {object} Layout
 * @property {Card[]} cards 並び順どおりのカード
 * @property {number} height 全体の高さ
 * @property {number} cols 列数
 * @property {number} tileH タイルの高さ（正方形なので幅と同じ）
 * @property {number} labelH 日付ラベルの高さ
 * @property {number} gap 行と列の間隔
 */

/**
 * 1日ぶんのカード。座標はすべて #spacer の上端を0とするレイアウト座標。
 * @typedef {object} Card
 * @property {string} d 日付
 * @property {number} y カードの上端
 * @property {number} h カードの高さ（ラベル込み）
 * @property {number} start 先頭の写真の通し番号
 * @property {number} n 枚数
 * @property {number} span 占める列数
 * @property {number} rows 段数
 */

/**
 * DayGroups の1要素。サーバが初回HTMLに埋め込む。
 * @typedef {object} DayGroup
 * @property {string} d 日付（"2026-02-08"）
 * @property {number} n その日の枚数
 */

/**
 * visibleWindow の結果。
 * @typedef {object} VisibleWindow
 * @property {CardRowRange[]} ranges
 * @property {number} pasteY 貼り付ける位置（先頭のカードか段の上端）
 * @property {number} from 先頭の写真の通し番号
 * @property {number} to 末尾の次の写真の通し番号
 */

/**
 * 可視範囲にかかるカードと、その中で描く段の範囲（両端を含む）。
 * @typedef {object} CardRowRange
 * @property {Card} card
 * @property {number} firstRow 描く最初の段
 * @property {number} lastRow 描く最後の段
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
	const cards = [];
	let y = 0; // いま組み立て中のストライプの上端
	let stripeH = 0; // その高さ。0なら未開始
	let freeCols = 0; // その空いている列数
	let start = 0; // 次のグループの先頭写真の通し番号

	for (const g of groups) {
		const span = Math.min(g.n, cols);
		const rows = Math.ceil(g.n / span);
		const h = labelH + gap + rows * tileH + (rows - 1) * gap;

		if (span < cols && freeCols >= span) {
			// いまのストライプに載る。横並びになるのはこの経路だけ。
			// 1行に収まる日は必ず rows===1 なので、高さはストライプと一致する。
			freeCols -= span;
		} else {
			if (stripeH > 0) y += stripeH + gap; // 前のストライプを閉じる
			stripeH = h;
			freeCols = cols - span; // 行を占有した日(span===cols)なら0になり、次は必ず新しい行
		}
		cards.push({ d: g.d, y, h, start, n: g.n, span, rows });
		start += g.n;
	}

	return {
		cards,
		height: stripeH > 0 ? y + stripeH : 0,
		cols,
		tileH,
		labelH,
		gap,
	};
}

// 通し番号 i の写真が属する段の上端。
/**
 * @param {Layout} L
 * @param {number} i
 * @returns {number}
 */
export function yForIndex(L, i) {
	if (L.cards.length === 0) return 0;
	const card = L.cards[lastAtMost(L.cards, i, "start")];
	const row = Math.floor((i - card.start) / card.span);
	return card.y + L.labelH + L.gap + row * (L.tileH + L.gap);
}

// y の位置にある日。スクラバーのラベルが使う。
/**
 * @param {Layout} L
 * @param {number} y
 * @returns {string}
 */
export function dayAtY(L, y) {
	if (L.cards.length === 0) return "";
	return L.cards[lastAtMost(L.cards, y, "y")].d;
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
	const cards = L.cards;
	if (cards.length === 0) return null;

	let i = lastAtMost(cards, top, "y");
	while (i > 0 && cards[i - 1].y === cards[i].y) i--; // ストライプの先頭まで戻る

	const ranges = [];
	for (; i < cards.length; i++) {
		const card = cards[i];
		if (card.y > bottom) break;

		const tileTop = card.y + L.labelH + L.gap;
		let firstRow = 0;
		let lastRow = card.rows - 1;
		if (card.rows > 1) {
			firstRow = Math.max(0, Math.floor((top - tileTop) / (L.tileH + L.gap)));
			lastRow = Math.min(
				card.rows - 1,
				Math.floor((bottom - tileTop) / (L.tileH + L.gap)),
			);
			if (lastRow < firstRow) continue; // まるごと範囲外
		} else if (card.y + card.h < top) {
			continue;
		}
		ranges.push({ card, firstRow, lastRow });
	}
	if (ranges.length === 0) return null;

	const f = ranges[0];
	const l = ranges[ranges.length - 1];
	return {
		ranges,
		// 先頭が段の途中から始まるならその段の上端、そうでなければカードの上端
		pasteY:
			f.firstRow > 0
				? f.card.y + L.labelH + L.gap + f.firstRow * (L.tileH + L.gap)
				: f.card.y,
		from: f.card.start + f.firstRow * f.card.span,
		to: Math.min(
			l.card.start + l.card.n,
			l.card.start + (l.lastRow + 1) * l.card.span,
		),
	};
}

// cards のうち、field の値が v 以下である最後のカードの添字。無ければ0。
/**
 * @param {Card[]} cards
 * @param {number} v
 * @param {"start" | "y"} field
 * @returns {number}
 */
function lastAtMost(cards, v, field) {
	let lo = 0;
	let hi = cards.length - 1;
	let found = 0;
	while (lo <= hi) {
		const mid = (lo + hi) >> 1;
		if (cards[mid][field] <= v) {
			found = mid;
			lo = mid + 1;
		} else {
			hi = mid - 1;
		}
	}
	return found;
}
