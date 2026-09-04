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

func TestKindOf(t *testing.T) {
	tests := map[string]photo.Kind{
		"a.jpg":              photo.KindRaster,
		"a.jpeg":             photo.KindRaster,
		"a.png":              photo.KindRaster,
		"a.gif":              photo.KindRaster,
		"a.webp":             photo.KindRaster,
		"A.JPG":              photo.KindRaster, // 大文字小文字を区別しない
		"a.heic":             photo.KindOpaque, // デコードせず素のまま配信する
		"a.HEIF":             photo.KindOpaque,
		"a.mp4":              photo.KindUnsupported, // 動画は対象外
		"a.mov":              photo.KindUnsupported,
		"a.txt":              photo.KindUnsupported,
		"noext":              photo.KindUnsupported,
		"/photos/2020/b.png": photo.KindRaster, // フルパスでも拡張子で判定する
	}
	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, want, photo.KindOf(name))
		})
	}
}

func TestIsSupported(t *testing.T) {
	require.True(t, photo.IsSupported("a.jpg"))
	require.True(t, photo.IsSupported("a.heic"))
	require.False(t, photo.IsSupported("a.mp4"))
}

func TestContentType(t *testing.T) {
	tests := map[string]string{
		"a.jpg":  "image/jpeg",
		"a.jpeg": "image/jpeg",
		"a.png":  "image/png",
		"a.gif":  "image/gif",
		"a.webp": "image/webp",
		"a.heic": "image/heic",
		"a.heif": "image/heif",
		"a.txt":  "application/octet-stream",
	}
	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, want, photo.ContentType(name))
		})
	}
}

// testID は32文字のダミーID。photo.IDFor の出力と同じ形（16進32文字）にしてある。
const testID = "abcdef0123456789abcdef0123456789"

func TestFamifoThumbPathShardsByFirstTwoChars(t *testing.T) {
	require.Equal(t, filepath.Join("/data/thumbs", "ab", testID+".jpg"),
		photo.FamifoThumbPath("/data/thumbs", testID))
}

func TestThumbPathBySource(t *testing.T) {
	const thumbDir = "/data/thumbs"
	tests := []struct {
		name string
		p    photo.Photo
		want string
		ok   bool
	}{
		{
			name: "自前で生成したものは自分の置き場から引く",
			p:    photo.Photo{ID: testID, Path: "/photos/a.jpg", ThumbSource: photo.ThumbFamifo},
			want: filepath.Join(thumbDir, "ab", testID+".jpg"),
			ok:   true,
		},
		{
			name: "借りたものは @eaDir から引く",
			p:    photo.Photo{ID: testID, Path: "/photos/a.heic", ThumbSource: photo.ThumbSyno},
			want: "/photos/@eaDir/a.heic/SYNOPHOTO_THUMB_M.jpg",
			ok:   true,
		},
		{
			name: "借りるものも作れるものも無ければ ok=false",
			p:    photo.Photo{ID: testID, Path: "/photos/a.heic", ThumbSource: photo.ThumbNone},
			want: "",
			ok:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := photo.ThumbPath(tt.p, thumbDir)
			require.Equal(t, tt.ok, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestHasThumbAgreesWithThumbSource(t *testing.T) {
	tests := []struct {
		name string
		src  photo.ThumbSource
		want bool
	}{
		{
			name: "自前で生成したものはある",
			src:  photo.ThumbFamifo,
			want: true,
		},
		{
			name: "借りたものもある",
			src:  photo.ThumbSyno,
			want: true,
		},
		{
			name: "どちらでもなければ無い",
			src:  photo.ThumbNone,
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := photo.Photo{ID: testID, Path: "/photos/a.jpg", ThumbSource: tt.src}
			require.Equal(t, tt.want, photo.HasThumb(p))
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
			p:    photo.Photo{Path: "/photos/a.heic", ThumbSource: photo.ThumbSyno},
			want: "/photos/@eaDir/a.heic/SYNOPHOTO_THUMB_XL.jpg",
		},
		{
			name: "HEIC + 借りていない → 原本（Safariでしか見えないが他に出せるものが無い）",
			p:    photo.Photo{Path: "/photos/a.heic", ThumbSource: photo.ThumbNone},
			want: "/photos/a.heic",
		},
		{
			name: "HEIC + 自前生成 → 原本（HEICは自前生成しないので実際には起きない）",
			p:    photo.Photo{Path: "/photos/a.heic", ThumbSource: photo.ThumbFamifo},
			want: "/photos/a.heic",
		},
		{
			name: "JPEG + 借りている → 原本（借りるのは一覧用だけ）",
			p:    photo.Photo{Path: "/photos/a.jpg", ThumbSource: photo.ThumbSyno},
			want: "/photos/a.jpg",
		},
		{
			name: "JPEG + 自前生成 → 原本",
			p:    photo.Photo{Path: "/photos/a.jpg", ThumbSource: photo.ThumbFamifo},
			want: "/photos/a.jpg",
		},
		{
			name: "JPEG + サムネイル無し → 原本",
			p:    photo.Photo{Path: "/photos/a.jpg", ThumbSource: photo.ThumbNone},
			want: "/photos/a.jpg",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, photo.FullPath(tt.p))
		})
	}
}

// photo.FullPath の戻り値からMIMEが引けることが、ハンドラ側で分岐を持たずに済む根拠。
func TestContentTypeOfTheBorrowedXLIsJPEG(t *testing.T) {
	p := photo.Photo{Path: "/photos/a.heic", ThumbSource: photo.ThumbSyno}
	require.Equal(t, "image/jpeg", photo.ContentType(photo.FullPath(p)))
}

func TestContentTypeOfAnUnborrowedHEICIsHEIC(t *testing.T) {
	p := photo.Photo{Path: "/photos/a.heic", ThumbSource: photo.ThumbNone}
	require.Equal(t, "image/heic", photo.ContentType(photo.FullPath(p)))
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

	p := photo.New(path, fakeFileInfo{modTime: testModTime, size: 1234}, time.Time{})

	require.Equal(t, photo.IDFor(path), p.ID, "IDはパスから導く")
	require.Equal(t, path, p.Path)
	require.Equal(t, int64(1234), p.Size)
	require.True(t, p.ModTime.Equal(testModTime))
}
