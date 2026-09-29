package camera

import (
	"context"

	"github.com/IDisposable/docker-wyze-bridge/internal/go2rtcmgr"
)

const substreamSuffix = "-sub"

// streamSpec describes one media producer for a physical camera.
// Camera remains the device/state/control object; streamSpec is only
// the desired go2rtc registration.
type streamSpec struct {
	Name     string
	URL      string
	Protocol string
	Quality  string
}

// ownsSecondaryStream reports whether the bridge owns the historical
// <camera>-sub name for this physical camera.
func (m *Manager) ownsSecondaryStream(cam *Camera) bool {
	if !m.cfg.CamSubstream(cam.Name()) {
		return false
	}
	_, protocol := m.streamSourceFor(cam)
	// A TUTK camera owns the secondary producer it can create. A
	// force-WebRTC camera may still own a stale secondary producer
	// created immediately before runtime fallback. Native WebRTC and
	// Gwell cameras never claim the suffix merely because SUBSTREAM is
	// enabled globally.
	return protocol == "tutk" || cam.ForceWebRTC()
}

// streamSpecsFor returns the desired go2rtc streams for a camera.
// The main stream always exists. A secondary stream is opt-in and,
// for now, only available on the TUTK path where go2rtc supports an
// independent wyze:// producer with subtype=sd/hd.
// StreamNames returns the media stream names owned by a physical camera.
// Device state remains represented by Camera; callers that expose media
// endpoints can use this to include optional variants such as <camera>-sub.
func (m *Manager) StreamNames(cam *Camera) []string {
	specs := m.streamSpecsFor(cam)
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Name)
	}
	return names
}

func (m *Manager) streamSpecsFor(cam *Camera) []streamSpec {
	mainURL, protocol := m.streamSourceFor(cam)
	specs := []streamSpec{{
		Name:     cam.Name(),
		URL:      mainURL,
		Protocol: protocol,
		Quality:  cam.GetQuality(),
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

	// When the feature is disabled this suffix is not ours. Leave any
	// user-defined <camera>-sub stream untouched so the default
	// configuration has no new side effects.
	if !m.cfg.CamSubstream(cam.Name()) {
		return
	}

	specs := m.streamSpecsFor(cam)
	subName := cam.Name() + substreamSuffix

	if len(specs) == 1 {
		// Native WebRTC/Gwell cameras never own the suffix. A camera
		// runtime-promoted from TUTK does, so remove that stale producer.
		if m.ownsSecondaryStream(cam) {
			_ = go2rtc.DeleteStream(ctx, subName)
		}
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
	if !m.cfg.CamSubstream(cam.Name()) {
		return
	}

	specs := m.streamSpecsFor(cam)
	subName := cam.Name() + substreamSuffix

	if len(specs) == 1 {
		if m.ownsSecondaryStream(cam) {
			if _, stale := streams[subName]; stale {
				if go2rtc := m.go2rtcClient(); go2rtc != nil {
					_ = go2rtc.DeleteStream(ctx, subName)
				}
			}
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

// deleteCameraStreams removes stream names owned by a physical camera.
// Native WebRTC/Gwell cameras never own the secondary suffix, and a
// disabled SUBSTREAM leaves any user-defined <camera>-sub stream alone.
func (m *Manager) deleteCameraStreams(ctx context.Context, cam *Camera) {
	go2rtc := m.go2rtcClient()
	if go2rtc == nil {
		return
	}
	_ = go2rtc.DeleteStream(ctx, cam.Name())
	if m.ownsSecondaryStream(cam) {
		_ = go2rtc.DeleteStream(ctx, cam.Name()+substreamSuffix)
	}
}
