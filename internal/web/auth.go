package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/yendo/famifo-proto/internal/oidcauth"
)

// sessionTTL はログイン状態が続く長さ。
//
// 使うたびに延ばすスライディング方式にすると、リクエストごとに Set-Cookie を出すか、
// 残り時間を見て再発行する分岐が要る。家族が30日ごとに1回入れ直す程度なら固定で足りる。
// 利用者が変えられる設定ではない。
const sessionTTL = 30 * 24 * time.Hour

// flowTTL は認可の往復に許す時間。ログイン画面を開いたまま放置した場合の上限になる。
const flowTTL = 10 * time.Minute

const (
	sessionCookie = "famifo_session"
	flowCookie    = "famifo_oidc"
)

// Provider は認可の往復を担う。oidcauth.Client がこれを満たす。
// インターフェースにしてあるのは、テストがIdPを立てずに済むようにするためである。
type Provider interface {
	AuthURL(oidcauth.Params) string
	Exchange(ctx context.Context, code string, p oidcauth.Params) (oidcauth.Identity, error)
	// LogoutURL はRP-Initiated Logoutの宛先を返す。IdPがdiscoveryで
	// end_session_endpointを広告していなければfalseを返す。
	LogoutURL(postLogoutRedirectURI string) (string, bool)
}

// Auth は認証の手段をまとめる。NewServer に nil を渡すと認証しない。
type Auth struct {
	OIDC   Provider
	Key    []byte // session.KeyLen バイト。webが用途ごとのCodecを導出する
	Secure bool   // Cookie に Secure を付けるか。外部URLがhttpsのときだけ真
	// ExternalURL はfamifoが外から見えるURL。RP-Initiated Logoutの
	// post_logout_redirect_uriを組み立てるのに使う。
	ExternalURL string
}

// flowState は認可の往復のあいだ持ち越す値。署名付きCookieに載せる。
// 秘密は含まない。stateとnonceに要るのは改竄されないことで、秘匿ではない。
// PKCEのverifierも、読めるのは利用者本人なので差し支えない。
type flowState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Next     string `json:"x"`
}

// authenticate は認証を要求するミドルウェア。auth が nil なら素通しする。
func (s *Server) authenticate(next http.Handler) http.Handler {
	if s.auth == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.currentUser(r) != "" {
			next.ServeHTTP(w, r)
			return
		}
		// ページはログインへ送る。データを取りに来た経路は401で返す。
		// ここでHTMLを返すと、fetchしている側でJSONの解釈が壊れ、画面には
		// 何も出ないまま原因も読めなくなる。/thumb と /file は <img> と
		// <video> が読むので、リダイレクトしても意味がない。
		if isDataPath(r.URL.Path) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
	})
}

// currentUser はセッションCookieから利用者名を取り出す。無ければ空を返す。
func (s *Server) currentUser(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	user, ok := s.sessionCodec.Verify(c.Value, time.Now())
	if !ok {
		return ""
	}
	return user
}

func isDataPath(p string) bool {
	return p == "/tiles" || strings.HasPrefix(p, "/thumb/") || strings.HasPrefix(p, "/file/")
}

// handleLogin は認可の往復を始める。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	p, err := oidcauth.NewParams()
	if err != nil {
		s.log.Error("cannot start the login flow", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	raw, err := json.Marshal(flowState{
		State: p.State, Nonce: p.Nonce, Verifier: p.Verifier, Next: safeNext(r.URL.Query().Get("next")),
	})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.setCookie(w, flowCookie, s.flowCodec.Sign(string(raw), time.Now().Add(flowTTL)), int(flowTTL.Seconds()))
	http.Redirect(w, r, s.auth.OIDC.AuthURL(p), http.StatusFound)
}

// handleCallback は認可コードを受け取ってセッションを発行する。
func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(flowCookie)
	if err != nil {
		s.callbackError(w, "the login attempt has expired, please start again", http.StatusBadRequest)
		return
	}
	raw, ok := s.flowCodec.Verify(c.Value, time.Now())
	if !ok {
		s.callbackError(w, "the login attempt has expired, please start again", http.StatusBadRequest)
		return
	}
	var f flowState
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		s.callbackError(w, "the login attempt is unreadable, please start again", http.StatusBadRequest)
		return
	}
	// stateが合わないものを通すとCSRFになる。
	if q := r.URL.Query().Get("state"); q == "" || q != f.State {
		s.callbackError(w, "the login attempt does not match, please start again", http.StatusBadRequest)
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		s.log.Warn("the identity provider refused the login", "err", e)
		s.callbackError(w, "the identity provider refused the login", http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		s.callbackError(w, "the identity provider returned no code", http.StatusBadRequest)
		return
	}

	id, err := s.auth.OIDC.Exchange(r.Context(), code, oidcauth.Params{State: f.State, Nonce: f.Nonce, Verifier: f.Verifier})
	if err != nil {
		// 「IdPに届かない」と「IdPが応答したうえで拒んだ」は原因の調べ方が違う。
		// 実機のインシデントでは両方が同じ「IdPに到達できない」に丸められ、
		// 調査が違う方向へ進んだ。ProviderErrorが載っていれば後者である。
		var perr *oidcauth.ProviderError
		if errors.As(err, &perr) {
			s.log.Error("the identity provider refused the login", "status", perr.StatusCode, "body", perr.Body)
			// 502はここでは正しくない。502は上流から届いた応答が不正なときの
			// もので、ここでは上流は普通に応答し、そのうえで拒んだだけである。
			// 503は「今は無理だが、また試して良い」を表す。実際に効くことが
			// 多い（インシデントはどれも再試行で通っている）。
			s.callbackError(w, "the identity provider refused the sign-in, please try again", http.StatusServiceUnavailable)
			return
		}
		// famifo は動いているがIdPに届かなかった、という区別を残す。
		s.log.Error("cannot complete the login", "err", err)
		s.callbackError(w, "cannot reach the identity provider", http.StatusBadGateway)
		return
	}
	s.clearCookie(w, flowCookie)
	s.setCookie(w, sessionCookie, s.sessionCodec.Sign(id.Username, time.Now().Add(sessionTTL)), int(sessionTTL.Seconds()))
	s.log.Info("signed in", "user", id.Username)
	http.Redirect(w, r, safeNext(f.Next), http.StatusFound)
}

