package thumb_test

// 配信するファイルの選択を確かめる。SmallPath と LargePath はI/Oを持たないので、
// 実ファイルもHTTPサーバーも用意せずに全ての組み合わせを並べられる。

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/photo"
	"github.com/yendo/famifo-proto/internal/thumb"
)

var testModTime = time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

// restored は保存済みの1枚を模したPhotoを組み立てる。日時とサイズはどのテストも
// 見ないので固定値でよく、パスと出どころだけを行ごとに変える。
func restored(path string, thumbSource photo.ThumbSource) photo.Photo {
	return photo.Restore(path, testModTime, testModTime, 0, thumbSource)
}

// newPathProvider は置き場のディレクトリも一緒に返す。自前で生成したサムネイルの
// パスは、その下のどこに置かれるかまで含めて期待値になる。
func newPathProvider(t *testing.T) (*thumb.Provider, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "thumbs")
	pv, err := thumb.NewProvider(dir)
	require.NoError(t, err)
	return pv, dir
}

func TestSmallPathBySource(t *testing.T) {
	pv, thumbDir := newPathProvider(t)
	jpgID := photo.IDFor("/photos/a.jpg")
	tests := []struct {
		name string
		p    photo.Photo
		want string
		ok   bool
	}{
		{
			name: "自前で生成したものは自分の置き場から引く",
			p:    restored("/photos/a.jpg", photo.ThumbFamifo),
			want: filepath.Join(thumbDir, jpgID[:2],
				fmt.Sprintf("%s-%d.jpg", jpgID, testModTime.Unix())),
			ok: true,
		},
		{
			name: "借りたものは @eaDir から引く",
			p:    restored("/photos/a.heic", photo.ThumbSyno),
			want: "/photos/@eaDir/a.heic/SYNOPHOTO_THUMB_M.jpg",
			ok:   true,
		},
		{
			name: "借りるものも作れるものも無ければ ok=false",
			p:    restored("/photos/a.heic", photo.ThumbNone),
			want: "",
			ok:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := pv.SmallPath(tt.p)
			require.Equal(t, tt.ok, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

// 1ディレクトリにファイルが集中しないよう、IDの先頭2文字で分割する。
func TestSmallPathShardsByTheFirstTwoCharsOfTheID(t *testing.T) {
	pv, thumbDir := newPathProvider(t)
	p := restored("/photos/a.jpg", photo.ThumbFamifo)

	got, ok := pv.SmallPath(p)

	require.True(t, ok)
	require.Equal(t, filepath.Join(thumbDir, p.ID()[:2]), filepath.Dir(got))
}

// 名前に元画像の版が入るので、写真が差し替われば別のファイルを指す。
// 鮮度を「サムネイルのほうが新しいか」で測らずに済ませるための土台。
func TestSmallPathVariesWithTheSourceVersion(t *testing.T) {
	pv, _ := newPathProvider(t)
	const path = "/photos/a.jpg"

	before, ok := pv.SmallPath(photo.Restore(path, testModTime, time.Unix(1700000000, 0), 0, photo.ThumbFamifo))
	require.True(t, ok)
	after, ok := pv.SmallPath(photo.Restore(path, testModTime, time.Unix(1600000000, 0), 0, photo.ThumbFamifo))
	require.True(t, ok)

	require.NotEqual(t, before, after, "版が違えば別の名前になる")
	require.Equal(t, filepath.Dir(before), filepath.Dir(after), "置き場は同じ")
}

// XLに差し替えるのは「HEICで、かつ借りている」ときだけ。
// 他の5通りはすべて原本を配信する。
func TestLargePathSwapsInTheXLOnlyForBorrowedOpaquePhotos(t *testing.T) {
	pv, _ := newPathProvider(t)
	tests := []struct {
		name string
		p    photo.Photo
		want string
	}{
		{
			name: "HEIC + 借りている → SynologyのXL",
			p:    restored("/photos/a.heic", photo.ThumbSyno),
			want: "/photos/@eaDir/a.heic/SYNOPHOTO_THUMB_XL.jpg",
		},
		{
			name: "HEIC + 借りていない → 原本（Safariでしか見えないが他に出せるものが無い）",
			p:    restored("/photos/a.heic", photo.ThumbNone),
			want: "/photos/a.heic",
		},
		{
			name: "HEIC + 自前生成 → 原本（HEICは自前生成しないので実際には起きない）",
			p:    restored("/photos/a.heic", photo.ThumbFamifo),
			want: "/photos/a.heic",
		},
		{
			name: "JPEG + 借りている → 原本（借りるのは一覧用だけ）",
			p:    restored("/photos/a.jpg", photo.ThumbSyno),
			want: "/photos/a.jpg",
		},
		{
			name: "JPEG + 自前生成 → 原本",
			p:    restored("/photos/a.jpg", photo.ThumbFamifo),
			want: "/photos/a.jpg",
		},
		{
			name: "JPEG + サムネイル無し → 原本",
			p:    restored("/photos/a.jpg", photo.ThumbNone),
			want: "/photos/a.jpg",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := pv.LargePath(tt.p)
			require.Equal(t, tt.want, got)
		})
	}
}

// MIMEが選ばれたファイルに追随することが、ハンドラ側で分岐を持たずに済む根拠。
func TestLargePathContentTypeFollowsTheChosenFile(t *testing.T) {
	pv, _ := newPathProvider(t)

	_, borrowed := pv.LargePath(restored("/photos/a.heic", photo.ThumbSyno))
	require.Equal(t, "image/jpeg", borrowed, "借りたXLは .jpg なので原本がHEICでもJPEG")

	_, original := pv.LargePath(restored("/photos/a.heic", photo.ThumbNone))
	require.Equal(t, "image/heic", original, "原本を出すなら原本の拡張子どおり")
}
