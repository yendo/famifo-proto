package owner_test

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/owner"
)

// 持ち主はファイルの所有者である。テストが書いたファイルの所有者は、テストを
// 走らせている自分になる。別の uid へ chown するには root が要るので扱わない。
func TestUIDIsTheFileOwner(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "a.jpg")
	require.NoError(t, os.WriteFile(path, nil, 0o644))
	fi, err := os.Lstat(path)
	require.NoError(t, err)

	require.Equal(t, uint32(os.Getuid()), owner.UID(fi))
}

// 名前はアカウントのフルネーム（GECOS）で、空ならアカウント名にする。
func TestNameUsesTheFullNameFirst(t *testing.T) {
	t.Parallel()
	uid := os.Getuid()
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		t.Skipf("the test user has no account entry: %v", err)
	}
	want := u.Name
	if want == "" {
		want = u.Username
	}

	require.Equal(t, want, owner.Name(uint32(uid)))
}

// nobody は /etc/passwd に行があっても出さない。userns-remap や rootless Docker では
// 範囲外の uid がこれに潰れて見え、誰の写真か分からないのに「nobody」と出てしまう。
func TestNameIsEmptyForNobody(t *testing.T) {
	t.Parallel()

	require.Empty(t, owner.Name(65534))
}

func TestNameIsEmptyWithoutAnAccount(t *testing.T) {
	t.Parallel()

	require.Empty(t, owner.Name(unknownUID(t)))
}

// 開発機とCIには /etc/passwd がある。無い側はパスが固定なので確かめない。
func TestCheckPassesWhereThereIsAnAccountList(t *testing.T) {
	t.Parallel()
	if _, err := os.Stat("/etc/passwd"); err != nil {
		t.Skipf("no /etc/passwd here: %v", err)
	}

	require.NoError(t, owner.Check())
}

// unknownUID はアカウントの無い uid を返す。どの uid が空いているかは環境で
// 変わるので、決め打ちせずに探す。
func unknownUID(t *testing.T) uint32 {
	t.Helper()
	for uid := uint32(2_000_000_000); uid < 2_000_000_100; uid++ {
		_, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
		var unknown user.UnknownUserIdError
		if errors.As(err, &unknown) {
			return uid
		}
	}
	t.Fatal("no uid without an account was found")
	return 0
}
