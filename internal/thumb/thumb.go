// Package thumb は一覧用のサムネイルを所有する。借りられるものは借り、
// 借りられないものだけ自前で生成する（自前の出力は常にJPEG）。
//
// 取り込み側（internal/index）が生成と掃除を、配信側（internal/web）がパスの取得を
// 使う。置き場所の規則を知るのはこのパッケージだけで、どちらの側もサムネイルの
// ディレクトリを持たない。
//
// 生成にHEICと動画は来ない（自前ではデコードしない方針）。動画のサムネイルは
// 借りるだけで、famifoが作るものは何も無い。@eaDir のパスの組み立てと
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

	_ "image/gif" // image.Decode にGIFを登録する
	_ "image/png" // image.Decode にPNGを登録する

	"github.com/yendo/famifo-proto/internal/imagefmt"
	"github.com/yendo/famifo-proto/internal/media"
	"github.com/yendo/famifo-proto/internal/synology"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // image.Decode にWebPを登録する（デコードのみ）
)

// jpegQuality はサムネイルの画質。一覧表示に十分で、かつ十分軽い値。
const jpegQuality = 82

// maxEdge はサムネイルの辺の最大ピクセル数。長辺がこの値に収まるまで縮小する。
//
// 一覧のタイルは正方形で object-fit: cover のため、実際に効くのは短辺
// （3:2の写真なら320px）である。設定可能にしていたが、利用者が変える場面が
// 無いうえ、変えても既存のサムネイルは作り直されず「設定できるのに効かない」
// フラグになっていたため定数にした。値を変えたときはデータディレクトリごと
// 削除して作り直すこと。
const maxEdge = 480

// Provider は一覧用のサムネイルを供給する。自前の置き場を所有し、借りられる
// ものは @eaDir から借り、借りられないものだけ生成する。
type Provider struct {
	dir string
}

// NewProvider は置き場のディレクトリを用意してProviderを返す。
func NewProvider(dir string) (*Provider, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("cannot create the thumbnail directory: %w", err)
	}
	return &Provider{dir: dir}, nil
}

// famifoThumbPath は自前で生成したサムネイルの置き場所を返す。実在するとは限らない。
// Prepare が書き込む先であり、Path が引き当てる先でもある。
//
// 名前に元画像の版（mtimeのUnix秒）を含める。写真が差し替われば別のファイルに
// なるので、鮮度の判定が「サムネイルのほうが新しいか」という順序の比較ではなく
// 「その版の名前があるか」という一致の確認で済む。mtimeは前にしか進むとは
// 限らず（cp -p や rsync -t でバックアップから戻すと過去へ動く）、順序で
// 判定すると作り直しを見送ってしまうため。
//
// 秒に丸めるのは、DBが mod_time を Unix 秒で持っているのに合わせるためと、
// ファイルシステムによって時刻の粒度が違うのを避けるため。
func (pv *Provider) famifoThumbPath(m media.Media) string {
	name := fmt.Sprintf("%s-%d.jpg", m.ID(), m.ModTime().Unix())
	return filepath.Join(pv.shardDir(m.ID()), name)
}

// Path は一覧のタイルに配信するサムネイルのパスと、そのMIMEタイプを返す。
//
// 出どころはDBに持たず、配信のたびに調べる。取り込み時点の判断を焼き付けると、
// あとからDSMがサムネイルを作っても（原本のmtimeが動かない限り再取り込みされないため）
// 永久に反映されない。
//
// @eaDir を先に見るのは、実際のライブラリではHEICもJPEGもほぼ全てにSynologyの
// サムネイルがあり、取り込み時に借りる側を優先しているぶん自前の置き場がほぼ空になる
// ためである。先に自分の置き場を見ると大半のタイルで空振りする。
//
// どちらも無ければ原本に落ちる。ただしブラウザが表示できない形式（HEIC/HEIF）では
// 原本を出しても割れたタイルになるだけなので ok=false を返し、配信側がプレースホルダに
// 差し替える。
//
// 原本に落ちるのを残すかは未決である（2026-10-07）。取り込み時に Prepare が借りるか
// 作るかしており、生成に失敗した写真はDBに載らないので、ここに来るのは取り込み後に
// サムネイルが消えた場合に限られる。借りていた @eaDir のものが消えた（借りた時点で
// 自前の分は掃除済み）か、自前の置き場から消されたかである。原本のmtimeが動かない限り
// 取り込み直されないので、その間は重い原本が一覧に出続ける。また、原本を返すのは
// サムネイルを供給するというこの型の役割を超えている。
//
// 「ブラウザが出せるか」の判定に IsDecodable を使っている。いまの対応表では
// 「famifoがデコードできる形式」と「ブラウザが表示できる形式」が一致しているためで、
// 別の問いである。両者が食い違う形式（Goがデコードできないがブラウザは表示できる
// AVIFなど）を表に足すときは、ここを分ける必要がある。
func (pv *Provider) Path(m media.Media) (path, contentType string, ok bool) {
	if synology.HasThumbM(m.Path()) {
		borrowed := synology.ThumbMPath(m.Path())
		return borrowed, imagefmt.ContentType(borrowed), true
	}
	if out := pv.famifoThumbPath(m); isRegularFile(out) {
		return out, imagefmt.ContentType(out), true
	}
	if imagefmt.IsDecodable(m.Path()) {
		return m.Path(), imagefmt.ContentType(m.Path()), true
	}
	return "", "", false
}

