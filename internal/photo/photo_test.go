package photo

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKindOf(t *testing.T) {
	tests := map[string]Kind{
		"a.jpg":            KindRaster,
		"a.jpeg":           KindRaster,
		"a.png":            KindRaster,
		"a.gif":            KindRaster,
		"a.webp":           KindRaster,
		"A.JPG":            KindRaster, // 大文字小文字を区別しない
		"a.heic":           KindOpaque, // デコードせず素のまま配信する
		"a.HEIF":           KindOpaque,
		"a.mp4":            KindUnsupported, // 動画は対象外
		"a.mov":            KindUnsupported,
		"a.txt":            KindUnsupported,
		"noext":            KindUnsupported,
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
