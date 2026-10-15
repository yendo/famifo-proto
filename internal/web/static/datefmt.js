// @ts-check

// 日付と日時の表示用の書式。一覧の日付ラベルと全画面の日時で同じ規則を使う。
// 文字列は Date に通さず、数字に分解して使う。時差の無い文字列をブラウザに
// 解釈させると、書式によっては UTC として扱われてずれる。

// "2026-02-08" → "2026年2月8日"。今年なら年を省く。
// 一覧では最狭の1列(CSSの最小値110px)に収めるため、これ以上長い表記にはしない。
/**
 * @param {string} d
 * @returns {string}
 */
export function formatDay(d) {
	if (!d) return "";
	const [y, m, day] = d.split("-");
	const head = Number(y) === new Date().getFullYear() ? "" : `${y}年`;
	return `${head}${Number(m)}月${Number(day)}日`;
}

// "2026-02-08T09:05:30" → "2026年2月8日 9:05"。日付の部分は formatDay と同じ。
/**
 * @param {string} t
 * @returns {string}
 */
export function formatDateTime(t) {
	if (!t) return "";
	const [day, time] = t.split("T");
	const [hh, mm] = time.split(":");
	return `${formatDay(day)} ${Number(hh)}:${mm}`;
}
