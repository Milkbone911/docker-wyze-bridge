package camera

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/IDisposable/docker-wyze-bridge/internal/config"
	"github.com/IDisposable/docker-wyze-bridge/internal/go2rtcmgr"
)

func TestManager_SubstreamRegistrationFailureDoesNotFailCamera(t *testing.T) {
	streams := make(map[string]bool)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/streams" && r.Method == http.MethodGet:
			result := make(map[string]*go2rtcmgr.StreamInfo)
			for name := range streams {
				result[name] = &go2rtcmgr.StreamInfo{
					Producers: []go2rtcmgr.ProducerInfo{{URL: "wyze://test"}},
				}
			}
			_ = json.NewEncoder(w).Encode(result)

		case r.URL.Path == "/api/streams" && r.Method == http.MethodPut:
			name := r.URL.Query().Get("name")
			if strings.HasSuffix(name, substreamSuffix) {
				http.Error(w, "substream rejected", http.StatusInternalServerError)
				return
			}
			streams[name] = true
			w.WriteHeader(http.StatusOK)

		case r.URL.Path == "/api/streams" && r.Method == http.MethodDelete:
			delete(streams, r.URL.Query().Get("name"))
			w.WriteHeader(http.StatusOK)

		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := &config.Config{
		Quality:      "hd",
		Audio:        true,
		Substream:    true,
		SubQuality:   "sd",
		CamOverrides: make(map[string]config.CamOverride),
	}
	api := go2rtcmgr.NewAPIClient(srv.URL, zerolog.Nop())
	mgr := NewManager(cfg, nil, api, zerolog.Nop())
	cam := substreamTestCamera("patio", "HL_CFL2")
	mgr.cameras[cam.Name()] = cam

	mgr.connectCamera(context.Background(), cam)

	if cam.GetState() != StateStreaming {
		t.Errorf("state = %v, want Streaming despite substream failure", cam.GetState())
	}
	if cam.GetErrorCount() != 0 {
		t.Errorf("error count = %d, want 0", cam.GetErrorCount())
	}
	if cam.TUTKFailStreak() != 0 {
		t.Errorf("TUTK fail streak = %d, want 0", cam.TUTKFailStreak())
	}

	active, err := api.HasActiveProducer(context.Background(), "patio")
	if err != nil {
		t.Fatal(err)
	}
	if !active {
		t.Error("main stream should remain active")
	}
}

func TestManager_NonTUTKReconnectFailureStillDropsStaleSubstream(t *testing.T) {
	streams := map[string]bool{"patio-sub": true}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/streams" && r.Method == http.MethodGet:
			result := make(map[string]*go2rtcmgr.StreamInfo)
			for name := range streams {
				result[name] = &go2rtcmgr.StreamInfo{
					Producers: []go2rtcmgr.ProducerInfo{{URL: "wyze://stale"}},
				}
			}
			_ = json.NewEncoder(w).Encode(result)

		case r.URL.Path == "/api/streams" && r.Method == http.MethodDelete:
			delete(streams, r.URL.Query().Get("name"))
			w.WriteHeader(http.StatusOK)

		case r.URL.Path == "/api/streams" && r.Method == http.MethodPut:
			http.Error(w, "primary reconnect rejected", http.StatusInternalServerError)

		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := &config.Config{
		Quality:      "hd",
		Audio:        true,
		Substream:    true,
		SubQuality:   "sd",
		CamOverrides: make(map[string]config.CamOverride),
	}
	api := go2rtcmgr.NewAPIClient(srv.URL, zerolog.Nop())
	mgr := NewManager(cfg, nil, api, zerolog.Nop())
	cam := substreamTestCamera("patio", "HL_CFL2")
	cam.SetForceWebRTC(true)
	mgr.cameras[cam.Name()] = cam

	mgr.connectCamera(context.Background(), cam)

	if _, ok := streams["patio-sub"]; ok {
		t.Error("stale TUTK substream should be removed before failed WebRTC reconnect")
	}
	if cam.GetState() != StateError {
		t.Errorf("state = %v, want Error after primary reconnect failure", cam.GetState())
	}
}
