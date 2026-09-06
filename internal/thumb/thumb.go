// Package thumb は表示用に派生した画像を所有する。一覧用のサムネイルは借りられる
// ものを借り、借りられないものだけ自前で生成する（自前の出力は常にJPEG）。
// 拡大表示にどのファイルを配信するかの選択もここが決める。
//
// 取り込み側（internal/index）が生成と掃除を、配信側（internal/web）がパスの取得を
// 使う。置き場所の規則を知るのはこのパッケージだけで、どちらの側もサムネイルの
// ディレクトリを持たない。
//
// 生成にHEICは来ない（自前ではデコードしない方針）。@eaDir のパスの組み立てと
// 存在確認は internal/synology が持ち、ここはそれを使って選ぶだけである。
package thumb

import (
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "image/gif" // image.Decode にGIFを登録する
	_ "image/png" // image.Decode にPNGを登録する

	"github.com/yendo/famifo-proto/internal/imagefmt"
	"github.com/yendo/famifo-proto/internal/photo"
	"github.com/yendo/famifo-proto/internal/synology"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // image.Decode にWebPを登録する（デコードのみ）
)

// jpegQuality はサムネイルの画質。一覧表示に十分で、かつ十分軽い値。
const jpegQuality = 82

// MaxEdge はサムネイルの辺の最大ピクセル数。長辺がこの値に収まるまで縮小する。
//
// 一覧のタイルは正方形で object-fit: cover のため、実際に効くのは短辺
// （3:2の写真なら320px）である。設定可能にしていたが、利用者が変える場面が
// 無いうえ、変えても既存のサムネイルは作り直されず「設定できるのに効かない」
// フラグになっていたため定数にした。値を変えたときはデータディレクトリごと
// 削除して作り直すこと。
const MaxEdge = 480

// Provider は一覧用のサムネイルを供給する。自前の置き場を所有し、借りられる
// ものは @eaDir から借り、借りられないものだけ生成する。
type Provider struct {
	dir string
}

// NewProvider は置き場のディレクトリを用意してProviderを返す。
func NewProvider(dir string) (*Provider, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("サムネイルディレクトリを作れません: %w", err)
	}
	return &Provider{dir: dir}, nil
}

// shardDir は id のサムネイルを置くディレクトリを返す。
// 1ディレクトリにファイルが集中しないようIDの先頭2文字で分割する。
func (pv *Provider) shardDir(id string) string {
	return filepath.Join(pv.dir, id[:2])
}

// path は元画像の版に対応するサムネイルの絶対パスを返す。
//
// 名前に元画像の版（mtimeのUnix秒）を含める。写真が差し替われば別のファイルに
// なるので、鮮度の判定が「サムネイルのほうが新しいか」という順序の比較ではなく
// 「その版の名前があるか」という一致の確認で済む。mtimeは前にしか進むとは
// 限らず（cp -p や rsync -t でバックアップから戻すと過去へ動く）、順序で
// 判定すると作り直しを見送ってしまうため。
//
// 秒に丸めるのは、DBが mod_time を Unix 秒で持っているのに合わせるためと、
// ファイルシステムによって時刻の粒度が違うのを避けるため。
func (pv *Provider) path(id string, srcModTime time.Time) string {
	return filepath.Join(pv.shardDir(id), fmt.Sprintf("%s-%d.jpg", id, srcModTime.Unix()))
}

// SmallPath は一覧に出すサムネイルのパスを返す。無ければ ok=false。
// ok=false のとき、一覧は原本のURLにフォールバックし、/thumb/ エンドポイントは404を返す。
func (pv *Provider) SmallPath(p photo.Photo) (string, bool) {
	switch p.ThumbSource() {
	case photo.ThumbFamifo:
		return pv.path(p.ID(), p.ModTime()), true
	case photo.ThumbSyno:
		return synology.ThumbMPath(p.Path()), true
	}
	return "", false
}

