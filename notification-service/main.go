package main

import (
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"heatlab/internal/store"
	"heatlab/internal/web"
)

type Message struct {
	ID            string    `json:"id"`
	MeasurementID string    `json:"measurement_id"`
	Text          string    `json:"text"`
	Read          bool      `json:"read"`
	CreatedAt     time.Time `json:"created_at"`
}

func router(db *store.Store[Message]) http.Handler {
	r := web.Router("notification-service")
	r.Get("/notifications", func(w http.ResponseWriter, r *http.Request) { web.JSON(w, 200, db.List()) })
	r.Post("/notifications", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			ID            string `json:"id"`
			MeasurementID string `json:"measurement_id"`
			Text          string `json:"text"`
		}
		if !web.Decode(w, r, &p) {
			return
		}
		p.Text = strings.TrimSpace(p.Text)
		if !web.ValidID(p.ID) || !web.ValidID(p.MeasurementID) || p.Text == "" || utf8.RuneCountInString(p.Text) > 240 {
			web.Error(w, 422, "invalid_notification")
			return
		}
		v, created := db.Insert(p.ID, Message{ID: p.ID, MeasurementID: p.MeasurementID, Text: p.Text, CreatedAt: time.Now().UTC()})
		if !created {
			if v.MeasurementID != p.MeasurementID || v.Text != p.Text {
				web.Error(w, 409, "notification_conflict")
				return
			}
			web.JSON(w, 200, v)
			return
		}
		slog.Info("notification created", "id", v.ID, "measurement_id", v.MeasurementID, "text", v.Text)
		w.Header().Set("Location", "/notifications/"+v.ID)
		web.JSON(w, 201, v)
	})
	r.Get("/notifications/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, ok := db.Get(chi.URLParam(r, "id"))
		if !ok {
			web.Error(w, 404, "notification_not_found")
			return
		}
		web.JSON(w, 200, v)
	})
	r.Patch("/notifications/{id}", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Read *bool `json:"read"`
		}
		if !web.Decode(w, r, &p) {
			return
		}
		if p.Read == nil {
			web.Error(w, 422, "read_required")
			return
		}
		v, ok := db.Update(chi.URLParam(r, "id"), func(v Message) Message {
			v.Read = *p.Read
			return v
		})
		if !ok {
			web.Error(w, 404, "notification_not_found")
			return
		}
		web.JSON(w, 200, v)
	})
	r.Delete("/notifications/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !db.Delete(chi.URLParam(r, "id")) {
			web.Error(w, 404, "notification_not_found")
			return
		}
		w.WriteHeader(204)
	})
	return r
}

func main() {
	if err := web.Serve("notification-service", "127.0.0.1:5002", router(store.New[Message]())); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
