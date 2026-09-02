package photo

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKindOf(t *testing.T) {
	tests := map[string]Kind{
		"a.jpg":              KindRaster,
		"a.jpeg":             KindRaster,
		"a.png":              KindRaster,
		"a.gif":              KindRaster,
		"a.webp":             KindRaster,
		"A.JPG":              KindRaster, // 大文字小文字を区別しない
		"a.heic":             KindOpaque, // デコードせず素のまま配信する
		"a.HEIF":             KindOpaque,
		"a.mp4":              KindUnsupported, // 動画は対象外
		"a.mov":              KindUnsupported,
		"a.txt":              KindUnsupported,
		"noext":              KindUnsupported,
		"/photos/2020/b.png": KindRaster, // フルパスでも拡張子で判定する
	}
	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, want, KindOf(name))
		})
	}
}

func TestIsSupported(t *testing.T) {
	require.True(t, IsSupported("a.jpg"))
	require.True(t, IsSupported("a.heic"))
	require.False(t, IsSupported("a.mp4"))
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
			require.Equal(t, want, ContentType(name))
		})
	}
}

// testID は32文字のダミーID。IDFor の出力と同じ形（16進32文字）にしてある。
const testID = "abcdef0123456789abcdef0123456789"

func TestCachePathShardsByFirstTwoChars(t *testing.T) {
	require.Equal(t, filepath.Join("/data/thumbs", "ab", testID+".jpg"),
		CachePath("/data/thumbs", testID))
}

func TestThumbPathBySource(t *testing.T) {
	const cacheDir = "/data/thumbs"
	tests := []struct {
		name string
		p    Photo
		want string
		ok   bool
	}{
		{
			name: "自前で生成したものはキャッシュから引く",
			p:    Photo{ID: testID, Path: "/photos/a.jpg", ThumbSource: ThumbFamifo},
			want: filepath.Join(cacheDir, "ab", testID+".jpg"),
			ok:   true,
		},
		{
			name: "借りたものは @eaDir から引く",
			p:    Photo{ID: testID, Path: "/photos/a.heic", ThumbSource: ThumbSyno},
			want: "/photos/@eaDir/a.heic/SYNOPHOTO_THUMB_M.jpg",
			ok:   true,
		},
		{
			name: "借りるものも作れるものも無ければ ok=false",
			p:    Photo{ID: testID, Path: "/photos/a.heic", ThumbSource: ThumbNone},
			want: "",
			ok:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ThumbPath(tt.p, cacheDir)
			require.Equal(t, tt.ok, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

// XLに差し替えるのは「HEICで、かつ借りている」ときだけ。
// 他の5通りはすべて原本を配信する。
func TestFullPathSwapsInTheXLOnlyForBorrowedOpaquePhotos(t *testing.T) {
	tests := []struct {
		name string
		p    Photo
		want string
	}{
		{
			name: "HEIC + 借りている → SynologyのXL",
			p:    Photo{Path: "/photos/a.heic", ThumbSource: ThumbSyno},
			want: "/photos/@eaDir/a.heic/SYNOPHOTO_THUMB_XL.jpg",
		},
		{
			name: "HEIC + 借りていない → 原本（Safariでしか見えないが他に出せるものが無い）",
			p:    Photo{Path: "/photos/a.heic", ThumbSource: ThumbNone},
			want: "/photos/a.heic",
		},
		{
			name: "HEIC + 自前生成 → 原本（HEICは自前生成しないので実際には起きない）",
			p:    Photo{Path: "/photos/a.heic", ThumbSource: ThumbFamifo},
			want: "/photos/a.heic",
		},
		{
			name: "JPEG + 借りている → 原本（借りるのは一覧用だけ）",
			p:    Photo{Path: "/photos/a.jpg", ThumbSource: ThumbSyno},
			want: "/photos/a.jpg",
		},
		{
			name: "JPEG + 自前生成 → 原本",
			p:    Photo{Path: "/photos/a.jpg", ThumbSource: ThumbFamifo},
			want: "/photos/a.jpg",
		},
		{
			name: "JPEG + サムネイル無し → 原本",
			p:    Photo{Path: "/photos/a.jpg", ThumbSource: ThumbNone},
			want: "/photos/a.jpg",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, FullPath(tt.p))
		})
	}
}

// FullPath の戻り値からMIMEが引けることが、ハンドラ側で分岐を持たずに済む根拠。
func TestContentTypeOfTheBorrowedXLIsJPEG(t *testing.T) {
	p := Photo{Path: "/photos/a.heic", ThumbSource: ThumbSyno}
	require.Equal(t, "image/jpeg", ContentType(FullPath(p)))
}

func TestContentTypeOfAnUnborrowedHEICIsHEIC(t *testing.T) {
	p := Photo{Path: "/photos/a.heic", ThumbSource: ThumbNone}
	require.Equal(t, "image/heic", ContentType(FullPath(p)))
}
