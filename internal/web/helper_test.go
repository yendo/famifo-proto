package web_test

import (
	"os"
	"testing"

	"github.com/yendo/famifo-proto/internal/owner"
)

// twoNamedOwners は名前の引ける uid を2つ返す。テストを走らせている自分と root で
// ある。撮影者の節には名前の引ける人しか並ばないので、2人を並べるテストに使う。
// 自分が root のときや、どちらかに名前が無い環境では skip する。
func twoNamedOwners(t *testing.T) (me, root uint32) {
	t.Helper()
	me = uint32(os.Getuid())
	if me == 0 {
		t.Skip("running as root, so there is only one named owner")
	}
	if owner.Name(me) == "" || owner.Name(0) == "" {
		t.Skip("the test user or root has no account entry, so there are no names to list")
	}
	return me, 0
}
