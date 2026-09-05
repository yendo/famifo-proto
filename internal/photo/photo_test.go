package photo_test

import (
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/photo"
)

func TestIDForIsStableAndDistinct(t *testing.T) {
	a := photo.IDFor("/photos/a.jpg")

	require.Len(t, a, 32)
	require.Equal(t, a, photo.IDFor("/photos/a.jpg"))
	require.NotEqual(t, a, photo.IDFor("/photos/b.jpg"))
}

// testID は32文字のダミーID。photo.IDFor の出力と同じ形（16進32文字）にしてある。
const testID = "abcdef0123456789abcdef0123456789"

func TestFamifoThumbPathShardsByFirstTwoChars(t *testing.T) {
	require.Equal(t, filepath.Join("/data/thumbs", "ab", testID+".jpg"),
		photo.FamifoThumbPath("/data/thumbs", testID))
}

// restored は保存済みの1枚を模したPhotoを組み立てる。日時とサイズはどのテストも
// 見ないので固定値でよく、パスと出どころだけを行ごとに変える。
func restored(path string, thumbSource photo.ThumbSource) photo.Photo {
	return photo.Restore(path, testModTime, testModTime, 0, thumbSource)
}

func TestThumbPathBySource(t *testing.T) {
	const thumbDir = "/data/thumbs"
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
			want: filepath.Join(thumbDir, jpgID[:2], jpgID+".jpg"),
			ok:   true,
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
			got, ok := tt.p.ThumbPath(thumbDir)
			require.Equal(t, tt.ok, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

// HasThumb は「一覧に出せるか」、HasFamifoThumb は「消してよいか」を答える。
// 借りたものだけが両者で答えが分かれる。
func TestHasThumbAgreesWithThumbSource(t *testing.T) {
	tests := []struct {
		name        string
		thumbSource photo.ThumbSource
		want        bool
		wantFamifo  bool
	}{
		{
			name:        "自前で生成したものはあり、消してよい",
			thumbSource: photo.ThumbFamifo,
			want:        true,
			wantFamifo:  true,
		},
		{
			name:        "借りたものもあるが、消してはいけない",
			thumbSource: photo.ThumbSyno,
			want:        true,
			wantFamifo:  false,
		},
		{
			name:        "どちらでもなければ無い",
			thumbSource: photo.ThumbNone,
			want:        false,
			wantFamifo:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := restored("/photos/a.jpg", tt.thumbSource)
			require.Equal(t, tt.want, p.HasThumb())
			require.Equal(t, tt.wantFamifo, p.HasFamifoThumb())
		})
	}
}

// XLに差し替えるのは「HEICで、かつ借りている」ときだけ。
// 他の5通りはすべて原本を配信する。
func TestFullPathSwapsInTheXLOnlyForBorrowedOpaquePhotos(t *testing.T) {
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
			require.Equal(t, tt.want, tt.p.FullPath())
		})
	}
}

// ContentType が FullPath の選択に追随することが、ハンドラ側で分岐を持たずに済む根拠。
func TestContentTypeOfTheBorrowedXLIsJPEG(t *testing.T) {
	p := restored("/photos/a.heic", photo.ThumbSyno)
	require.Equal(t, "image/jpeg", p.ContentType())
}

func TestContentTypeOfAnUnborrowedHEICIsHEIC(t *testing.T) {
	p := restored("/photos/a.heic", photo.ThumbNone)
	require.Equal(t, "image/heic", p.ContentType())
}

// fakeFileInfo は New が読む ModTime と Size だけを持つ fs.FileInfo。
// 他のメソッドが呼ばれたら、埋め込んだ nil で落ちるので気づける。
type fakeFileInfo struct {
	fs.FileInfo
	modTime time.Time
	size    int64
}

func (f fakeFileInfo) ModTime() time.Time { return f.modTime }
func (f fakeFileInfo) Size() int64        { return f.size }

var testModTime = time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

func TestNewFillsTheFieldsFromThePathAndFileInfo(t *testing.T) {
	const path = "/photos/A.JPG"

	p := photo.New(path, fakeFileInfo{modTime: testModTime, size: 1234}, time.Time{}, photo.ThumbNone)

	require.Equal(t, photo.IDFor(path), p.ID(), "IDはパスから導く")
	require.Equal(t, path, p.Path())
	require.Equal(t, int64(1234), p.Size())
	require.True(t, p.ModTime().Equal(testModTime))
}
