// @ts-check

// 入口。gallery.html が読むのはこのファイルだけで、各機能は import した
// 時点で動き出す。仮想スクロール（gallery.js）は両方から import されるので
// 先に評価される。
import "./lightbox.js";
import "./scrubber.js";
