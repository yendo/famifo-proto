// @ts-check

// 入口。index.html が読むのはこのファイルだけで、各機能は import した
// 時点で動き出す。ライトボックスとスクラバーも gallery.js を import するが、
// それに頼らずギャラリーの本体であることをここで明示する。
import "./gallery.js";
import "./lightbox.js";
import "./scrubber.js";
