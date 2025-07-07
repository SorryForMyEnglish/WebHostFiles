package server

import (
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/example/filestoragebot/config"
	"github.com/example/filestoragebot/db"
)

func Start(cfg *config.Config, database *db.DB, notify func(int64, string)) error {
	handler := func(w http.ResponseWriter, r *http.Request) {
		slug := path.Base(r.URL.Path)
		if slug == "" || slug == "." || slug == "/" {
			http.NotFound(w, r)
			return
		}

		link := strings.TrimRight(cfg.Domain, "/") + "/" + slug
		f, err := database.GetFileByLink(link)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		fp := filepath.Join(cfg.FileStoragePath, f.StorageName)
		if _, err := os.Stat(fp); os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, fp)

		if f.Notify && notify != nil {
			notify(f.UserID, "Ваш файл '"+f.LocalName+"' скачан")
		}
	}
	http.HandleFunc("/", handler)

	addr := cfg.HTTPAddress
	if cfg.TLSCert != "" && cfg.TLSKey != "" {
		log.Printf("Serving HTTPS on %s", addr)
		return http.ListenAndServeTLS(addr, cfg.TLSCert, cfg.TLSKey, nil)
	}
	log.Printf("Serving HTTP on %s", addr)
	return http.ListenAndServe(addr, nil)
}