// handleLogout はこの端末のセッションを捨てる。
// 署名付きCookieを選んだ帰結として、他の端末のセッションは生き続ける。
//
// 消したあとに "/" へリダイレクトしてはいけない。ギャラリーはすべて認証の
// 内側にあるので、"/" は未認証を検知して /login に送り、/login はIdPの
// authorize エンドポイントへ送る。IdP側のセッションはfamifoより長生きする
// ことが多く、まだ生きていれば利用者に何も聞かずそのままcallbackへ差し戻し、
// 新しいセッションを発行してしまう。結果としてサインアウトが見た目上何も
// していないように見え、共有端末では次の利用者がサインインしたままになる。
// そのため /logout はミドルウェアの外側（認証不要な経路）で完結する自前の
// ページを200で返す。
//
// IdPがRP-Initiated Logout（end_session_endpoint）に対応していれば、
// Cookieを消したあとそちらへ送る。1回の操作でIdP側のセッションも終わる。
// 対応していなければ（実機ではSynology SSO Serverがこれにあたる）、
// famifo自身のセッションを終えたことを伝える案内ページを返す。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.clearCookie(w, sessionCookie)
	// ログインを始めて完了させなかった端末に往復用Cookieが残らないようにする。
	s.clearCookie(w, flowCookie)
	if u, ok := s.auth.OIDC.LogoutURL(s.signedOutURL()); ok {
		http.Redirect(w, r, u, http.StatusFound)
		return
	}
	s.renderSignedOut(w)
}

// handleSignedOut はRP-Initiated Logoutの戻り先、かつ /logout が
// end_session_endpoint を持たないIdPのときに見せる案内ページ。
// ミドルウェアの外側（認証不要な経路）に登録してあるので、セッションが
// 無くても届く。
func (s *Server) handleSignedOut(w http.ResponseWriter, r *http.Request) {
	s.renderSignedOut(w)
}

// signedOutURL はRP-Initiated Logoutのpost_logout_redirect_uriに渡す、
// famifo自身の絶対URL。IdPはここへブラウザを送り返す。
func (s *Server) signedOutURL() string {
	return strings.TrimSuffix(s.auth.ExternalURL, "/") + "/signed-out"
}

func (s *Server) renderSignedOut(w http.ResponseWriter) {
	s.writeHTMLPage(w, http.StatusOK, "サインアウトしました", `<link rel="stylesheet" href="/static/app.css">`+
		`<p>famifo からサインアウトしました。</p>`+
		`<p>ログイン画面へのサインインは、これとは別に残っていることがあります。もう一度サインインしても`+
		`何も聞かれないことがありますが、それはこのためです。</p>`+
		`<p><a href="/login">もう一度サインインする</a></p>`)
}

// safeNext は戻り先を自サイト内に限る。
// "//evil.example" はプロトコル相対URLで、別サイトへのリダイレクトになる。
// "\" を含むものも拒む。ブラウザはWHATWG URLの仕様に従って "\" を "/" に
// 正規化するので、"/\evil.example" も別サイトへのリダイレクトになる。
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, `\`) {
		return "/"
	}
	return next
}

// callbackError は失敗した /auth/callback を、/login へのリンク付きの小さな
// HTMLページで終える。素のtext/plainな400だとURLバーを手で書き換えるしか
// 戻る手段がない。ここで一時Cookieも消す。消さないと、失敗した往復のあとも
// famifo_oidc が最長flowTTLぶん残り、「やり直してください」が実際にはやり直し
// にならない（別タブが一時Cookieを上書きしてここに来るのは日常的に起きる）。
func (s *Server) callbackError(w http.ResponseWriter, message string, status int) {
	s.clearCookie(w, flowCookie)
	s.writeHTMLPage(w, status, "Sign-in failed",
		fmt.Sprintf(`<p>%s</p><p><a href="/login">Try signing in again</a></p>`, html.EscapeString(message)))
}

// writeHTMLPage は認証の内側を経由しない小さなHTMLページを書き出す。bodyHTML
// はすでに安全な断片であることを呼び出し側が保証する（利用者由来の文字列を
// 混ぜるときはhtml.EscapeStringを通すこと）。
func (s *Server) writeHTMLPage(w http.ResponseWriter, status int, title, bodyHTML string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><title>%s</title>%s`, html.EscapeString(title), bodyHTML)
}

func (s *Server) setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.auth.Secure,
	})
}

func (s *Server) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.auth.Secure,
	})
}
