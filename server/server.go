package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/example/filestoragebot/config"
	"github.com/example/filestoragebot/db"
	"github.com/example/filestoragebot/logdb"
	uaParser "github.com/mssola/user_agent"
)

func Start(cfg *config.Config, database *db.DB, logs *logdb.DB, notify func(int64, string)) error {
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

		ua := uaParser.New(r.UserAgent())
		osInfo := ua.OSInfo()
		platform := ua.Platform()
		model := ua.Model()
		browserName, browserVer := ua.Browser()

		ip := r.Header.Get("X-Forwarded-For")
		if ip == "" {
			ip, _, _ = net.SplitHostPort(r.RemoteAddr)
		} else {
			ip = strings.TrimSpace(strings.Split(ip, ",")[0])
		}

		var loc struct {
			City    string `json:"city"`
			Country string `json:"country"`
		}
		if resp, err := http.Get("https://ipinfo.io/" + ip + "/json"); err == nil {
			data, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			json.Unmarshal(data, &loc)
		}

		_ = logs.Add(f.ID, &logdb.Entry{
			IP:          ip,
			City:        loc.City,
			Country:     loc.Country,
			Platform:    platform,
			Model:       model,
			OSName:      osInfo.Name,
			OSVersion:   osInfo.Version,
			BrowserName: browserName,
			BrowserVer:  browserVer,
		})

		if f.Notify && notify != nil {
			info := fmt.Sprintf("\xF0\x9F\x95\x8B Файл: %s\n\xF0\x9F\x93\x9A \u0422\u0435\u0433: %s\n\xF0\x9F\x8C\x8D IP: %s\n\xF0\x9F\x97\xBD \u041B\u043E\u043A\u0430\u0446\u0438\u044F: %s, %s\n\xF0\x9F\x93\xB1 \u0423\u0441\u0442\u0440\u043E\u0439\u0441\u0442\u0432\u043E: %s %s\n\xF0\x9F\x92\xBB \u041E\u0421: %s %s\n\xF0\x9F\x8C\x90 \u0411\u0440\u0430\u0443\u0437\u0435\u0440: %s %s",
				f.LocalName, slug, ip, loc.City, loc.Country, platform, model, osInfo.Name, osInfo.Version, browserName, browserVer)
			notify(f.UserID, info)
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
