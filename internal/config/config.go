// Package config はアプリの実行時設定の保持と検証を担う。
// 設定値をどこから読むか（コマンドライン引数の解析）は呼び出し側の責務。
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config はアプリの実行時設定。すべてコマンドライン引数から与えられる。
type Config struct {
	PhotoDirs   []string // 写真を収集するルートディレクトリ（複数可）
	DataDir     string   // DBとサムネイルの置き場
	Addr        string   // HTTPの待ち受けアドレス
	ScanWorkers int      // 同時に取り込む枚数（スキャンとfsnotifyの追従に共通）
	// ScanInterval はインデックスをディスクの実態と突き合わせ直す間隔。
	// fsnotify の取りこぼしはこれで回復する。
	ScanInterval time.Duration

	// OIDCIssuer は認証に使う IdP の issuer。空なら認証しない。
	// ポート番号を含める必要がある。Synology SSO Server では
	// "https://<host>:5001/webman/sso" の形になる。省くと discovery は引けるが
	// トークン交換が 405 で失敗する。
	OIDCIssuer string
	// OIDCClientID は IdP に登録したクライアントID。
	OIDCClientID string
	// OIDCClientSecret は IdP に登録したクライアントの秘密。
	// フラグでは受け取らない。コマンドライン引数は同じホストの誰からでも
	// /proc で読めるためである。
	OIDCClientSecret string
	// ExternalURL は famifo が外から見えるURL。redirect_uri の組み立てに使う。
	// この scheme が https のとき Cookie に Secure を付ける。
	ExternalURL string
}

// Validate は設定の不備を報告する。ここでのエラーは起動を中止させる。
func (c Config) Validate() error {
	if len(c.PhotoDirs) == 0 {
		return errors.New("-dir is required")
	}
	for i, dir := range c.PhotoDirs {
		fi, err := os.Stat(dir)
		if err != nil {
			// ':' はUnixのパスに使える文字なので、それを含むディレクトリを
			// 渡すと意図しない位置で切れる。分割結果を見せて原因を読めるようにする。
			return fmt.Errorf("cannot read -dir: %w (-dir was split on %q into: %v)",
				err, string(filepath.ListSeparator), c.PhotoDirs)
		}
		if !fi.IsDir() {
			return fmt.Errorf("-dir is not a directory: %s", dir)
		}
		// 同じルートを2回走査しても無駄なだけ。入れ子は同じファイルを2回
		// 走査し、サムネイルを2回作る。
		for _, other := range c.PhotoDirs[i+1:] {
			nested, err := dirContains(dir, other)
			if err != nil {
				return fmt.Errorf("cannot resolve -dir: %w", err)
			}
			if !nested {
				if nested, err = dirContains(other, dir); err != nil {
					return fmt.Errorf("cannot resolve -dir: %w", err)
				}
			}
			if nested {
				return fmt.Errorf("-dir entries are duplicated or nested: %s and %s", dir, other)
			}
		}
	}
	if c.Addr == "" {
		return errors.New("-addr is required")
	}
	// 0を「自動」と読み替えない。既定値はフラグの側が runtime.NumCPU() で
	// 与えており、0が届くのは利用者が明示的に0を渡したときだけである。
	// 黙って読み替えると、走査が始まらない設定を無言で書き換えることになる。
	if c.ScanWorkers < 1 {
		return fmt.Errorf("-scan-workers must be 1 or greater: %d", c.ScanWorkers)
	}
	// 0を「無効」と読み替えない。待たずに走査を繰り返すことになり、それが
	// 止まらなくなる。回したくなければ十分に長い値を渡せばよい。
	if c.ScanInterval <= 0 {
		return fmt.Errorf("-scan-interval must be positive: %s", c.ScanInterval)
	}
	for _, dir := range c.PhotoDirs {
		inside, err := dirContains(dir, c.DataDir)
		if err != nil {
			return fmt.Errorf("cannot resolve -data: %w", err)
		}
		if inside {
			return fmt.Errorf("-data must sit outside every -dir, or it indexes itself: %s is inside %s", c.DataDir, dir)
		}
	}
	if c.OIDCIssuer != "" {
		// 一部だけ揃った状態を黙って無認証に落とさない。認証したつもりの構成が
		// 素通しで配信されるのが、いちばん困る壊れ方である。
		if c.OIDCClientID == "" {
			return errors.New("-oidc-client-id is required when -oidc-issuer is given")
		}
		if c.OIDCClientSecret == "" {
			return errors.New("FAMIFO_OIDC_CLIENT_SECRET is required when -oidc-issuer is given")
		}
		if c.ExternalURL == "" {
			return errors.New("-external-url is required when -oidc-issuer is given")
		}
		u, err := url.Parse(c.ExternalURL)
		if err != nil {
			return fmt.Errorf("-external-url is not a URL: %w", err)
		}
		if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("-external-url must be an absolute http or https URL: %s", c.ExternalURL)
		}
	}
	return nil
}

// dirContains はabsパスに変換したうえで、innerがouter自身か、その配下にあるかを判定する。
// filepath.Relを使うのは文字列プレフィックス比較を避けるため
// （例えば "/photos-data" は "/photos" の中ではない）。
func dirContains(outer, inner string) (bool, error) {
	absOuter, err := filepath.Abs(outer)
	if err != nil {
		return false, err
	}
	absInner, err := filepath.Abs(inner)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(absOuter, absInner)
	if err != nil {
		return false, err
	}
	if rel == "." {
		return true, nil // 同一ディレクトリ
	}
	first, _, _ := strings.Cut(rel, string(filepath.Separator))
	return first != "..", nil
}

// DBPath はSQLiteファイルのパスを返す。
func (c Config) DBPath() string { return filepath.Join(c.DataDir, "famifo.db") }

// ThumbDir は生成したサムネイルのルートを返す。
func (c Config) ThumbDir() string { return filepath.Join(c.DataDir, "thumbs") }

// SessionKeyPath はセッションの署名鍵の置き場を返す。
func (c Config) SessionKeyPath() string { return filepath.Join(c.DataDir, "session.key") }

// RedirectURI は IdP に登録する戻り先を組み立てる。
// ここで組み立てた文字列と、IdP 側に登録した文字列は完全に一致していなければならない。
func (c Config) RedirectURI() string {
	return strings.TrimSuffix(c.ExternalURL, "/") + "/auth/callback"
}

// CookieSecure は Cookie に Secure を付けるかを返す。
// ヘッダからは推測しない。設定した外部URLの scheme だけで決める。
// Validate と同じ net/url での解釈に揃える。url.Parse は scheme を小文字化するので、
// "HTTPS://..." のような大文字混じりの入力でも文字列プレフィックス比較のように
// 見落とさない。
func (c Config) CookieSecure() bool {
	u, err := url.Parse(c.ExternalURL)
	return err == nil && u.Scheme == "https"
}
