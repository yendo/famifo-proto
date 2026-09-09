// Package synology はSynology NASが写真や動画の隣に作る @eaDir の規約を扱う。
// パスの組み立てと存在確認だけで、ファイルの中身は読まない。
//
// @eaDir は読むだけ。famifoはSynology Photosの領域に書き込みも削除もしない。
package synology

import (
	"os"
	"path/filepath"
	"strings"
)

// eaDir はSynologyがサムネイルなどを置く管理用ディレクトリの名前。
const eaDir = "@eaDir"

// thumbMName は一覧に借りるサムネイルのファイル名。Mは短辺320px（4:3なら
// 長辺427px）で、famifo自身が作る長辺480pxよりわずかに小さい。
const thumbMName = "SYNOPHOTO_THUMB_M.jpg"

// thumbXLName は拡大表示に借りるJPEGのファイル名。長辺1707px・約1MBで、
// 一覧には過大だが1枚だけ見せる場面では妥当な大きさになる。
const thumbXLName = "SYNOPHOTO_THUMB_XL.jpg"

// filmName はブラウザ互換のために借りる変換済み動画のファイル名。
//
// PixelもiPhoneもHEVCを吐くが、HEVCを再生できるかは端末にハードウェアデコーダが
// あるかで決まる。DSMが作るこのH.264版（実測1280x720・3.6MB、原本はHEVCで15.9MB）を
// 配ればどの端末でも再生できる。HEICで SYNOPHOTO_THUMB_XL.jpg を借りているのと
// 同じ構えである。
//
// 他の解像度の変換版がある可能性はあるが、実物を1本しか見ていないので推測で名前を
// 並べない。この名前が無ければ借りるのをやめて原本に落ちる。
const filmName = "SYNOPHOTO_FILM_H.mp4"

// ThumbMPath はSynologyがsrcPathの写真用に持つ一覧用サムネイル（M）のパスを返す。
// 実在するとは限らない。あるかどうかは HasThumbM で確かめる。
func ThumbMPath(srcPath string) string { return entryPath(srcPath, thumbMName) }

// ThumbXLPath はSynologyがsrcPathの写真用に持つ拡大表示用JPEG（XL）のパスを返す。
// ThumbMPath と同じディレクトリを指す。存在は確かめない。MとXLは同じ生成器が
// 一緒に書くため、Mがあることを確かめてあればXLもあるものとして扱う。
func ThumbXLPath(srcPath string) string { return entryPath(srcPath, thumbXLName) }

// FilmPath はSynologyがsrcPathの動画から作った変換版のパスを返す。
// 実在するとは限らない。あるかどうかは HasFilm で確かめる。
func FilmPath(srcPath string) string { return entryPath(srcPath, filmName) }

// HasFilm は借りられる変換版があるかを報告する。
//
// サムネイルの有無からは導けない。実測した @eaDir には SYNOPHOTO_THUMB_M.jpg が
// あるのに SYNOPHOTO_FILM.fail があり、サムネイル生成と動画変換が別の工程で
// 片方だけ失敗しうることが分かる。MとXLのように一方から他方を導けない。
//
// DSMは失敗すると0バイトの .fail を置くので、HasThumbM と同じく中身があることまで見る。
func HasFilm(srcPath string) bool {
	fi, err := os.Stat(FilmPath(srcPath))
	return err == nil && fi.Mode().IsRegular() && fi.Size() > 0
}

// HasThumbM は借りられるサムネイル（M）があるかを報告する。
//
// DSM 7.3 はHEICをデコードできず、.jpg の代わりに0バイトの .fail を置く。拡張子が
// 違うので存在確認だけで弾けるが、手で消したあとに空の .jpg が残るような状況も
// あるため、通常ファイルかつ中身があることまで見る。
func HasThumbM(srcPath string) bool {
	fi, err := os.Stat(ThumbMPath(srcPath))
	return err == nil && fi.Mode().IsRegular() && fi.Size() > 0
}

// entryPath は @eaDir の中の1ファイルのパスを組み立てる。
func entryPath(srcPath, name string) string {
	return filepath.Join(filepath.Dir(srcPath), eaDir, filepath.Base(srcPath), name)
}

// managedDirs はSynologyが写真ディレクトリの中に作る管理用ディレクトリ。
// 中身は写真と同じ拡張子を持つが写真ではないので、降りると1枚が複数枚に見える。
var managedDirs = map[string]bool{
	// 写真1枚につき @eaDir/<ファイル名>/SYNOPHOTO_THUMB_*.jpg を作る。
	// EXIFが無いためmtimeに落ち、生成した日に大量の重複が積み上がる。
	eaDir: true,
	// Synologyのゴミ箱。削除した写真が一覧に復活する。
	"#recycle": true,
}

// IsManagedDir はディレクトリ名が走査対象外かを報告する。
func IsManagedDir(name string) bool { return managedDirs[name] }

// InManagedDir はパスの途中に管理用ディレクトリが挟まっているかを報告する。
// 走査は fs.SkipDir で降りずに済むが、fsnotify のイベントは個々のパスで
// 届くためこちらで判定する必要がある。
func InManagedDir(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if managedDirs[part] {
			return true
		}
	}
	return false
}
