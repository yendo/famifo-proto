// Package synology はSynology NASが写真の隣に作る @eaDir の規約を扱う。
// パスの組み立てと存在確認だけで、ファイルの中身は読まない。
//
// @eaDir は読むだけ。famifoはSynology Photosの領域に書き込みも削除もしない。
package synology

import (
	"os"
	"path/filepath"
)

// eaDir はSynologyがサムネイルなどを置く管理用ディレクトリの名前。
const eaDir = "@eaDir"

// thumbName は一覧に借りるサムネイルのファイル名。Mは短辺320px（4:3なら
// 長辺427px）で、famifo自身が作る長辺480pxよりわずかに小さい。
const thumbName = "SYNOPHOTO_THUMB_M.jpg"

// largeName は拡大表示に借りるJPEGのファイル名。長辺1707px・約1MBで、
// 一覧には過大だが1枚だけ見せる場面では妥当な大きさになる。
const largeName = "SYNOPHOTO_THUMB_XL.jpg"

// entryPath は @eaDir の中の1ファイルのパスを組み立てる。
func entryPath(srcPath, name string) string {
	return filepath.Join(filepath.Dir(srcPath), eaDir, filepath.Base(srcPath), name)
}

// ThumbPath はSynologyがsrcPathの写真用に持つ一覧用サムネイルのパスを返す。
// 実在するとは限らない。あるかどうかは HasThumb で確かめる。
func ThumbPath(srcPath string) string { return entryPath(srcPath, thumbName) }

// LargePath はSynologyがsrcPathの写真用に持つ拡大表示用JPEGのパスを返す。
// ThumbPath と同じディレクトリを指す。存在は確かめない。MとXLは同じ生成器が
// 一緒に書くため、Mがあることを確かめてあればXLもあるものとして扱う。
func LargePath(srcPath string) string { return entryPath(srcPath, largeName) }

// HasThumb は借りられるサムネイルがあるかを報告する。
//
// DSM 7.3 はHEICをデコードできず、.jpg の代わりに0バイトの .fail を置く。拡張子が
// 違うので存在確認だけで弾けるが、手で消したあとに空の .jpg が残るような状況も
// あるため、通常ファイルかつ中身があることまで見る。
func HasThumb(srcPath string) bool {
	fi, err := os.Stat(ThumbPath(srcPath))
	return err == nil && fi.Mode().IsRegular() && fi.Size() > 0
}
