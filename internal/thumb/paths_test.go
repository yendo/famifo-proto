package thumb_test

// 配信するファイルの選択を確かめる。出どころはDBに持たずディスクの状態で決めるので、
// @eaDir と自前の置き場に実際にファイルを置いて確かめる。

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/yendo/famifo-proto/internal/photo"
	"github.com/yendo/famifo-proto/internal/synology"
	"github.com/yendo/famifo-proto/internal/thumb"
)

type pathFixture struct {
	pv       *thumb.Provider
	photoDir string
}

func newPathFixture(t *testing.T) *pathFixture {
	t.Helper()
	base := t.TempDir()
	pv, err := thumb.NewProvider(filepath.Join(base, "thumbs"))
	require.NoError(t, err)
	photoDir := filepath.Join(base, "photos")
	require.NoError(t, os.MkdirAll(photoDir, 0o755))
	return &pathFixture{pv: pv, photoDir: photoDir}
}

// addPhoto は原本を1つ置いて、その1枚を返す。
// どのテストも中身は見ないので、画像として妥当である必要はない。
func (f *pathFixture) addPhoto(t *testing.T, name string) photo.Photo {
	t.Helper()
	path := filepath.Join(f.photoDir, name)
	require.NoError(t, os.WriteFile(path, []byte("original"), 0o644))
	return photoOf(t, path)
}

// borrowable は Synologyが作った体のMとXLを写真の隣に置く。
func (f *pathFixture) borrowable(t *testing.T, p photo.Photo) {
	t.Helper()
	writeFileAt(t, synology.ThumbMPath(p.Path()), "eadir m")
	writeFileAt(t, synology.ThumbXLPath(p.Path()), "eadir xl")
}

// generated は自前で生成した体のサムネイルを、その版の置き場に置く。
func (f *pathFixture) generated(t *testing.T, p photo.Photo) string {
	t.Helper()
	out := f.pv.GeneratedPath(p)
	writeFileAt(t, out, "generated thumb")
	return out
}

