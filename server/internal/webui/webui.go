package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var assets embed.FS

var Dist fs.FS = mustSub(assets, "dist")

func mustSub(f embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {

		panic(err)
	}
	return sub
}

func Built() bool {
	_, err := fs.Stat(Dist, "index.html")
	return err == nil
}
