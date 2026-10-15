// @ts-check

// ライトボックス。仮想スクロールではDOM上に可視範囲のタイルしか無いため、
// 全体の通し番号で動かす。そうしないと窓枠の端でスワイプが止まる。
//
// 開いている写真はアドレス欄にも出る（/item/<id>）。ページは遷移させず履歴だけを
// 積むので、一覧のスクロール位置は保たれる。閉じる操作は自分で閉じずに
// history.back() を呼び、実際に閉じるのは popstate の1箇所だけにする。2経路で
// 閉じると履歴を二重に消費し、閉じたのにURLが写真のまま残る。
//
// history.scrollRestoration は既定のままにする。リロードでスクロール位置が
// 戻るのはブラウザのその働きによるもので、manualにすると失われる。

import { chunks } from "./chunk.js";
import { formatDateTime } from "./datefmt.js";
import { gallery } from "./gallery.js";

(() => {
	/** @type {HTMLElement | null} */
	const box = document.querySelector("#lightbox");
	if (!box || !gallery || !chunks) return;

	const img = box.querySelector("img");
	const vid = box.querySelector("video");
	/** @type {HTMLElement} */
	const errBox = box.querySelector(".lb-error");
	/** @type {HTMLElement} */
	const owner = box.querySelector(".lb-owner");
	/** @type {HTMLElement} */
	const when = box.querySelector(".lb-when");
	/** @type {HTMLElement} */
	const caption = box.querySelector(".lb-caption");
	// 標準コントロールの高さの目安(px)。app.css の .lb-caption.raised の 4rem と揃える。
	// 実際の高さはブラウザによって少し違う。
	const CONTROLS_H = 64;
	const SWIPE_X = 50; // 左右送りとみなす最小移動量(px)
	const SWIPE_Y = 80; // 下スワイプで閉じる最小移動量(px)

	let idx = -1;
	let requestSeq = 0; // 連続スワイプで古いloadTileの解決が新しいものを上書きしないための世代番号
	// 自分で積んだ履歴エントリの上にいるか。写真のURLを直接開いた場合は積んで
	// いないので、閉じるときに戻る先が無い。
	let pushed = false;
	let startX = 0;
	let startY = 0;
	let tracking = false;

	/** @param {PopStateEvent} e */
	function onPopState(e) {
		const i = e.state?.i;
		if (Number.isInteger(i)) {
			// 「進む」で写真のエントリに戻ってきた場合。
			pushed = true;
			open(i, "none").catch(() => {});
			return;
		}
		if (!box.hidden) close();
	}

	/** @param {MouseEvent} e */
	function onTileClick(e) {
		/** @type {HTMLElement | null} */
		const tile = /** @type {Element} */ (e.target).closest("#window .tile");
		if (!tile) return;
		e.preventDefault();
		const i = Number(tile.dataset.i);
		if (!Number.isInteger(i)) return; // 通し番号が無いタイルは無視する。
		// NaN は i < 0 も i >= total も満たさず、
		// chunk=NaN のリクエストまで素通りする
		open(i, "push").catch(() => {});
	}

	// 再生できない形式（HEVCを出せない端末）では要素は作られたまま失敗し、画面が
	// 真っ黒になる。何が起きたのか分かるように文を出す。
	function onVideoError() {
		if (!vid.hidden) errBox.hidden = false;
	}

	/** @param {MouseEvent} e */
	function onBoxClick(e) {
		const target = /** @type {Element} */ (e.target);
		if (target.closest(".lb-back")) {
			requestClose();
			return;
		}
		if (target.closest(".lb-prev")) {
			show(idx - 1, "replace");
			return;
		}
		if (target.closest(".lb-next")) {
			show(idx + 1, "replace");
			return;
		}
		// controls があるときはブラウザに任せる（PCは再生/一時停止）。無いときは
		// 帯を消しているので、出す。
		if (target.closest("video")) {
			if (!vid.controls) setChrome(true);
			return;
		}
		setChrome(!box.classList.contains("chrome"));
	}

	/** @param {KeyboardEvent} e */
	function onKeyDown(e) {
		if (box.hidden) return;
		if (e.key === "Escape") requestClose();
		else if (e.key === "ArrowRight") show(idx + 1, "replace");
		else if (e.key === "ArrowLeft") show(idx - 1, "replace");
	}

	/** @param {TouchEvent} e */
	function onTouchStart(e) {
		// 2本指はピンチズーム。ブラウザに任せる
		tracking = e.touches.length === 1;
		// シークバーのドラッグを左右スワイプと取り違えない
		if (/** @type {Element} */ (e.target).closest("video")) tracking = false;
		if (!tracking) return;
		startX = e.touches[0].clientX;
		startY = e.touches[0].clientY;
	}

	/** @param {TouchEvent} e */
	function onTouchEnd(e) {
		if (!tracking) return;
		tracking = false;
		const t = e.changedTouches[0];
		const dx = t.clientX - startX;
		const dy = t.clientY - startY;

		if (Math.abs(dx) > SWIPE_X && Math.abs(dx) > Math.abs(dy)) {
			show(dx < 0 ? idx + 1 : idx - 1, "replace");
		} else if (dy > SWIPE_Y && Math.abs(dy) > Math.abs(dx)) {
			requestClose();
		}
	}

	/**
	 * @param {number} i
	 * @param {"push" | "replace" | "none"} mode
	 */
	async function open(i, mode) {
		await show(i, mode);
		if (idx < 0) return;
		// 開くときは必ず帯を出す。送りでは前の状態のままにする。
		setChrome(true);
		box.hidden = false;
		document.body.classList.add("locked");
	}

	// mode は履歴の扱い。"push" は新しいエントリを積む（一覧から開いたとき）、
	// "replace" はいまのエントリのURLだけ差し替える（送りと、写真のURLで
	// 開かれたとき）、"none" は履歴に触らない（popstateから復元するとき）。
	/**
	 * @param {number} i
	 * @param {"push" | "replace" | "none"} mode
	 */
	async function show(i, mode) {
		if (i < 0 || i >= chunks.tileCount) return;
		const mySeq = ++requestSeq;
		const tile = await chunks.loadTile(i);
		if (!tile || mySeq !== requestSeq) return; // 待っている間に追い越されたら破棄
		idx = i;
		errBox.hidden = true;
		// 情報は写真より遅れて届く。前の写真の情報を次の写真に残さない。
		clearInfo();
		showInfo(tile.infoUrl, mySeq);
		if (tile.isVideo) {
			img.hidden = true;
			img.removeAttribute("src");
			vid.pause(); // 動画から動画へ送るとき、前の音が重ならないように
			vid.src = tile.fullUrl;
			vid.hidden = false;
		} else {
			stopVideo();
			vid.hidden = true;
			img.src = tile.fullUrl;
			img.hidden = false;
		}
		// 送りで積むと、めくった枚数だけ戻るボタンを押さないとギャラリーへ
		// 帰れなくなる。URLは追随させるが履歴には積まない。
		const page = tile.pageUrl ?? location.pathname;
		if (mode === "push") {
			history.pushState({ i }, "", page);
			pushed = true;
		} else if (mode === "replace") {
			history.replaceState({ i }, "", page);
		}
		chunks.prefetch(i + 1); // 次を先読みしておく
		chunks.prefetch(i - 1);
	}

	// stopVideo は再生を止めて読み込みも捨てる。src を外すだけではブラウザが
	// 取得を続けるので load() まで呼ぶ。これを忘れると、次の写真を開いても
	// 前の動画の音が鳴り続け、裏でダウンロードも走り続ける。
	function stopVideo() {
		vid.pause();
		vid.removeAttribute("src");
		vid.load();
	}

	// setChrome は帯（上のボタン、左下の2行、前後のボタン）を出すか消すかを決める。
	// 動画の標準コントロールも帯の一部として一緒に出し入れする。
	/** @param {boolean} on */
	function setChrome(on) {
		box.classList.toggle("chrome", on);
		vid.controls = on;
		placeCaption();
	}

	// placeCaption は左下の2行が動画の標準コントロールと重なるときだけ上げる。
	// コントロールは画面の下端ではなく動画の下端に描かれる。横長の動画を縦の画面で
	// 見ると動画は中ほどで終わり、文字とは重ならない。常に上げると浮いて見える。
	function placeCaption() {
		caption.classList.remove("raised");
		if (vid.hidden || !vid.controls) return;
		const c = caption.getBoundingClientRect();
		const v = vid.getBoundingClientRect();
		const hit = c.bottom > v.bottom - CONTROLS_H && c.top < v.bottom
			&& c.left < v.right && c.right > v.left;
		caption.classList.toggle("raised", hit);
	}

	// showInfo は左下の2行（撮影者と撮影日時）を取って出す。写真の表示は待たせない。
	// 待っている間に別の写真へ送られたか閉じられたら、届いた情報は捨てる。
	// 取れなかったときは隠したままにする。情報が無くても写真は見られる。
	/**
	 * @param {string} url
	 * @param {number} seq
	 */
	async function showInfo(url, seq) {
		try {
			const res = await fetch(url);
			if (!res.ok) return;
			/** @type {{ owner?: string, takenAt: string }} */
			const info = await res.json();
			if (seq !== requestSeq) return;
			if (info.owner) {
				owner.textContent = info.owner;
				owner.hidden = false;
			}
			when.textContent = formatDateTime(info.takenAt);
			when.hidden = !when.textContent;
			placeCaption();
		} catch {
			// 通信の失敗。隠したままにする
		}
	}

	function clearInfo() {
		owner.hidden = true;
		owner.textContent = "";
		when.hidden = true;
		when.textContent = "";
	}

	// close は閉じるだけで履歴には触らない。呼ぶのは popstate と、戻る先を
	// 持たない requestClose だけである。
	function close() {
		box.hidden = true;
		img.removeAttribute("src");
		stopVideo();
		vid.hidden = true;
		errBox.hidden = true;
		clearInfo();
		document.body.classList.remove("locked");
		idx = -1;
		requestSeq++; // 閉じた後に届く古い解決を破棄する
		pushed = false;
	}

	// requestClose は「閉じたい」という要求。積んだエントリがあるなら1つ戻り、
	// popstate に閉じさせる。戻る先が無い（写真のURLを直接開いた）ときだけ、
	// URLをギャラリーに直してから自分で閉じる。
	function requestClose() {
		if (pushed) {
			history.back();
			return;
		}
		history.replaceState(null, "", "/");
		close();
	}

	window.addEventListener("popstate", onPopState);
	document.addEventListener("click", onTileClick);
	vid.addEventListener("error", onVideoError);
	box.addEventListener("click", onBoxClick);
	document.addEventListener("keydown", onKeyDown);
	box.addEventListener("touchstart", onTouchStart, { passive: true });
	box.addEventListener("touchend", onTouchEnd, { passive: true });

	// 動画の大きさはメタデータが届いてから決まり、画面を回すと lightbox の大きさが
	// 変わる。どちらでも重なりが変わるので判定し直す。
	const resize = new ResizeObserver(placeCaption);
	resize.observe(box);
	resize.observe(vid);

	// 写真ごとのURLで開かれた場合。サーバーが通し番号を埋めているので、背後の
	// 一覧をその位置に合わせてから開く。閉じたときにその写真の場所が見える。
	if (gallery.openIndex >= 0) {
		gallery.jumpTo(gallery.openIndex);
		open(gallery.openIndex, "replace").catch(() => {});
	}
})();