// LargePath は拡大表示に配信するファイルのパスと、そのMIMEタイプを返す。
//
// HEICはSafari以外のブラウザが表示できない。@eaDir から借りているなら原本ではなく
// SynologyのXL（長辺1707px）を返す。thumb_source が eadir であればMがあり、MとXLは
// 同じ生成器が一緒に書くので、XLの存在はそこから導ける。
//
// 借りたXLは .jpg なので、原本がHEICでもMIMEは image/jpeg になる。呼び出し側が
// 選ばれたパスからMIMEを引き直さずに済むよう、ここで一緒に返す。
func (pv *Provider) LargePath(p photo.Photo) (path, contentType string) {
	path = p.Path()
	if imagefmt.IsSupported(path) && !imagefmt.IsDecodable(path) &&
		p.ThumbSource() == photo.ThumbSyno {
		path = synology.ThumbXLPath(p.Path())
	}
	return path, imagefmt.ContentType(path)
}

// ResolveSource は写真1枚のサムネイルの出どころを確定させて返す。
//
// Synologyが作ったものがあれば借りる。デコードもリサイズもせずに済み、famifoが
// デコードできないHEICも一覧に出せるようになる。@eaDir は読むだけで、書き込みも
// 削除もしない。
//
// 借りられず自前でも作れない写真（サムネイルの無いHEIC等）は ThumbNone を返す。
// 一覧は原本のURLにフォールバックする。
//
// 生成に失敗した場合はエラーを返す。インデックスに載せるかどうかは呼び出し側の
// 判断で、ここでは出どころを決めない。
func (pv *Provider) ResolveSource(path string, orientation uint16) (photo.ThumbSource, error) {
	id := photo.IDFor(path)
	switch {
	case synology.HasThumbM(path):
		// 借りるほうへ切り替わったら、自前で作ったものは用済みになる。
		pv.sweepQuietly(id, "")
		return photo.ThumbSyno, nil
	case imagefmt.IsDecodable(path):
		out, err := pv.generate(path, id, orientation)
		if err != nil {
			// 失敗しても古い版は消さない。新しいのができるまでの控えとして
			// 働いており、先に消すと一覧のタイルが割れるため。
			return photo.ThumbNone, err
		}
		pv.sweepQuietly(id, out)
		return photo.ThumbFamifo, nil
	}
	pv.sweepQuietly(id, "")
	return photo.ThumbNone, nil
}

