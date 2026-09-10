// Package session は署名付きCookieの組み立てと検証を担う。
//
// OIDCもHTTPも知らない。鍵と時刻を渡されて、文字列に署名し、あとで
// 取り出せるようにするだけである。何を載せるかは呼び出し側が決める。
package session

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// KeyLen は署名鍵の長さ。HMAC-SHA256 のブロックに収まる大きさである。
const KeyLen = 32

// Codec は署名鍵を持ち、payloadに失効時刻を添えて署名する。
type Codec struct{ key []byte }

// NewCodec は鍵を確かめてCodecを作る。
func NewCodec(key []byte) (*Codec, error) {
	if len(key) != KeyLen {
		return nil, fmt.Errorf("the signing key must be %d bytes, got %d", KeyLen, len(key))
	}
	// 呼び出し側が後から書き換えても影響しないよう写しを持つ。
	k := make([]byte, KeyLen)
	copy(k, key)
	return &Codec{key: k}, nil
}

// Sign はpayloadと失効時刻を1つの文字列にまとめて署名する。
// 中身は誰でも読めるが、改竄はできない。秘密は載せないこと。
func (c *Codec) Sign(payload string, expiry time.Time) string {
	// 失効時刻を先に置く。payloadに改行が混ざっても最初の1つで切り出せる。
	raw := strconv.FormatInt(expiry.Unix(), 10) + "\n" + payload
	body := base64.RawURLEncoding.EncodeToString([]byte(raw))
	return body + "." + base64.RawURLEncoding.EncodeToString(c.mac(body))
}

// Verify は署名と失効時刻を確かめてpayloadを返す。
func (c *Codec) Verify(value string, now time.Time) (string, bool) {
	body, sig, found := strings.Cut(value, ".")
	if !found {
		return "", false
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return "", false
	}
	// 一致する接頭辞の長さが実行時間に出ないよう、定数時間で比べる。
	if !hmac.Equal(got, c.mac(body)) {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return "", false
	}
	expStr, payload, found := strings.Cut(string(raw), "\n")
	if !found {
		return "", false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return "", false
	}
	if !now.Before(time.Unix(exp, 0)) {
		return "", false
	}
	return payload, true
}

func (c *Codec) mac(body string) []byte {
	m := hmac.New(sha256.New, c.key)
	m.Write([]byte(body))
	return m.Sum(nil)
}

// LoadOrCreateKey は署名鍵を読む。無ければ作って書く。
//
// このファイルがあるおかげで、famifoを再起動しても利用者はログインし直さずに済む。
// 逆に、消して再起動すれば全端末が一斉にログアウトする。個別の失効ができない
// 構えなので、これが唯一の一括失効手段である。
func LoadOrCreateKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	if err == nil {
		if len(key) != KeyLen {
			return nil, fmt.Errorf("the signing key in %s must be %d bytes, got %d", path, KeyLen, len(key))
		}
		return key, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("cannot read the signing key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("cannot create the directory for the signing key: %w", err)
	}
	key = make([]byte, KeyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("cannot generate the signing key: %w", err)
	}
	// 0600。読めた者はセッションを偽造できる。
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, fmt.Errorf("cannot write the signing key: %w", err)
	}
	return key, nil
}
