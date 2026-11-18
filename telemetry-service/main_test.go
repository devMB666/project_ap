package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"heatlab/internal/store"
)

func newTestService(source, notifications string) (service, *httptest.Server) {
	s := service{
		db: store.New[Measurement](),
		deps: dependencies{
			client:    &http.Client{Timeout: time.Second},
			sourceURL: source, notificationURL: notifications,
		},
	}
	return s, httptest.NewServer(s.router())
}

const alarm = `{"source_id":"source-1","stream_id":"temp","seq":1,"measured_at":"2026-11-13T00:00:00Z","kind":"alarm","value":95}`

func post(url, body string) (int, Measurement, error) {
	resp, err := http.Post(url+"/measurements", "application/json", strings.NewReader(body))
	if err != nil {
		return 0, Measurement{}, err
	}
	defer resp.Body.Close()
	var v Measurement
	err = json.NewDecoder(resp.Body).Decode(&v)
	return resp.StatusCode, v, err
}

func TestConcurrentReplayCreatesOneMeasurement(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"source-1","enabled":true}`)
	}))
	defer source.Close()
	s, api := newTestService(source.URL, "")
	defer api.Close()
	input := strings.Replace(alarm, `"kind":"alarm"`, `"kind":"measurement"`, 1)
	var wg sync.WaitGroup
	var created atomic.Int32
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _, err := post(api.URL, input)
			if err != nil || (code != 201 && code != 200) {
				t.Errorf("unexpected response: status=%d err=%v", code, err)
			}
			if code == 201 {
				created.Add(1)
			}
		}()
	}
	wg.Wait()
	if created.Load() != 1 || len(s.db.List()) != 1 {
		t.Fatalf("expected one creation and one record; got %d, %d", created.Load(), len(s.db.List()))
	}
}

func TestReplayAfterLostNotificationResponse(t *testing.T) {
	var lookups atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lookups.Add(1)
		fmt.Fprint(w, `{"id":"source-1","enabled":true}`)
	}))
	defer source.Close()
	received := store.New[string]()
	notifications := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&v); err != nil || v.ID == "" {
			w.WriteHeader(400)
			return
		}
		if _, added := received.Insert(v.ID, v.ID); added {
			conn, _, err := http.NewResponseController(w).Hijack()
			if err == nil {
				conn.Close() // The record is saved, but the HTTP response is lost.
			} else {
				t.Errorf("simulate lost response: %v", err)
			}
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": v.ID, "measurement_id": v.ID})
	}))
	defer notifications.Close()
	_, api := newTestService(source.URL, notifications.URL)
	defer api.Close()

	code, first, err := post(api.URL, alarm)
	if code != 201 || err != nil || first.NotificationStatus != "pending" {
		t.Fatalf("first call: HTTP %d, %+v, %v", code, first, err)
	}
	code, second, err := post(api.URL, alarm)
	if code != 200 || err != nil || second.NotificationStatus != "sent" || first.ID != second.ID {
		t.Fatalf("replay: HTTP %d, %+v, %v", code, second, err)
	}
	if lookups.Load() != 1 || len(received.List()) != 1 {
		t.Fatalf("expected one registry lookup and one notification")
	}
}

func TestSourceTimeoutLeavesStorageEmpty(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer source.Close()
	s := service{
		db: store.New[Measurement](),
		deps: dependencies{
			client: &http.Client{Timeout: 40 * time.Millisecond}, sourceURL: source.URL,
		},
	}
	api := httptest.NewServer(s.router())
	defer api.Close()
	res, err := http.Post(api.URL+"/measurements", "application/json", strings.NewReader(alarm))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 503 || !strings.Contains(string(body), "source_unavailable") || len(s.db.List()) != 0 {
		t.Fatalf("expected 503 and empty storage, got %d %s (%d records)", res.StatusCode, body, len(s.db.List()))
	}
}
