package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"heatlab/internal/store"
	"heatlab/internal/web"
)

type Packet struct {
	SourceID   string    `json:"source_id"`
	StreamID   string    `json:"stream_id"`
	Seq        int64     `json:"seq"`
	MeasuredAt time.Time `json:"measured_at"`
	Kind       string    `json:"kind"`
	Value      *float64  `json:"value"`
}

type Measurement struct {
	ID string `json:"id"`
	Packet
	ReviewNote         string `json:"review_note"`
	NotificationStatus string `json:"notification_status"`
	Deleted            bool   `json:"-"`
}

func (p Packet) id() string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", p.SourceID, p.StreamID, p.Seq)))
	return hex.EncodeToString(sum[:])
}

func (p Packet) equal(other Packet) bool {
	return p.SourceID == other.SourceID && p.StreamID == other.StreamID &&
		p.Seq == other.Seq && p.MeasuredAt.Equal(other.MeasuredAt) &&
		p.Kind == other.Kind && *p.Value == *other.Value
}

type service struct {
	db   *store.Store[Measurement]
	deps dependencies
}

func (s service) router() http.Handler {
	r := web.Router("telemetry-service")
	r.Get("/measurements", func(w http.ResponseWriter, r *http.Request) {
		items := s.db.List()
		active := items[:0]
		for _, v := range items {
			if !v.Deleted {
				active = append(active, v)
			}
		}
		web.JSON(w, 200, active)
	})
	r.Post("/measurements", s.ingest)
	r.Get("/measurements/{id}", func(w http.ResponseWriter, r *http.Request) {
		if v, ok := s.get(w, r); ok {
			web.JSON(w, 200, v)
		}
	})
	r.Patch("/measurements/{id}", s.patch)
	r.Delete("/measurements/{id}", s.remove)
	r.Post("/measurements/{id}/notification", s.retryNotification)
	return r
}

func (s service) get(w http.ResponseWriter, r *http.Request) (Measurement, bool) {
	v, ok := s.db.Get(chi.URLParam(r, "id"))
	if !ok || v.Deleted {
		web.Error(w, 404, "measurement_not_found")
		return Measurement{}, false
	}
	return v, true
}

func (s service) ingest(w http.ResponseWriter, r *http.Request) {
	var p Packet
	if !web.Decode(w, r, &p) {
		return
	}
	if !web.ValidID(p.SourceID) || !web.ValidID(p.StreamID) || p.Seq < 1 ||
		p.MeasuredAt.IsZero() || p.Value == nil || (p.Kind != "measurement" && p.Kind != "alarm") {
		web.Error(w, 422, "invalid_packet")
		return
	}
	p.MeasuredAt = p.MeasuredAt.UTC()
	id := p.id()

	// A saved packet can be replayed even when the registry is unavailable.
	v, found := s.db.Get(id)
	code := http.StatusOK
	if !found {
		allowed, reason := s.deps.checkSource(r.Context(), p.SourceID)
		if !allowed {
			if reason == "source_unavailable" {
				w.Header().Set("Retry-After", "1")
				web.Error(w, 503, reason)
			} else {
				web.Error(w, 422, reason)
			}
			return
		}
		notification := "not_required"
		if p.Kind == "alarm" {
			notification = "pending"
		}
		var inserted bool
		v, inserted = s.db.Insert(id, Measurement{ID: id, Packet: p, NotificationStatus: notification})
		if inserted {
			code = http.StatusCreated
			w.Header().Set("Location", "/measurements/"+id)
		}
	}
	if v.Deleted {
		web.Error(w, 410, "measurement_deleted")
		return
	}
	if !v.Packet.equal(p) {
		web.Error(w, 409, "measurement_conflict")
		return
	}
	// HTTP notification happens after saving the measurement. A timeout cannot undo the save.
	v = s.deliver(r, v)
	web.JSON(w, code, v)
}

func (s service) deliver(r *http.Request, v Measurement) Measurement {
	if v.NotificationStatus != "pending" {
		return v
	}
	if err := s.deps.notify(r.Context(), v); err != nil {
		slog.Warn("notification pending", "measurement_id", v.ID,
			"request_id", middleware.GetReqID(r.Context()), "error", err)
		if current, ok := s.db.Get(v.ID); ok && !current.Deleted {
			return current // Another request may already have marked this record sent.
		}
		return v
	}
	current, ok := s.db.Update(v.ID, func(x Measurement) Measurement {
		if !x.Deleted {
			x.NotificationStatus = "sent"
		}
		return x
	})
	if ok && !current.Deleted {
		return current
	}
	return v
}

func (s service) retryNotification(w http.ResponseWriter, r *http.Request) {
	v, ok := s.get(w, r)
	if !ok {
		return
	}
	if v.Kind != "alarm" {
		web.Error(w, 409, "notification_not_required")
		return
	}
	v = s.deliver(r, v)
	if v.NotificationStatus == "pending" {
		w.Header().Set("Retry-After", "1")
		web.Error(w, 503, "notification_unavailable")
		return
	}
	web.JSON(w, 200, v)
}

func (s service) patch(w http.ResponseWriter, r *http.Request) {
	var p struct {
		ReviewNote *string `json:"review_note"`
	}
	if !web.Decode(w, r, &p) {
		return
	}
	if p.ReviewNote == nil || utf8.RuneCountInString(*p.ReviewNote) > 240 {
		web.Error(w, 422, "invalid_review_note")
		return
	}
	v, ok := s.db.Update(chi.URLParam(r, "id"), func(v Measurement) Measurement {
		if !v.Deleted {
			v.ReviewNote = strings.TrimSpace(*p.ReviewNote)
		}
		return v
	})
	if !ok || v.Deleted {
		web.Error(w, 404, "measurement_not_found")
		return
	}
	web.JSON(w, 200, v)
}

func (s service) remove(w http.ResponseWriter, r *http.Request) {
	deleted := false
	s.db.Update(chi.URLParam(r, "id"), func(v Measurement) Measurement {
		if !v.Deleted {
			v.Deleted = true // Keep the idempotency key reserved until process restart.
			deleted = true
		}
		return v
	})
	if !deleted {
		web.Error(w, 404, "measurement_not_found")
		return
	}
	w.WriteHeader(204)
}

func main() {
	s := service{
		db: store.New[Measurement](),
		deps: dependencies{
			client: &http.Client{
				Timeout:       750 * time.Millisecond,
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			},
			sourceURL:       web.BaseURL(web.Env("SOURCE_URL", "http://127.0.0.1:5000")),
			notificationURL: web.BaseURL(web.Env("NOTIFICATION_URL", "http://127.0.0.1:5002")),
		},
	}
	if err := web.Serve("telemetry-service", "127.0.0.1:5001", s.router()); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
