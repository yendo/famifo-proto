package session_test

// 署名付きCookieの組み立てと検証を確かめる。鍵と時刻を注入するので、
// 実時間にも実ファイルにも依存しない（鍵ファイルのテストだけは実ファイルを使う）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/session"
)

func newCodec(t *testing.T) *session.Codec {
	t.Helper()
	key := make([]byte, session.KeyLen)
	for i := range key {
		key[i] = byte(i)
	}
	c, err := session.NewCodec(key, "test")
	require.NoError(t, err)
	return c
}

func TestSignAndVerifyRoundTrip(t *testing.T) {
	c := newCodec(t)
	now := time.Unix(1_700_000_000, 0)

	v := c.Sign("yendo", now.Add(time.Hour))

	got, ok := c.Verify(v, now)
	require.True(t, ok)
	require.Equal(t, "yendo", got)
}

func TestVerifyRejectsExpired(t *testing.T) {
	c := newCodec(t)
	now := time.Unix(1_700_000_000, 0)

	v := c.Sign("yendo", now.Add(time.Hour))

	_, ok := c.Verify(v, now.Add(2*time.Hour))
	require.False(t, ok)
}

func TestVerifyRejectsTamperedPayload(t *testing.T) {
	c := newCodec(t)
	now := time.Unix(1_700_000_000, 0)

	v := c.Sign("yendo", now.Add(time.Hour))
	// 本体の1バイトを変える。署名が合わなくなる。
	tampered := []byte(v)
	tampered[0] ^= 0x01
	v = string(tampered)

	_, ok := c.Verify(v, now)
	require.False(t, ok)
}

func TestVerifyRejectsAnotherKey(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	v := newCodec(t).Sign("yendo", now.Add(time.Hour))

	other := make([]byte, session.KeyLen) // すべて0の別の鍵
	oc, err := session.NewCodec(other, "test")
	require.NoError(t, err)

	_, ok := oc.Verify(v, now)
	require.False(t, ok)
}

func TestVerifyRejectsAnotherPurpose(t *testing.T) {
	// 用途ごとに鍵を分けるのは、ログインの往復用Cookieがそのままセッション
	// Cookieとして通ってしまう事態を防ぐため。
	key := make([]byte, session.KeyLen)
	for i := range key {
		key[i] = byte(i)
	}
	now := time.Unix(1_700_000_000, 0)

	sessionCodec, err := session.NewCodec(key, "session")
	require.NoError(t, err)
	flowCodec, err := session.NewCodec(key, "flow")
	require.NoError(t, err)

	v := flowCodec.Sign("yendo", now.Add(time.Hour))
	_, ok := sessionCodec.Verify(v, now)
	require.False(t, ok, "a value signed for one purpose must not verify for another")
}

func TestVerifyRejectsMalformed(t *testing.T) {
	c := newCodec(t)
	now := time.Unix(1_700_000_000, 0)

	for _, v := range []string{"", ".", "nodot", "not-base64.also-not", strings.Repeat("a", 100)} {
		_, ok := c.Verify(v, now)
		require.False(t, ok, "must reject %q", v)
	}
}

func TestPayloadMayContainNewlines(t *testing.T) {
	// 一時状態はJSONを載せる。将来改行が混ざっても壊れないことを固定する。
	c := newCodec(t)
	now := time.Unix(1_700_000_000, 0)
	payload := "{\n  \"state\": \"x\"\n}"

	got, ok := c.Verify(c.Sign(payload, now.Add(time.Hour)), now)
	require.True(t, ok)
	require.Equal(t, payload, got)
}

func TestNewCodecRejectsWrongKeyLength(t *testing.T) {
	_, err := session.NewCodec([]byte("short"), "test")
	require.Error(t, err)
}

func TestNewCodecRejectsEmptyPurpose(t *testing.T) {
	_, err := session.NewCodec(make([]byte, session.KeyLen), "")
	require.Error(t, err)
}

func TestLoadOrCreateKeyCreatesAndReuses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "session.key")

	first, err := session.LoadOrCreateKey(path)
	require.NoError(t, err)
	require.Len(t, first, session.KeyLen)

	fi, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())

	second, err := session.LoadOrCreateKey(path)
	require.NoError(t, err)
	require.Equal(t, first, second, "the key must survive a restart")
}

func TestLoadOrCreateKeyRejectsWrongSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.key")
	require.NoError(t, os.WriteFile(path, []byte("too short"), 0o600))

	_, err := session.LoadOrCreateKey(path)
	require.Error(t, err)
}
