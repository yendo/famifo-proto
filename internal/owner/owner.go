// Package owner は写真の持ち主を扱う。famifo はファイルの所有者を撮った人と
// みなし、その uid を名前に変えて見せる。
//
// 名前の出どころは /etc/passwd である。cgo を使わないビルドでは os/user が
// /etc/passwd を自分で読む。scratch のイメージには /etc/passwd が無いので、
// ホストのものをマウントしない限り名前は出ない。
package owner

import (
	"io/fs"
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// nobodyUID は nobody の uid。所有者がこれなら名前を出さない。userns-remap や
// rootless Docker では、範囲外の uid がこれに潰れて見える。誰の写真か分からない
// のに、/etc/passwd に nobody の行があると「nobody」と出てしまう。
const nobodyUID = 65534

// UID はファイルの持ち主の uid を返す。fi は os.Lstat などの結果で、Linux では
// *syscall.Stat_t を持つ。ビルド対象は Linux だけである。
//
// fi を受け取るのは、取り込みが既に Lstat しているからである。ここで読み直すと
// 1 回余分に stat することになる。
func UID(fi fs.FileInfo) uint32 {
	return fi.Sys().(*syscall.Stat_t).Uid
}

// Name は uid の持ち主の表示名を返す。出せる名前が無ければ空を返す。
//
// フルネーム（GECOS）を先に使い、空ならアカウント名にする。
func Name(uid uint32) string {
	if uid == nobodyUID {
		return ""
	}
	u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
	if err != nil {
		return ""
	}
	if u.Name != "" {
		return u.Name
	}
	return u.Username
}

// Check は名前の出どころ（/etc/passwd）を開けるかを確かめる。開けなければ、
// その理由のエラーを返す。起動時に呼び、マウントし忘れや読めない理由を知らせる
// ために使う。開けるかだけを見て、中身は解析しない。
func Check() error {
	f, err := os.Open("/etc/passwd")
	if err != nil {
		return err
	}
	return f.Close()
}
