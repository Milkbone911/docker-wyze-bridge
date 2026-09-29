package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/IDisposable/docker-wyze-bridge/internal/camera"
	"github.com/IDisposable/docker-wyze-bridge/internal/config"
	"github.com/IDisposable/docker-wyze-bridge/internal/go2rtcmgr"
	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

// testServer creates a Server with mock dependencies for testing.
func testServer(t *testing.T) (*Server, *go2rtcmgr.APIClient) {
	t.Helper()

	// Mock go2rtc API server
	go2rtcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/streams" && r.Method == "GET":
			json.NewEncoder(w).Encode(map[string]interface{}{})
		case r.URL.Path == "/api/streams" && r.Method == "PUT":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/api/streams" && r.Method == "DELETE":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/api/frame.jpeg":
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(go2rtcSrv.Close)

	cfg := &config.Config{
		BridgePort:   5080,
		Quality:      "hd",
		Audio:        true,
		CamOverrides: make(map[string]config.CamOverride),
	}

	go2rtcAPI := go2rtcmgr.NewAPIClient(go2rtcSrv.URL, zerolog.Nop())
	apiClient := wyzeapi.NewClient(wyzeapi.Credentials{}, "test", zerolog.Nop())
	camMgr := camera.NewManager(cfg, apiClient, go2rtcAPI, zerolog.Nop())

	srv := NewServer(Options{
		Config:    cfg,
		CameraMgr: camMgr,
		Version:   "test-version",
		Log:       zerolog.Nop(),
	})
	srv.SetGo2RTCAPI(go2rtcAPI)
	srv.startTime = time.Now()

	return srv, go2rtcAPI
}

func TestHandleHealth(t *testing.T) {
	srv, _ := testServer(t)

	req := httptest.NewRequest("GET", "/api/health", nil)
	w := httptest.NewRecorder()
	srv.handleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)

	if resp["status"] != "ok" {
		t.Errorf("status = %v", resp["status"])
	}
	if resp["version"] != "test-version" {
		t.Errorf("version = %v", resp["version"])
	}
	if _, ok := resp["uptime"]; !ok {
		t.Error("missing uptime")
	}
}

func TestHandleVersion(t *testing.T) {
	srv, _ := testServer(t)

	req := httptest.NewRequest("GET", "/api/version", nil)
	w := httptest.NewRecorder()
	srv.handleVersion(w, req)

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)

	if resp["version"] != "test-version" {
		t.Errorf("version = %v", resp["version"])
	}
}

func TestHandleAPICameras_Empty(t *testing.T) {
	srv, _ := testServer(t)

	req := httptest.NewRequest("GET", "/api/cameras", nil)
	w := httptest.NewRecorder()
	srv.handleAPICameras(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d", w.Code)
	}

	var resp []interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp) != 0 {
		t.Errorf("expected empty array, got %d items", len(resp))
	}
}

func TestHandleStreamsM3U8(t *testing.T) {
	srv, _ := testServer(t)

	req := httptest.NewRequest("GET", "/api/streams", nil)
	req.Host = "192.168.1.50:5080"
	w := httptest.NewRecorder()
	srv.handleStreamsM3U8(w, req)

	if ct := w.Header().Get("Content-Type"); ct != "application/vnd.apple.mpegurl" {
		t.Errorf("content-type = %q", ct)
	}

	body := w.Body.String()
	if !strings.Contains(body, "#EXTM3U") {
		t.Error("missing #EXTM3U header")
	}
}

func TestHandleStreamsM3U8_IncludesConfiguredSubstream(t *testing.T) {
	srv, _ := testServer(t)
	srv.cfg.Substream = true
	srv.cfg.SubQuality = "sd"

	cam := camera.NewCamera(wyzeapi.CameraInfo{
		Name:     "front_door",
		Nickname: "Front Door",
		Model:    "HL_PAN3",
		LanIP:    "10.0.0.5",
		P2PID:    "UID12345678901234567",
		ENR:      "enr123",
		MAC:      "AABBCCDDEEFF",
		DTLS:     true,
	}, "hd", true, false)
	srv.camMgr.InjectCamera("front_door", cam)

	req := httptest.NewRequest("GET", "/api/streams", nil)
	req.Host = "192.168.1.50:5080"
	w := httptest.NewRecorder()
	srv.handleStreamsM3U8(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "rtsp://192.168.1.50:8554/front_door\n") {
		t.Errorf("playlist missing main stream:\n%s", body)
	}
	if !strings.Contains(body, "rtsp://192.168.1.50:8554/front_door-sub\n") {
		t.Errorf("playlist missing substream:\n%s", body)
	}
	if !strings.Contains(body, "Front Door (sub)") {
		t.Errorf("playlist missing substream label:\n%s", body)
	}
}