// Prepare は写真1枚ぶんのサムネイルを配信できる状態にする。
//
// 呼び終わると、自分の置き場にはこの写真の現在の版が1つだけあるか、1つも無い。
// 「サムネイルがある」ことは保証しない。
//
// Synologyが作ったものがあれば借りる。デコードもリサイズもせずに済み、famifoが
// デコードできないHEICも一覧に出せるようになる。@eaDir は読むだけで、書き込みも
// 削除もしない。
//
// 借りられず自前でも作れない写真（サムネイルの無いHEIC等）には何も残さない。
// 出せるものが無いことはエラーではなく、配信側が原本に落ちる。
//
// 生成に失敗した場合だけエラーを返す。インデックスに載せるかどうかは呼び出し側の
// 判断である。
//
// 古い版の掃除の失敗は握りつぶす。消し残しは表示にも正しさにも影響せず、
// 数KBのファイルが残るだけなので、これで取り込み全体を失敗させる価値がない。
func (pv *Provider) Prepare(m media.Media, orientation uint16) error {
	switch {
	case synology.HasThumbM(m.Path()):
		// 借りるほうへ切り替わったら、自前で作ったものは用済みになる。
		_ = pv.sweepFamifoThumbs(m.ID(), "")
	case imagefmt.IsDecodable(m.Path()):
		out, err := pv.generateFamifoThumb(m, orientation)
		if err != nil {
			// 失敗しても古い版は消さない。新しいのができるまでの控えとして
			// 働いており、先に消すと一覧のタイルが割れるため。
			return err
		}
		_ = pv.sweepFamifoThumbs(m.ID(), out)
	default:
		_ = pv.sweepFamifoThumbs(m.ID(), "")
	}
	return nil
}

// generateFamifoThumb は m の原本からサムネイルを作る。
// デコードできないファイルはエラーを返し、サムネイルは何も残さない。
//
// orientation はEXIFの向き（Orientation、1..8）。image.Decode はEXIFを見ずに
// 生の画素を返し、jpeg.Encode はEXIFを書き出さないため、ここで適用しないと
// 向きの情報はサムネイルから完全に失われる。1..8以外は回転不要として扱う。
//
// その版のサムネイルが既にあれば何もしない。DBを作り直すたびに全件を作り直すと
// 4,495枚で37分（NASなら数時間）かかるが、その大半は中身の変わらないサムネイルの
// 再生成である。既にある場合は原本を開きもしない。
//
// 版は m.ModTime() から取る。自分で stat し直すと、その1回とインデックスに載る
// 版とが食い違い、配信側が存在しない名前を引くことになるため。
//
// 作った（または既にあった）サムネイルのパスを返す。呼び出し側が、それ以外の版を
// 掃除するために使う。
func (pv *Provider) generateFamifoThumb(m media.Media, orientation uint16) (string, error) {
	out := pv.famifoThumbPath(m)
	if isRegularFile(out) {
		return out, nil
	}

	f, err := os.Open(m.Path())
	if err != nil {
		return "", fmt.Errorf("cannot open the image: %w", err)
	}
	defer f.Close()

	src, _, err := image.Decode(f)
	if err != nil {
		return "", fmt.Errorf("cannot decode the image: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
		return "", fmt.Errorf("cannot create the thumbnail destination: %w", err)
	}

	// 一時ファイルに書いてからrenameする。生成途中のファイルをHTTPハンドラが
	// 掴んでしまわないようにするため。
	tmp, err := os.CreateTemp(filepath.Dir(out), ".tmp-*")
	if err != nil {
		return "", fmt.Errorf("cannot create a temporary file: %w", err)
	}
	defer os.Remove(tmp.Name()) // renameが成功していれば消す対象は無い

	// 縮小してから回転する。長辺基準の縮小なので順序で結果の寸法は変わらないが、
	// 4032x3024ではなく480x360を回すぶん安く済む。
	dst := applyOrientation(scaleToFit(src, maxEdge), orientation)
	if err := jpeg.Encode(tmp, dst, &jpeg.Options{Quality: jpegQuality}); err != nil {
		tmp.Close()
		return "", fmt.Errorf("cannot write the thumbnail: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("cannot close the temporary file: %w", err)
	}
	if err := os.Rename(tmp.Name(), out); err != nil {
		return "", fmt.Errorf("cannot put the thumbnail in place: %w", err)
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

// RemoveFamifoThumbs は id のサムネイルを版によらず全て削除する。
// 存在しない場合はエラーにしない。
func (pv *Provider) RemoveFamifoThumbs(id string) error { return pv.sweepFamifoThumbs(id, "") }

// sweepFamifoThumbs は id のサムネイルのうち keep 以外を削除する。keep が空なら全て消す。
//
// 同じ写真の古い版はここでまとめて片づく。前回の異常終了で取り残されたものも
// 同時に回収する。版を持たない旧形式（<id>.jpg）も接頭辞で拾えるようにしてある。
func (pv *Provider) sweepFamifoThumbs(id, keep string) error {
	dir := pv.shardDir(id)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot read the thumbnail directory: %w", err)
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
			return fmt.Errorf("cannot delete an old thumbnail: %w", err)
		}
	}
	return nil
}

// shardDir は id のサムネイルを置くディレクトリを返す。
// 1ディレクトリにファイルが集中しないようIDの先頭2文字で分割する。
func (pv *Provider) shardDir(id string) string {
	return filepath.Join(pv.dir, id[:2])
}

// scaleToFit は長辺が maxEdge 以下になるよう縮小する。元より大きくは引き伸ばさない。
func scaleToFit(src image.Image, maxEdge int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxEdge && h <= maxEdge {
		return src
	}
	if w >= h {
		w, h = maxEdge, h*maxEdge/w
	} else {
		w, h = w*maxEdge/h, maxEdge
	}
	w, h = max(w, 1), max(h, 1)
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
