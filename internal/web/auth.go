package web

import (
	"context"
	"encoding/json"
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
}

// Auth は認証の手段をまとめる。NewServer に nil を渡すと認証しない。
type Auth struct {
	OIDC   Provider
	Key    []byte // session.KeyLen バイト。webが用途ごとのCodecを導出する
	Secure bool   // Cookie に Secure を付けるか。外部URLがhttpsのときだけ真
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
		http.Error(w, "the login attempt has expired, please start again", http.StatusBadRequest)
		return
	}
	raw, ok := s.flowCodec.Verify(c.Value, time.Now())
	if !ok {
		http.Error(w, "the login attempt has expired, please start again", http.StatusBadRequest)
		return
	}
	var f flowState
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		http.Error(w, "the login attempt is unreadable, please start again", http.StatusBadRequest)
		return
	}
	// stateが合わないものを通すとCSRFになる。
	if q := r.URL.Query().Get("state"); q == "" || q != f.State {
		http.Error(w, "the login attempt does not match, please start again", http.StatusBadRequest)
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		s.log.Warn("the identity provider refused the login", "err", e)
		http.Error(w, "the identity provider refused the login", http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "the identity provider returned no code", http.StatusBadRequest)
		return
	}

	id, err := s.auth.OIDC.Exchange(r.Context(), code, oidcauth.Params{State: f.State, Nonce: f.Nonce, Verifier: f.Verifier})
	if err != nil {
		// famifo は動いているがIdPとの往復が失敗した、という区別を残す。
		s.log.Error("cannot complete the login", "err", err)
		http.Error(w, "cannot reach the identity provider", http.StatusBadGateway)
		return
	}
	s.clearCookie(w, flowCookie)
	s.setCookie(w, sessionCookie, s.sessionCodec.Sign(id.Username, time.Now().Add(sessionTTL)), int(sessionTTL.Seconds()))
	s.log.Info("signed in", "user", id.Username)
	http.Redirect(w, r, safeNext(f.Next), http.StatusFound)
}

// handleLogout はこの端末のセッションを捨てる。
// 署名付きCookieを選んだ帰結として、他の端末のセッションは生き続ける。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.clearCookie(w, sessionCookie)
	// ログインを始めて完了させなかった端末に往復用Cookieが残らないようにする。
	s.clearCookie(w, flowCookie)
	http.Redirect(w, r, "/", http.StatusFound)
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