func TestHandleStreamM3U8_ResolvesConfiguredSubstream(t *testing.T) {
	srv, _ := testServer(t)
	srv.cfg.Substream = true
	srv.cfg.SubQuality = "sd"

	cam := camera.NewCamera(wyzeapi.CameraInfo{
		Name:  "garage",
		Model: "HL_CFL2",
		LanIP: "10.0.0.6",
		P2PID: "UID12345678901234568",
		ENR:   "enr456",
		MAC:   "AABBCCDDEE00",
		DTLS:  true,
	}, "hd", true, false)
	srv.camMgr.InjectCamera("garage", cam)

	req := httptest.NewRequest("GET", "/api/streams/garage-sub.m3u8", nil)
	req.Host = "192.168.1.50:5080"
	w := httptest.NewRecorder()
	srv.handleStreamM3U8(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "rtsp://192.168.1.50:8554/garage-sub") {
		t.Errorf("substream playlist = %q", w.Body.String())
	}
}

func TestHandleStreamM3U8_SubstreamDisabledIsNotExposed(t *testing.T) {
	srv, _ := testServer(t)

	cam := camera.NewCamera(wyzeapi.CameraInfo{
		Name:  "garage",
		Model: "HL_CFL2",
		LanIP: "10.0.0.6",
	}, "hd", true, false)
	srv.camMgr.InjectCamera("garage", cam)

	req := httptest.NewRequest("GET", "/api/streams/garage-sub.m3u8", nil)
	w := httptest.NewRecorder()
	srv.handleStreamM3U8(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestHandleSnapshot_Missing(t *testing.T) {
	srv, _ := testServer(t)

	req := httptest.NewRequest("GET", "/api/snapshot/", nil)
	w := httptest.NewRecorder()
	srv.handleSnapshot(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("missing cam should be 400, got %d", w.Code)
	}
}

// TestHandleAPICameraAction_Snapshot verifies the webui button → HTTP →
// snapshot-manager wiring. The action handler fires the registered
// OnSnapshotRequest callback in a goroutine; we sync via a channel so
// the test isn't flaky.
func TestHandleAPICameraAction_Snapshot(t *testing.T) {
	srv, _ := testServer(t)

	cam := camera.NewCamera(wyzeapi.CameraInfo{Name: "front_door"}, "hd", true, false)
	srv.camMgr.InjectCamera("front_door", cam)

	fired := make(chan string, 1)
	srv.OnSnapshotRequest(func(_ context.Context, name string) {
		fired <- name
	})

	req := httptest.NewRequest("POST", "/api/cameras/front_door/snapshot", nil)
	w := httptest.NewRecorder()
	srv.handleAPICameraAction(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	select {
	case got := <-fired:
		if got != "front_door" {
			t.Errorf("callback got %q, want front_door", got)
		}
	case <-time.After(time.Second):
		t.Fatal("snapshot callback never fired")
	}
}

// TestHandleAPICameraAction_SnapshotNoHandler returns 503 when main.go
// hasn't wired the callback — protects against silent no-ops.
func TestHandleAPICameraAction_SnapshotNoHandler(t *testing.T) {
	srv, _ := testServer(t)

	cam := camera.NewCamera(wyzeapi.CameraInfo{Name: "front_door"}, "hd", true, false)
	srv.camMgr.InjectCamera("front_door", cam)

	req := httptest.NewRequest("POST", "/api/cameras/front_door/snapshot", nil)
	w := httptest.NewRecorder()
	srv.handleAPICameraAction(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestHandleIndex_Root(t *testing.T) {
	srv, _ := testServer(t)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	srv.handleIndex(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("content-type = %q", ct)
	}
	if !strings.Contains(w.Body.String(), "Wyze Bridge") {
		t.Error("page should contain 'Wyze Bridge'")
	}
}

func TestHandleIndex_NotRoot(t *testing.T) {
	srv, _ := testServer(t)

	req := httptest.NewRequest("GET", "/nonexistent", nil)
	w := httptest.NewRecorder()
	srv.handleIndex(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("non-root should be 404, got %d", w.Code)
	}
}

func TestHandleCameraPage_Missing(t *testing.T) {
	srv, _ := testServer(t)

	req := httptest.NewRequest("GET", "/camera/nonexistent", nil)
	w := httptest.NewRecorder()
	srv.handleCameraPage(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("missing cam should be 404, got %d", w.Code)
	}
}

func TestHandleCameraPage_Empty(t *testing.T) {
	srv, _ := testServer(t)

	req := httptest.NewRequest("GET", "/camera/", nil)
	w := httptest.NewRecorder()
	srv.handleCameraPage(w, req)

	if w.Code != http.StatusFound {
		t.Errorf("empty cam name should redirect, got %d", w.Code)
	}
}

func TestHandleAPICameraAction_NotFound(t *testing.T) {
	srv, _ := testServer(t)

	req := httptest.NewRequest("GET", "/api/cameras/nonexistent", nil)
	w := httptest.NewRecorder()
	srv.handleAPICameraAction(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("missing cam should be 404, got %d", w.Code)
	}
}

func TestHandleAPICameraAction_NoName(t *testing.T) {
	srv, _ := testServer(t)

	req := httptest.NewRequest("GET", "/api/cameras/", nil)
	w := httptest.NewRecorder()
	srv.handleAPICameraAction(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("no name should be 400, got %d", w.Code)
	}
}

func TestWriteJSON(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, map[string]string{"hello": "world"})

	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["hello"] != "world" {
		t.Errorf("response = %v", resp)
	}
}
