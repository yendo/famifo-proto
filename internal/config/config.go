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

// Config はアプリの実行時設定。すべてコマンドライン引数から与えられる。
type Config struct {
	PhotoDir  string // 写真を収集するルートディレクトリ
	DataDir   string // DBとサムネイルキャッシュの置き場
	Addr      string // HTTPの待ち受けアドレス
	ThumbSize int    // サムネイルの長辺ピクセル数
}

// Parse は引数を解析して検証済みのConfigを返す。argsにはプログラム名を含めない。
func Parse(args []string, stderr io.Writer) (Config, error) {
	fs := flag.NewFlagSet("famifo", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var c Config
	fs.StringVar(&c.PhotoDir, "dir", "", "写真を収集するディレクトリ (必須)")
	fs.StringVar(&c.DataDir, "data", "./famifo-data", "DBとサムネイルキャッシュの保存先")
	fs.StringVar(&c.Addr, "addr", ":8080", "HTTPの待ち受けアドレス")
	fs.IntVar(&c.ThumbSize, "thumb", 480, "サムネイルの長辺ピクセル数")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	return c, c.Validate()
}

// Validate は設定の不備を報告する。ここでのエラーは起動を中止させる。
func (c Config) Validate() error {
	if c.PhotoDir == "" {
		return errors.New("-dir は必須です")
	}
	fi, err := os.Stat(c.PhotoDir)
	if err != nil {
		return fmt.Errorf("-dir を読めません: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("-dir はディレクトリではありません: %s", c.PhotoDir)
	}
	if c.ThumbSize < 1 || c.ThumbSize > 4096 {
		return fmt.Errorf("-thumb は 1..4096 で指定してください: %d", c.ThumbSize)
	}
	if c.Addr == "" {
		return errors.New("-addr は必須です")
	}
	if inside, err := dirContains(c.PhotoDir, c.DataDir); err != nil {
		return fmt.Errorf("-data を解決できません: %w", err)
	} else if inside {
		return fmt.Errorf("-data は -dir の外に置いてください（自己増殖の原因になります）: %s は %s の中です", c.DataDir, c.PhotoDir)
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

// ThumbDir はサムネイルキャッシュのルートを返す。
func (c Config) ThumbDir() string { return filepath.Join(c.DataDir, "thumbs") }
