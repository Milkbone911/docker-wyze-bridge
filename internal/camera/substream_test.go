package camera

import (
	"context"
	"strings"
	"testing"

	"github.com/IDisposable/docker-wyze-bridge/internal/wyzeapi"
)

func substreamTestCamera(name, model string) *Camera {
	return NewCamera(wyzeapi.CameraInfo{
		Name:  name,
		LanIP: "10.0.0.5",
		P2PID: "UID12345678901234567",
		ENR:   "enr123",
		MAC:   "AABBCCDDEEFF",
		Model: model,
		DTLS:  true,
	}, "hd", true, false)
}

func TestStreamSpecsFor_TUTKSubstream(t *testing.T) {
	mgr, _ := newTestManager(t)
	mgr.cfg.Substream = true
	mgr.cfg.SubQuality = "sd"

	specs := mgr.streamSpecsFor(substreamTestCamera("living_room", "HL_PAN3"))

	if len(specs) != 2 {
		t.Fatalf("stream specs = %d, want 2", len(specs))
	}
	if specs[0].Name != "living_room" || specs[0].Role != streamRoleMain {
		t.Errorf("main spec = %+v", specs[0])
	}
	if specs[1].Name != "living_room-sub" || specs[1].Role != streamRoleSub {
		t.Errorf("sub spec = %+v", specs[1])
	}
	if !strings.Contains(specs[0].URL, "subtype=hd") {
		t.Errorf("main URL = %q, want subtype=hd", specs[0].URL)
	}
	if !strings.Contains(specs[1].URL, "subtype=sd") {
		t.Errorf("sub URL = %q, want subtype=sd", specs[1].URL)
	}
}

func TestStreamSpecsFor_SubstreamDisabled(t *testing.T) {
	mgr, _ := newTestManager(t)
	mgr.cfg.Substream = false

	specs := mgr.streamSpecsFor(substreamTestCamera("garage", "HL_CFL2"))
	if len(specs) != 1 {
		t.Fatalf("stream specs = %d, want main only", len(specs))
	}
}

func TestStreamSpecsFor_SubstreamExcludedFromNonTUTK(t *testing.T) {
	mgr, _ := newTestManager(t)
	mgr.cfg.Substream = true
	mgr.cfg.GwellEnabled = true

	t.Run("webrtc", func(t *testing.T) {
		cam := NewCamera(wyzeapi.CameraInfo{
			Name:  "duo",
			Model: "GW_DBD",
			MAC:   "AABBCCDDEE01",
		}, "hd", true, false)
		specs := mgr.streamSpecsFor(cam)
		if len(specs) != 1 || specs[0].Protocol != "webrtc" {
			t.Fatalf("WebRTC specs = %+v, want one main stream", specs)
		}
	})

	t.Run("gwell", func(t *testing.T) {
		cam := substreamTestCamera("window", "GW_WC")
		specs := mgr.streamSpecsFor(cam)
		if len(specs) != 1 || specs[0].Protocol != "gwell" {
			t.Fatalf("Gwell specs = %+v, want one main stream", specs)
		}
	})
}

func TestManager_ConnectCamera_RegistersSubstream(t *testing.T) {
	mgr, go2rtcAPI := newTestManager(t)
	mgr.cfg.Substream = true
	mgr.cfg.SubQuality = "sd"

	cam := substreamTestCamera("front_drive", "HL_CFL2")
	mgr.cameras[cam.Name()] = cam

	mgr.connectCamera(context.Background(), cam)

	if cam.GetState() != StateStreaming {
		t.Fatalf("state = %v, want Streaming", cam.GetState())
	}
	for _, name := range []string{"front_drive", "front_drive-sub"} {
		active, err := go2rtcAPI.HasActiveProducer(context.Background(), name)
		if err != nil {
			t.Fatal(err)
		}
		if !active {
			t.Errorf("%s should have an active producer", name)
		}
	}
}

func TestManager_HealthCheck_RepairsSubstreamWithoutCameraError(t *testing.T) {
	mgr, go2rtcAPI := newTestManager(t)
	mgr.cfg.Substream = true
	mgr.cfg.SubQuality = "sd"

	cam := substreamTestCamera("kitchen", "HL_PAN3")
	mgr.cameras[cam.Name()] = cam
	mgr.connectCamera(context.Background(), cam)

	if err := go2rtcAPI.DeleteStream(context.Background(), "kitchen-sub"); err != nil {
		t.Fatal(err)
	}

	mgr.HealthCheck(context.Background())

	if cam.GetState() != StateStreaming {
		t.Errorf("state = %v, want Streaming", cam.GetState())
	}
	if cam.GetErrorCount() != 0 {
		t.Errorf("error count = %d, want 0", cam.GetErrorCount())
	}
	active, err := go2rtcAPI.HasActiveProducer(context.Background(), "kitchen-sub")
	if err != nil {
		t.Fatal(err)
	}
	if !active {
		t.Error("HealthCheck should re-register the missing substream")
	}
}

func TestManager_StopStream_RemovesMainAndSub(t *testing.T) {
	mgr, go2rtcAPI := newTestManager(t)
	mgr.cfg.Substream = true
	mgr.cfg.SubQuality = "sd"

	cam := substreamTestCamera("office", "HL_PAN3")
	mgr.cameras[cam.Name()] = cam
	mgr.connectCamera(context.Background(), cam)
	mgr.StopStream(context.Background(), cam.Name())

	streams, err := go2rtcAPI.ListStreams(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := streams["office"]; ok {
		t.Error("main stream should be deleted")
	}
	if _, ok := streams["office-sub"]; ok {
		t.Error("substream should be deleted")
	}
	if cam.GetState() != StateOffline {
		t.Errorf("state = %v, want Offline", cam.GetState())
	}
}


func TestManager_SubstreamDisabledDoesNotOwnSuffix(t *testing.T) {
	mgr, api := newTestManager(t)
	mgr.cfg.Substream = false
	ctx := context.Background()

	if err := api.AddStream(ctx, "garage-sub", "custom-source"); err != nil {
		t.Fatal(err)
	}

	cam := substreamTestCamera("garage", "HL_CFL2")
	mgr.cameras[cam.Name()] = cam
	mgr.connectCamera(ctx, cam)
	mgr.StopStream(ctx, cam.Name())

	streams, err := api.ListStreams(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := streams["garage-sub"]; !ok {
		t.Error("garage-sub should be preserved when SUBSTREAM is disabled")
	}
}


func TestManager_SetQualityKeepsIndependentSubQuality(t *testing.T) {
	mgr, api := newTestManager(t)
	mgr.cfg.Substream = true
	mgr.cfg.SubQuality = "hd"
	ctx := context.Background()

	cam := substreamTestCamera("den", "HL_PAN3")
	mgr.cameras[cam.Name()] = cam
	mgr.connectCamera(ctx, cam)

	if err := mgr.SetQuality(ctx, cam.Name(), "sd"); err != nil {
		t.Fatal(err)
	}

	streams, err := api.ListStreams(ctx)
	if err != nil {
		t.Fatal(err)
	}
	main := streams["den"]
	sub := streams["den-sub"]
	if main == nil || len(main.Producers) == 0 || !strings.Contains(main.Producers[0].URL, "subtype=sd") {
		t.Fatalf("main producer = %+v, want subtype=sd", main)
	}
	if sub == nil || len(sub.Producers) == 0 || !strings.Contains(sub.Producers[0].URL, "subtype=hd") {
		t.Fatalf("sub producer = %+v, want subtype=hd", sub)
	}
}
