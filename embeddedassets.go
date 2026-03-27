package embeddedassets

import (
	"embed"
	"io/fs"
)

//go:embed static/*
var bundled embed.FS

func StaticFS() fs.FS {
	sub, err := fs.Sub(bundled, "static")
	if err != nil {
		panic(err)
	}
	return sub
}
