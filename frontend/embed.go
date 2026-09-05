// Package frontend 以 go:embed 內嵌 Vite 產出的 dist/（需先 `npm run build`）。
package frontend

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist 回傳以 dist/ 為根的檔案系統（index.html 在根目錄）。
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
