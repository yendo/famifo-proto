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

import { gallery } from "./gallery.js";

(() => {
	/** @type {HTMLElement | null} */
	const box = document.querySelector("#lightbox");
	if (!box || !gallery) return;

	const img = box.querySelector("img");
	const vid = box.querySelector("video");
	/** @type {HTMLElement} */
	const errBox = box.querySelector(".lb-error");
	const SWIPE_X = 50; // 左右送りとみなす最小移動量(px)
	const SWIPE_Y = 80; // 下スワイプで閉じる最小移動量(px)

	// stopVideo は再生を止めて読み込みも捨てる。src を外すだけではブラウザが
	// 取得を続けるので load() まで呼ぶ。これを忘れると、次の写真を開いても
	// 前の動画の音が鳴り続け、裏でダウンロードも走り続ける。
	function stopVideo() {
		vid.pause();
		vid.removeAttribute("src");
		vid.load();
	}

	let idx = -1;
	let requestSeq = 0; // 連続スワイプで古いurlAtの解決が新しいものを上書きしないための世代番号
	// 自分で積んだ履歴エントリの上にいるか。写真のURLを直接開いた場合は積んで
	// いないので、閉じるときに戻る先が無い。
	let pushed = false;

	// mode は履歴の扱い。"push" は新しいエントリを積む（一覧から開いたとき）、
	// "replace" はいまのエントリのURLだけ差し替える（送りと、写真のURLで
	// 開かれたとき）、"none" は履歴に触らない（popstateから復元するとき）。
	/**
	 * @param {number} i
	 * @param {"push" | "replace" | "none"} mode
	 */
	async function show(i, mode) {
		if (i < 0 || i >= gallery.total) return;
		const mySeq = ++requestSeq;
		const url = await gallery.urlAt(i);
		if (!url || mySeq !== requestSeq) return; // 待っている間に追い越されたら破棄
		idx = i;
		errBox.hidden = true;
		if (gallery.isVideoAt(i)) {
			img.hidden = true;
			img.removeAttribute("src");
			vid.pause(); // 動画から動画へ送るとき、前の音が重ならないように
			vid.src = url;
			vid.hidden = false;
			box.classList.add("playing");
		} else {
			stopVideo();
			vid.hidden = true;
			img.src = url;
			img.hidden = false;
			box.classList.remove("playing");
		}
		// 送りで積むと、めくった枚数だけ戻るボタンを押さないとギャラリーへ
		// 帰れなくなる。URLは追随させるが履歴には積まない。
		const page = gallery.pageAt(i) ?? location.pathname;
		if (mode === "push") {
			history.pushState({ i }, "", page);
			pushed = true;
		} else if (mode === "replace") {
			history.replaceState({ i }, "", page);
		}
		gallery.ensureChunk(i + 1); // 次を先読みしておく
		gallery.ensureChunk(i - 1);
	}

	/**
	 * @param {number} i
	 * @param {"push" | "replace" | "none"} mode
	 */
	async function open(i, mode) {
		await show(i, mode);
		if (idx < 0) return;
		box.hidden = false;
		document.body.classList.add("locked");
	}

	// close は閉じるだけで履歴には触らない。呼ぶのは popstate と、戻る先を
	// 持たない requestClose だけである。
	function close() {
		box.hidden = true;
		img.removeAttribute("src");
		stopVideo();
		vid.hidden = true;
		errBox.hidden = true;
		box.classList.remove("playing");
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

	window.addEventListener("popstate", (e) => {
		const i = e.state?.i;
		if (Number.isInteger(i)) {
			// 「進む」で写真のエントリに戻ってきた場合。
			pushed = true;
			open(i, "none").catch(() => {});
			return;
		}
		if (!box.hidden) close();
	});

	document.addEventListener("click", (e) => {
		/** @type {HTMLElement | null} */
		const tile = /** @type {Element} */ (e.target).closest("#window .tile");
		if (!tile) return;
		e.preventDefault();
		const i = Number(tile.dataset.i);
		if (!Number.isInteger(i)) return; // 通し番号が無いタイルは無視する。
		// NaN は i < 0 も i >= total も満たさず、
		// offset=NaN のリクエストまで素通りする
		open(i, "push").catch(() => {});
	});

	// 再生できない形式（HEVCを出せない端末）では要素は作られたまま失敗し、画面が
	// 真っ黒になる。何が起きたのか分かるように文を出す。
	vid.addEventListener("error", () => {
		if (!vid.hidden) errBox.hidden = false;
	});

	box.addEventListener("click", (e) => {
		// 再生コントロールの操作を、閉じる動作と取り違えない
		if (/** @type {Element} */ (e.target).closest("video")) return;
		if (/** @type {Element} */ (e.target).closest(".lb-prev")) {
			show(idx - 1, "replace");
			return;
		}
		if (/** @type {Element} */ (e.target).closest(".lb-next")) {
			show(idx + 1, "replace");
			return;
		}
		requestClose();
	});

	document.addEventListener("keydown", (e) => {
		if (box.hidden) return;
		if (e.key === "Escape") requestClose();
		else if (e.key === "ArrowRight") show(idx + 1, "replace");
		else if (e.key === "ArrowLeft") show(idx - 1, "replace");
	});

	let startX = 0;
	let startY = 0;
	let tracking = false;

	box.addEventListener(
		"touchstart",
		(e) => {
			// 2本指はピンチズーム。ブラウザに任せる
			tracking = e.touches.length === 1;
			// シークバーのドラッグを左右スワイプと取り違えない
			if (/** @type {Element} */ (e.target).closest("video")) tracking = false;
			if (!tracking) return;
			startX = e.touches[0].clientX;
			startY = e.touches[0].clientY;
		},
		{ passive: true },
	);

	box.addEventListener(
		"touchend",
		(e) => {
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
		},
		{ passive: true },
	);

	// 写真ごとのURLで開かれた場合。サーバーが通し番号を埋めているので、背後の
	// 一覧をその位置に合わせてから開く。閉じたときにその写真の場所が見える。
	if (gallery.openIndex >= 0) {
		gallery.jumpTo(gallery.openIndex);
		open(gallery.openIndex, "replace").catch(() => {});
	}
})();
