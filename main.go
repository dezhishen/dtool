package main

import (
	"errors"
	"os"

	"github.com/dezhishen/dtool/cmd"
	"github.com/dezhishen/dtool/internal/buildinfo"
	"github.com/dezhishen/dtool/internal/updater"
	"github.com/dezhishen/dtool/pkg/types"
)

func main() {
	updater.CleanupOld() // 清理上次升级遗留的 .old（Windows）
	if err := cmd.Execute(buildinfo.Get().Version); err != nil {
		code := types.CodeGeneral
		var te *types.Error
		if errors.As(err, &te) {
			code = te.Code
		}
		os.Exit(code)
	}
}
