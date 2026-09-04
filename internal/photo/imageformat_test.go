package photo_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/photo"
)

// 拡張子ごとに2つの問いへの答えを固定する。「インデックスに載せるか」と
// 「自前でサムネイルを作れるか」は独立で、HEICだけが載せるが作れない側に来る。
func TestSupportedAndDecodableByExtension(t *testing.T) {
	tests := map[string]struct{ supported, decodable bool }{
		"a.jpg":              {true, true},
		"a.jpeg":             {true, true},
		"a.png":              {true, true},
		"a.gif":              {true, true},
		"a.webp":             {true, true},
		"A.JPG":              {true, true},  // 大文字小文字を区別しない
		"a.heic":             {true, false}, // 載せるが、デコードは @eaDir 頼み
		"a.HEIF":             {true, false},
		"a.mp4":              {false, false}, // 動画は対象外
		"a.mov":              {false, false},
		"a.txt":              {false, false},
		"noext":              {false, false},
		"/photos/2020/b.png": {true, true}, // フルパスでも拡張子で判定する
	}
	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, want.supported, photo.IsSupportedFile(name), "IsSupportedFile")
			require.Equal(t, want.decodable, photo.IsDecodableFile(name), "IsDecodableFile")
		})
	}
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
			// 借りていない写真は原本を配信するので、MIMEは拡張子どおりになる。
			require.Equal(t, want, photo.Photo{Path: "/photos/" + name}.ContentType())
		})
	}
}
