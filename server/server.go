package server

import (
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"

	"github.com/example/filestoragebot/config"
	"github.com/example/filestoragebot/db"
)

func Start(cfg *config.Config, database *db.DB, notify func(int64, string)) error {
	handler := func(w http.ResponseWriter, r *http.Request) {
		name := path.Base(r.URL.Path)
		if name == "" || name == "." || name == "/" {
			http.NotFound(w, r)
			return
		}
		fp := filepath.Join(cfg.FileStoragePath, name)
		if _, err := os.Stat(fp); os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, fp)

		if f, err := database.GetFileByStorageName(name); err == nil {
			if f.Notify && notify != nil {
				notify(f.UserID, "Ваш файл '"+f.LocalName+"' скачан")
			}
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
