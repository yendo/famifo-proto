// Package config はコマンドライン引数の解析と検証を担う。
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrVersionRequested は -version が指定されたことを表す。
var ErrVersionRequested = errors.New("version requested")

// Config はアプリの実行時設定。すべてコマンドライン引数から与えられる。
type Config struct {
	PhotoDirs []string // 写真を収集するルートディレクトリ（複数可）
	DataDir   string   // DBとサムネイルの置き場
	Addr      string   // HTTPの待ち受けアドレス
}

// Parse は引数を解析して検証済みのConfigを返す。argsにはプログラム名を含めない。
func Parse(args []string, stderr io.Writer) (Config, error) {
	fs := flag.NewFlagSet("famifo", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var c Config
	var dirs string
	fs.StringVar(&dirs, "dir", "",
		fmt.Sprintf("写真を収集するディレクトリ (必須)。%q で区切って複数指定できる",
			string(filepath.ListSeparator)))
	fs.StringVar(&c.DataDir, "data", "./famifo-data", "DBとサムネイルの保存先")
	fs.StringVar(&c.Addr, "addr", ":8080", "HTTPの待ち受けアドレス")
	showVersion := fs.Bool("version", false, "バージョンを表示して終了する")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	// バージョンを表示するだけなので -dir は要らない。検証まで進めない。
	if *showVersion {
		return Config{}, ErrVersionRequested
	}
	// 空文字を SplitList に渡すと [""] ではなく [] が返るので、
	// 「未指定」は Validate 側の「1つ以上必須」で捕まる。
	c.PhotoDirs = filepath.SplitList(dirs)
	return c, c.Validate()
}

// Validate は設定の不備を報告する。ここでのエラーは起動を中止させる。
func (c Config) Validate() error {
	if len(c.PhotoDirs) == 0 {
		return errors.New("-dir は必須です")
	}
	for i, dir := range c.PhotoDirs {
		fi, err := os.Stat(dir)
		if err != nil {
			// ':' はUnixのパスに使える文字なので、それを含むディレクトリを
			// 渡すと意図しない位置で切れる。分割結果を見せて原因を読めるようにする。
			return fmt.Errorf("-dir を読めません: %w（-dir は %q で区切って解釈しました: %v）",
				err, string(filepath.ListSeparator), c.PhotoDirs)
		}
		if !fi.IsDir() {
			return fmt.Errorf("-dir はディレクトリではありません: %s", dir)
		}
		// 同じルートを2回走査しても無駄なだけ。入れ子は同じファイルを2回
		// 走査し、サムネイルを2回作る。
		for _, other := range c.PhotoDirs[i+1:] {
			nested, err := dirContains(dir, other)
			if err != nil {
				return fmt.Errorf("-dir を解決できません: %w", err)
			}
			if !nested {
				if nested, err = dirContains(other, dir); err != nil {
					return fmt.Errorf("-dir を解決できません: %w", err)
				}
			}
			if nested {
				return fmt.Errorf("-dir が重複または入れ子になっています: %s と %s", dir, other)
			}
		}
	}
	if c.Addr == "" {
		return errors.New("-addr は必須です")
	}
	for _, dir := range c.PhotoDirs {
		inside, err := dirContains(dir, c.DataDir)
		if err != nil {
			return fmt.Errorf("-data を解決できません: %w", err)
		}
		if inside {
			return fmt.Errorf("-data は -dir の外に置いてください（自己増殖の原因になります）: %s は %s の中です", c.DataDir, dir)
		}
	}
	return nil
}

// dirContains はabsパスに変換したうえで、dataがphoto自身か、その配下にあるかを判定する。
// filepath.Relを使うのは文字列プレフィックス比較を避けるため
// （例えば "/photos-data" は "/photos" の中ではない）。
func dirContains(photoDir, dataDir string) (bool, error) {
	absPhoto, err := filepath.Abs(photoDir)
	if err != nil {
		return false, err
	}
	absData, err := filepath.Abs(dataDir)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(absPhoto, absData)
	if err != nil {
		return false, err
	}
	if rel == "." {
		return true, nil // 同一ディレクトリ
	}
	first, _, _ := strings.Cut(rel, string(filepath.Separator))
	return first != "..", nil
}

// DBPath はSQLiteファイルのパスを返す。
func (c Config) DBPath() string { return filepath.Join(c.DataDir, "famifo.db") }

// ThumbDir は生成したサムネイルのルートを返す。
func (c Config) ThumbDir() string { return filepath.Join(c.DataDir, "thumbs") }
