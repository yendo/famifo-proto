package web

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/yendo/famifo-proto/internal/owner"
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

// ownerChoice はパネルの撮影者の節の1項目。
type ownerChoice struct {
	Name     string
	URL      string // この人を条件に入れた（選んでいれば外した）一覧の URL
	Selected bool
}

// ownerChoices は撮影者の節の項目を名前順に返す。写真のある uid のうち、名前の
// 引ける人だけを並べる。名前の無い人の写真は「全員」でだけ見える。
func (h *Handler) ownerChoices(r *http.Request, f store.Filter) ([]ownerChoice, error) {
	uids, err := h.store.Owners(r.Context())
	if err != nil {
		return nil, err
	}
	var out []ownerChoice
	for _, uid := range uids {
		name := owner.Name(uid)
		if name == "" {
			continue
		}
		_, selected := slices.BinarySearch(f.Owners, uid)
		out = append(out, ownerChoice{
			Name:     name,
			URL:      filterURL("/", toggleOwner(f, uid)),
			Selected: selected,
		})
	}
	// 同じ名前の人がいても順が揺れないよう、uid の順を保ったまま並べ替える。
	slices.SortStableFunc(out, func(a, b ownerChoice) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// toggleOwner は f の撮影者の条件で uid を入れ替えたものを返す。選んでいれば外し、
// 選んでいなければ入れる。並びは小さい順のまま保つ。f は変えない。
func toggleOwner(f store.Filter, uid uint32) store.Filter {
	owners := slices.Clone(f.Owners)
	if i, found := slices.BinarySearch(owners, uid); found {
		owners = slices.Delete(owners, i, i+1)
	} else {
		owners = slices.Insert(owners, i, uid)
	}
	f.Owners = owners
	return f
}