func writeFileAt(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

func TestSmallPathPrefersTheBorrowedThumb(t *testing.T) {
	f := newPathFixture(t)
	p := f.addPhoto(t, "a.jpg")
	own := f.generated(t, p)
	f.borrowable(t, p)

	got, contentType, ok := f.pv.SmallPath(p)

	require.True(t, ok)
	require.Equal(t, synology.ThumbMPath(p.Path()), got,
		"実ライブラリではほぼ全てに @eaDir があるので先に見る")
	require.NotEqual(t, own, got)
	require.Equal(t, "image/jpeg", contentType)
}

func TestSmallPathUsesTheGeneratedThumbWhenNothingToBorrow(t *testing.T) {
	f := newPathFixture(t)
	p := f.addPhoto(t, "a.jpg")
	own := f.generated(t, p)

	got, contentType, ok := f.pv.SmallPath(p)

	require.True(t, ok)
	require.Equal(t, own, got)
	require.Equal(t, "image/jpeg", contentType)
}

// ブラウザが表示できる形式なら、サムネイルが無くても原本を出せばタイルになる。
func TestSmallPathFallsBackToTheOriginal(t *testing.T) {
	f := newPathFixture(t)
	p := f.addPhoto(t, "a.jpg")

	got, contentType, ok := f.pv.SmallPath(p)

	require.True(t, ok)
	require.Equal(t, p.Path(), got)
	require.Equal(t, "image/jpeg", contentType)
}

// HEICはブラウザが表示できないので、原本を出しても割れたタイルになるだけである。
// 出せるものが無いことを伝えて、配信側にプレースホルダを出させる。
func TestSmallPathHasNothingToShowForAnUnborrowedHEIC(t *testing.T) {
	f := newPathFixture(t)
	p := f.addPhoto(t, "a.heic")

	got, contentType, ok := f.pv.SmallPath(p)

	require.False(t, ok)
	require.Empty(t, got)
	require.Empty(t, contentType)
}

// 取り込みのあとでDSMがサムネイルを作った場合。出どころをDBに焼いていたころは、
// 原本のmtimeが動かない限り再取り込みされないため、永久に反映されなかった。
func TestSmallPathSeesAThumbThatAppearsAfterIndexing(t *testing.T) {
	f := newPathFixture(t)
	p := f.addPhoto(t, "a.heic")
	_, _, ok := f.pv.SmallPath(p)
	require.False(t, ok, "この時点では出せるものが無い")

	f.borrowable(t, p)

	got, _, ok := f.pv.SmallPath(p)
	require.True(t, ok)
	require.Equal(t, synology.ThumbMPath(p.Path()), got,
		"取り込み直さなくても、次の配信から借りたものに切り替わる")
}

// 名前に版が入っているので、別の版のサムネイルは引き当たらない。
func TestSmallPathIgnoresAThumbFromAnotherVersion(t *testing.T) {
	f := newPathFixture(t)
	p := f.addPhoto(t, "a.jpg")
	stale := photo.Restore(p.Path(), p.TakenAt(), p.ModTime().Add(-time.Hour))
	writeFileAt(t, f.pv.GeneratedPath(stale), "古い版のサムネイル")

	got, _, ok := f.pv.SmallPath(p)

	require.True(t, ok)
	require.Equal(t, p.Path(), got, "版が違えば無いものとして扱い、原本に落ちる")
}

// 1ディレクトリにファイルが集中しないよう、IDの先頭2文字で分割する。
func TestGeneratedPathShardsByTheFirstTwoCharsOfTheID(t *testing.T) {
	f := newPathFixture(t)
	p := f.addPhoto(t, "a.jpg")

	got := f.pv.GeneratedPath(p)

	require.Equal(t, p.ID()[:2], filepath.Base(filepath.Dir(got)))
	require.Equal(t, ".jpg", filepath.Ext(got), "自前の出力は常にJPEG")
}

// 名前に元画像の版が入るので、写真が差し替われば別のファイルを指す。
// 鮮度を「サムネイルのほうが新しいか」で測らずに済ませるための土台。
func TestGeneratedPathVariesWithTheSourceVersion(t *testing.T) {
	f := newPathFixture(t)
	p := f.addPhoto(t, "a.jpg")
	older := photo.Restore(p.Path(), p.TakenAt(), p.ModTime().Add(-time.Hour))

	require.NotEqual(t, f.pv.GeneratedPath(p), f.pv.GeneratedPath(older),
		"版が違えば別の名前になる")
	require.Equal(t,
		filepath.Dir(f.pv.GeneratedPath(p)), filepath.Dir(f.pv.GeneratedPath(older)),
		"置き場は同じ")
}

// XLに差し替えるのは「自前でデコードできない形式で、かつ借りられる」ときだけ。
func TestLargePathSwapsInTheXLOnlyForBorrowedOpaquePhotos(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		borrowed bool
		wantXL   bool
		wantType string
	}{
		{"HEIC + 借りられる → SynologyのXL", "a.heic", true, true, "image/jpeg"},
		{"HEIC + 借りられない → 原本（Safariでしか見えないが他に出せるものが無い）",
			"a.heic", false, false, "image/heic"},
		{"JPEG + 借りられる → 原本（借りるのは見えないものの代替に限る）",
			"a.jpg", true, false, "image/jpeg"},
		{"JPEG + 借りられない → 原本", "a.jpg", false, false, "image/jpeg"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPathFixture(t)
			p := f.addPhoto(t, tt.file)
			if tt.borrowed {
				f.borrowable(t, p)
			}

			got, contentType := f.pv.LargePath(p)

			want := p.Path()
			if tt.wantXL {
				want = synology.ThumbXLPath(p.Path())
			}
			require.Equal(t, want, got)
			require.Equal(t, tt.wantType, contentType)
		})
	}
}
