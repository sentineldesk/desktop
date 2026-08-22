// SentinelDesk
// A collaborative operating system for people and AI agents.
//
// Copyright 2026 Federico Pereira <fpereira@cnsoluciones.com>
//
// Licensed under the Apache License, Version 2.0.
//
// This product's name and logo are trademarks of Federico Pereira and are not
// covered by the license above. See the README for the trademark policy.
//
// SPDX-License-Identifier: Apache-2.0

package stream

// Per-session Pion API with congestion control.
//
// Each PeerConnection gets its own API instance so that the GCC interceptor
// hands back an estimator that is unambiguously tied to that connection — with
// a shared API there is no way to tell whose estimate you are reading.

import (
	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/cc"
	"github.com/pion/interceptor/pkg/gcc"
	"github.com/pion/webrtc/v4"

	"github.com/sentineldesk/desktop/pkg/config"
)

// newPeerAPI builds a Pion API for one session. The returned channel delivers
// that connection's bandwidth estimator once the interceptor creates it.
func newPeerAPI(cfg config.Config) (*webrtc.API, chan cc.BandwidthEstimator, error) {
	media := &webrtc.MediaEngine{}
	if err := media.RegisterDefaultCodecs(); err != nil {
		return nil, nil, err
	}

	registry := &interceptor.Registry{}
	estimatorC := make(chan cc.BandwidthEstimator, 1)
	congestion, err := cc.NewInterceptor(func() (cc.BandwidthEstimator, error) {
		return gcc.NewSendSideBWE(
			gcc.SendSideBWEInitialBitrate(cfg.VideoKbps*1000),
			gcc.SendSideBWEMinBitrate(300_000),
			gcc.SendSideBWEMaxBitrate(cfg.VideoKbps*1000),
		)
	})
	if err != nil {
		return nil, nil, err
	}
	congestion.OnNewPeerConnection(func(_ string, estimator cc.BandwidthEstimator) {
		// Non-blocking: the buffered channel holds the one estimate we need,
		// and a session that never reads it must not wedge the interceptor.
		select {
		case estimatorC <- estimator:
		default:
		}
	})
	registry.Add(congestion)

	// TWCC feedback is what GCC estimates from, so it has to be registered
	// before the default interceptors.
	if err := webrtc.ConfigureTWCCHeaderExtensionSender(media, registry); err != nil {
		return nil, nil, err
	}
	if err := webrtc.RegisterDefaultInterceptors(media, registry); err != nil {
		return nil, nil, err
	}

	settings := webrtc.SettingEngine{}
	if cfg.MinPort > 0 && cfg.MaxPort > 0 {
		if err := settings.SetEphemeralUDPPortRange(cfg.MinPort, cfg.MaxPort); err != nil {
			return nil, nil, err
		}
	}
	// Behind 1:1 NAT the host candidate carries the private address, which the
	// far side cannot reach; this rewrites it to the public one.
	if cfg.NAT1To1IP != "" {
		settings.SetNAT1To1IPs([]string{cfg.NAT1To1IP}, webrtc.ICECandidateTypeHost)
	}

	api := webrtc.NewAPI(
		webrtc.WithMediaEngine(media),
		webrtc.WithInterceptorRegistry(registry),
		webrtc.WithSettingEngine(settings),
	)
	return api, estimatorC, nil
}
