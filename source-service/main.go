package main

import (
	"log/slog"
	"net/http"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"heatlab/internal/store"
	"heatlab/internal/web"
)

type Source struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

func router(db *store.Store[Source]) http.Handler {
	r := web.Router("source-service")
	r.Get("/sources", func(w http.ResponseWriter, r *http.Request) { web.JSON(w, 200, db.List()) })
	r.Post("/sources", func(w http.ResponseWriter, r *http.Request) {
		var v Source
		if !web.Decode(w, r, &v) {
			return
		}
		v.Name = strings.TrimSpace(v.Name)
		if !web.ValidID(v.ID) || !validName(v.Name) {
			web.Error(w, 422, "invalid_source")
			return
		}
		if _, created := db.Insert(v.ID, v); !created {
			web.Error(w, 409, "source_exists")
			return
		}
		w.Header().Set("Location", "/sources/"+v.ID)
		web.JSON(w, 201, v)
	})
	r.Get("/sources/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, ok := db.Get(chi.URLParam(r, "id"))
		if !ok {
			web.Error(w, 404, "source_not_found")
			return
		}
		web.JSON(w, 200, v)
	})
	r.Patch("/sources/{id}", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Name    *string `json:"name"`
			Enabled *bool   `json:"enabled"`
		}
		if !web.Decode(w, r, &p) {
			return
		}
		if p.Name != nil {
			*p.Name = strings.TrimSpace(*p.Name)
		}
		if p.Name == nil && p.Enabled == nil || p.Name != nil && !validName(*p.Name) {
			web.Error(w, 422, "invalid_source_patch")
			return
		}
		v, ok := db.Update(chi.URLParam(r, "id"), func(v Source) Source {
			if p.Name != nil {
				v.Name = *p.Name
			}
			if p.Enabled != nil {
				v.Enabled = *p.Enabled
			}
			return v
		})
		if !ok {
			web.Error(w, 404, "source_not_found")
			return
		}
		web.JSON(w, 200, v)
	})
	r.Delete("/sources/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !db.Delete(chi.URLParam(r, "id")) {
			web.Error(w, 404, "source_not_found")
			return
		}
		w.WriteHeader(204)
	})
	return r
}

func validName(s string) bool { return s != "" && utf8.RuneCountInString(s) <= 100 }

func main() {
	if err := web.Serve("source-service", "127.0.0.1:5000", router(store.New[Source]())); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
