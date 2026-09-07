// Package config はアプリの実行時設定の保持と検証を担う。
// 設定値をどこから読むか（コマンドライン引数の解析）は呼び出し側の責務。
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config はアプリの実行時設定。すべてコマンドライン引数から与えられる。
type Config struct {
	PhotoDirs   []string // 写真を収集するルートディレクトリ（複数可）
	DataDir     string   // DBとサムネイルの置き場
	Addr        string   // HTTPの待ち受けアドレス
	ScanWorkers int      // 同時に取り込む枚数（スキャンとfsnotifyの追従に共通）
	// ScanInterval はインデックスをディスクの実態と突き合わせ直す間隔。
	// fsnotify の取りこぼしはこれで回復する。
	ScanInterval time.Duration
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
	// 0を「自動」と読み替えない。既定値はフラグの側が runtime.NumCPU() で
	// 与えており、0が届くのは利用者が明示的に0を渡したときだけである。
	// 黙って読み替えると、走査が始まらない設定を無言で書き換えることになる。
	if c.ScanWorkers < 1 {
		return fmt.Errorf("-scan-workers は1以上にしてください: %d", c.ScanWorkers)
	}
	// 0を「無効」と読み替えない。待たずに走査を繰り返すことになり、それが
	// 止まらなくなる。回したくなければ十分に長い値を渡せばよい。
	if c.ScanInterval <= 0 {
		return fmt.Errorf("-scan-interval は正の値にしてください: %s", c.ScanInterval)
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
