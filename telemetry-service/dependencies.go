package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

type dependencies struct {
	client                     *http.Client
	sourceURL, notificationURL string
}

func (d dependencies) call(ctx context.Context, method, url string, body []byte, out any) (int, error) {
	var input io.Reader
	if body != nil {
		input = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, input)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Request-ID", middleware.GetReqID(ctx))
	res, err := d.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode == 200 || res.StatusCode == 201 {
		err = json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(out)
	}
	// Drain short responses so the client can reuse a connection.
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	return res.StatusCode, err
}

func (d dependencies) checkSource(ctx context.Context, id string) (bool, string) {
	for attempt := 0; attempt < 2; attempt++ {
		var v struct {
			ID      string `json:"id"`
			Enabled *bool  `json:"enabled"`
		}
		status, err := d.call(ctx, http.MethodGet, d.sourceURL+"/sources/"+id, nil, &v)
		if err == nil {
			switch {
			case status == 200 && v.ID == id && v.Enabled != nil:
				if !*v.Enabled {
					return false, "source_disabled"
				}
				return true, ""
			case status == 404:
				return false, "source_not_found"
			case status < 500:
				return false, "source_unavailable"
			}
		}
		slog.Warn("source lookup failed", "request_id", middleware.GetReqID(ctx), "attempt", attempt+1, "status", status, "error", err)
		if attempt == 0 {
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return false, "source_unavailable"
			case <-timer.C:
			}
		}
	}
	return false, "source_unavailable"
}

func (d dependencies) notify(ctx context.Context, v Measurement) error {
	body, err := json.Marshal(map[string]string{
		"id": v.ID, "measurement_id": v.ID, "text": "Alarm from " + v.SourceID + " / " + v.StreamID,
	})
	if err != nil {
		return err
	}
	var reply struct {
		ID            string `json:"id"`
		MeasurementID string `json:"measurement_id"`
	}
	status, err := d.call(ctx, http.MethodPost, d.notificationURL+"/notifications", body, &reply)
	if err != nil {
		return err
	}
	if (status != 200 && status != 201) || reply.ID != v.ID || reply.MeasurementID != v.ID {
		return fmt.Errorf("invalid notification acknowledgment (HTTP %d)", status)
	}
	return nil
}
