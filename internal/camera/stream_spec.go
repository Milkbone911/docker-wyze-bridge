package camera

import (
	"context"

	"github.com/IDisposable/docker-wyze-bridge/internal/go2rtcmgr"
)

const substreamSuffix = "-sub"

type streamRole string

const (
	streamRoleMain streamRole = "main"
	streamRoleSub  streamRole = "sub"
)

// streamSpec describes one media producer for a physical camera.
// Camera remains the device/state/control object; streamSpec is only
// the desired go2rtc registration.
type streamSpec struct {
	Name     string
	URL      string
	Protocol string
	Quality  string
	Role     streamRole
}

// streamSpecsFor returns the desired go2rtc streams for a camera.
// The main stream always exists. A secondary stream is opt-in and,
// for now, only available on the TUTK path where go2rtc supports an
// independent wyze:// producer with subtype=sd/hd.
func (m *Manager) streamSpecsFor(cam *Camera) []streamSpec {
	mainURL, protocol := m.streamSourceFor(cam)
	specs := []streamSpec{{
		Name:     cam.Name(),
		URL:      mainURL,
		Protocol: protocol,
		Quality:  cam.GetQuality(),
		Role:     streamRoleMain,
	}}

	if protocol != "tutk" || !m.cfg.CamSubstream(cam.Name()) {
		return specs
	}

	quality := m.cfg.CamSubQuality(cam.Name())
	specs = append(specs, streamSpec{
		Name:     cam.Name() + substreamSuffix,
		URL:      cam.GetInfo().StreamURL(quality),
		Protocol: protocol,
		Quality:  quality,
		Role:     streamRoleSub,
	})
	return specs
}

// syncSecondaryStreams reconciles optional secondary producers without
// changing the physical camera state. A failed substream must never
// tear down a healthy main stream, increment the camera error counter,
// or trigger protocol fallback.
func (m *Manager) syncSecondaryStreams(ctx context.Context, cam *Camera) {
	go2rtc := m.go2rtcClient()
	if go2rtc == nil {
		return
	}

	specs := m.streamSpecsFor(cam)
	subName := cam.Name() + substreamSuffix

	if len(specs) == 1 {
		// Clean up a stale secondary producer after config changes or
		// a runtime TUTK -> WebRTC promotion.
		_ = go2rtc.DeleteStream(ctx, subName)
		return
	}

	for _, spec := range specs[1:] {
		_ = go2rtc.DeleteStream(ctx, spec.Name)
		if err := go2rtc.AddStream(ctx, spec.Name, spec.URL); err != nil {
			m.log.Warn().Err(err).
				Str("cam", cam.Name()).
				Str("stream", spec.Name).
				Str("quality", spec.Quality).
				Msg("failed to register secondary stream; main stream remains active")
			continue
		}
		m.log.Info().
			Str("cam", cam.Name()).
			Str("stream", spec.Name).
			Str("quality", spec.Quality).
			Msg("secondary stream registered")
	}
}

// healthCheckSecondaryStreams repairs missing/dead secondary producers
// independently from the main camera state machine.
func (m *Manager) healthCheckSecondaryStreams(
	ctx context.Context,
	cam *Camera,
	streams map[string]*go2rtcmgr.StreamInfo,
) {
	specs := m.streamSpecsFor(cam)
	subName := cam.Name() + substreamSuffix

	if len(specs) == 1 {
		if _, stale := streams[subName]; stale {
			_ = m.go2rtcClient().DeleteStream(ctx, subName)
		}
		return
	}

	for _, spec := range specs[1:] {
		info, ok := streams[spec.Name]
		if ok && len(info.Producers) > 0 {
			continue
		}
		m.log.Warn().
			Str("cam", cam.Name()).
			Str("stream", spec.Name).
			Msg("secondary stream lost, re-registering without changing camera state")
		m.syncSecondaryStreams(ctx, cam)
		return
	}
}

// deleteCameraStreams removes every stream name owned by a physical
// camera. The substream name is deleted unconditionally so disabling
// SUBSTREAM or switching protocols cannot leave an orphan behind.
func (m *Manager) deleteCameraStreams(ctx context.Context, cam *Camera) {
	go2rtc := m.go2rtcClient()
	if go2rtc == nil {
		return
	}
	_ = go2rtc.DeleteStream(ctx, cam.Name())
	_ = go2rtc.DeleteStream(ctx, cam.Name()+substreamSuffix)
}
