package embeddedassets

import (
	"embed"
	"io/fs"
)

//go:embed static/* data/panel.db
var bundled embed.FS

func StaticFS() fs.FS {
	sub, err := fs.Sub(bundled, "static")
	if err != nil {
		panic(err)
	}
	return sub
}

func BundledPanelDB() []byte {
	content, err := bundled.ReadFile("data/panel.db")
	if err != nil {
		panic(err)
	}
	return content
}
