package main

import (
	"fmt"
	"os"

	"github.com/yendo/famifo-proto/internal/config"
)

func main() {
	cfg, err := config.Parse(os.Args[1:], os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Printf("dir=%s data=%s addr=%s thumb=%d\n",
		cfg.PhotoDir, cfg.DataDir, cfg.Addr, cfg.ThumbSize)
}
