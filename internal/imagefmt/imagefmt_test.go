package imagefmt_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/imagefmt"
)

// 拡張子ごとに3つの問いへの答えを固定する。「インデックスに載せるか」「自前で
// サムネイルを作れるか」「動画か」は独立している。HEICは載せるが作れず、動画は
// 載せるが作れず、しかも動画である。
func TestSupportedDecodableAndVideoByExtension(t *testing.T) {
	t.Parallel()
	tests := map[string]struct{ supported, decodable, video bool }{
		"a.jpg":              {true, true, false},
		"a.jpeg":             {true, true, false},
		"a.png":              {true, true, false},
		"a.gif":              {true, true, false},
		"a.webp":             {true, true, false},
		"A.JPG":              {true, true, false},  // 大文字小文字を区別しない
		"a.heic":             {true, false, false}, // 載せるが、デコードは @eaDir 頼み
		"a.HEIF":             {true, false, false},
		"a.mp4":              {true, false, true}, // 載せるが、絵は借りるしかない
		"a.MOV":              {true, false, true},
		"a.avi":              {false, false, false}, // 実物を見るまで足さない
		"a.webm":             {false, false, false},
		"a.txt":              {false, false, false},
		"noext":              {false, false, false},
		"/photos/2020/b.png": {true, true, false}, // フルパスでも拡張子で判定する
	}
	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, want.supported, imagefmt.IsSupported(name), "IsSupported")
			require.Equal(t, want.decodable, imagefmt.IsDecodable(name), "IsDecodable")
			require.Equal(t, want.video, imagefmt.IsVideo(name), "IsVideo")
		})
	}
}

func TestContentType(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"a.jpg":  "image/jpeg",
		"a.jpeg": "image/jpeg",
		"a.png":  "image/png",
		"a.gif":  "image/gif",
		"a.webp": "image/webp",
		"a.heic": "image/heic",
		"a.heif": "image/heif",
		"a.mp4":  "video/mp4",
		"a.mov":  "video/quicktime",
		"a.txt":  "application/octet-stream",
	}
	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, want, imagefmt.ContentType("/photos/"+name))
		})
	}
}
