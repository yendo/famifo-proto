package web

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/yendo/famifo-proto/internal/store"
)

// filterFrom は要求のクエリから一覧の絞り込みを読む。?owner= は何度でも書け、
// 選んだ誰かが撮った写真に絞る。どれか1つでも uid として読めなければエラーにする。
//
// 値は重複を落として小さい順に並べる。filterURL はこの並びのまま書くので、同じ
// 条件の組は同じ URL になる。
func filterFrom(r *http.Request) (store.Filter, error) {
	var f store.Filter
	for _, raw := range r.URL.Query()["owner"] {
		uid, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return store.Filter{}, fmt.Errorf("invalid owner: %q", raw)
		}
		f.Owners = append(f.Owners, uint32(uid))
	}
	slices.Sort(f.Owners)
	f.Owners = slices.Compact(f.Owners)
	return f, nil
}

// filterURL は path に f の条件をクエリとして付けた URL を返す。条件が無ければ
// path のまま。絞り込みを引き継ぐ URL は、すべてここで作る。JS は条件の組み立て方を
// 知らない。
func filterURL(path string, f store.Filter) string {
	if len(f.Owners) == 0 {
		return path
	}
	q := url.Values{}
	for _, uid := range f.Owners {
		q.Add("owner", strconv.FormatUint(uint64(uid), 10))
	}
	return path + "?" + q.Encode()
}