// generate は srcPath の画像からサムネイルを作る。
// デコードできないファイルはエラーを返し、サムネイルは何も残さない。
//
// orientation は internal/index/exif が読んだEXIFの向き。image.Decode はEXIFを見ずに
// 生の画素を返し、jpeg.Encode はEXIFを書き出さないため、ここで適用しないと
// 向きの情報はサムネイルから完全に失われる。1..8以外は回転不要として扱う。
//
// その版のサムネイルが既にあれば何もしない。DBを作り直すたびに全件を作り直すと
// 4,495枚で37分（NASなら数時間）かかるが、その大半は中身の変わらないサムネイルの
// 再生成である。
//
// 作った（または既にあった）サムネイルのパスを返す。呼び出し側が、それ以外の版を
// 掃除するために使う。
func (pv *Provider) generate(srcPath, id string, orientation uint16) (string, error) {
	f, err := os.Open(srcPath)
	if err != nil {
		return "", fmt.Errorf("画像を開けません: %w", err)
	}
	defer f.Close()

	// 出力の名前に元画像の版が入るので、mtimeが取れないと置き場所が決まらない。
	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("画像のファイル情報を取得できません: %w", err)
	}
	out := pv.path(id, fi.ModTime())
	if isRegularFile(out) {
		return out, nil
	}

	src, _, err := image.Decode(f)
	if err != nil {
		return "", fmt.Errorf("画像をデコードできません: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", fmt.Errorf("サムネイルの保存先を作れません: %w", err)
	}

	// 一時ファイルに書いてからrenameする。生成途中のファイルをHTTPハンドラが
	// 掴んでしまわないようにするため。
	tmp, err := os.CreateTemp(filepath.Dir(out), ".tmp-*")
	if err != nil {
		return "", fmt.Errorf("一時ファイルを作れません: %w", err)
	}
	defer os.Remove(tmp.Name()) // renameが成功していれば消す対象は無い

	// 縮小してから回転する。長辺基準の縮小なので順序で結果の寸法は変わらないが、
	// 4032x3024ではなく480x360を回すぶん安く済む。
	dst := applyOrientation(scaleToFit(src, MaxEdge), orientation)
	if err := jpeg.Encode(tmp, dst, &jpeg.Options{Quality: jpegQuality}); err != nil {
		tmp.Close()
		return "", fmt.Errorf("サムネイルを書き出せません: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("一時ファイルを閉じられません: %w", err)
	}
	if err := os.Rename(tmp.Name(), out); err != nil {
		return "", fmt.Errorf("サムネイルを配置できません: %w", err)
	}
	return out, nil
}

// isRegularFile はそのパスに通常ファイルがあるかを返す。
//
// 名前に元画像の版が入っているので、存在すればその版から作られたものである。
// 「元より新しいか」を確かめる必要はない。出力は一時ファイルへ書いてから
// renameしているため、中途半端な内容が残っていることもない。
func isRegularFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

// Remove は id のサムネイルを版によらず全て削除する。
// 存在しない場合はエラーにしない。
func (pv *Provider) Remove(id string) error { return pv.sweep(id, "") }

// sweep は id のサムネイルのうち keep 以外を削除する。keep が空なら全て消す。
//
// 同じ写真の古い版はここでまとめて片づく。前回の異常終了で取り残されたものも
// 同時に回収する。版を持たない旧形式（<id>.jpg）も接頭辞で拾えるようにしてある。
func (pv *Provider) sweep(id, keep string) error {
	dir := pv.shardDir(id)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("サムネイルの置き場を読めません: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), id) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if path == keep {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("古いサムネイルを削除できません: %w", err)
		}
	}
	return nil
}

// sweepQuietly は掃除の失敗を握りつぶす。消し残しは表示にも正しさにも影響せず、
// 数KBのファイルが残るだけなので、これで取り込み全体を失敗させる価値がない。
func (pv *Provider) sweepQuietly(id, keep string) { _ = pv.sweep(id, keep) }

// scaleToFit は長辺が max 以下になるよう縮小する。元より大きくは引き伸ばさない。
func scaleToFit(src image.Image, max int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= max && h <= max {
		return src
	}
	if w >= h {
		w, h = max, h*max/w
	} else {
		w, h = w*max/h, max
	}
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Over, nil)
	return dst
}

// applyOrientation はEXIFのOrientationに従って画素を並べ替える。
func applyOrientation(src image.Image, o uint16) image.Image {
	if o <= 1 || o > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w // 5..8 は縦横が入れ替わる
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range dh {
		for x := range dw {
			sx, sy := sourcePixel(x, y, w, h, o)
			dst.Set(x, y, src.At(b.Min.X+sx, b.Min.Y+sy))
		}
	}
	return dst
}

// sourcePixel は表示後の(x,y)に対応する元画像の座標を返す。w,hは元画像の寸法。
//
// EXIFのOrientationは「元データの0行目/0列目が表示上のどこへ来るか」を表す。
// 6なら Right-Top、つまり0行目が右端・0列目が上端に来る（右90度回転）。
func sourcePixel(x, y, w, h int, o uint16) (int, int) {
	switch o {
	case 2: // Top-Right: 左右反転
		return w - 1 - x, y
	case 3: // Bottom-Right: 180度
		return w - 1 - x, h - 1 - y
	case 4: // Bottom-Left: 上下反転
		return x, h - 1 - y
	case 5: // Left-Top: 転置
		return y, x
	case 6: // Right-Top: 右90度
		return y, h - 1 - x
	case 7: // Right-Bottom: 逆転置
		return w - 1 - y, h - 1 - x
	case 8: // Left-Bottom: 右270度
		return w - 1 - y, x
	}
	return x, y
}
