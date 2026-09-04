// Copyright 2026 The Tipsy Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package graphics

import (
	"errors"
	"fmt"
)

// Renderer is a user-facing rendering backend choice.
type Renderer string

const (
	RendererAuto   Renderer = "auto"
	RendererOpenGL Renderer = "opengl"
	RendererVulkan Renderer = "vulkan"
)

// ErrRendererUnavailable identifies a valid renderer that this Tipsy build
// cannot coherently provide to the Android client.
var ErrRendererUnavailable = errors.New("graphics: renderer unavailable")

// UnsupportedRendererError is returned before client startup when a valid
// renderer cannot be backed by the complete host/platform compatibility path.
// In particular, finding libvulkan alone is not sufficient Vulkan support.
type UnsupportedRendererError struct {
	Renderer Renderer
	Reason   string
}

func (e *UnsupportedRendererError) Error() string {
	if e == nil {
		return ErrRendererUnavailable.Error()
	}
	if e.Reason == "" {
		return fmt.Sprintf("graphics: %s renderer unavailable", e.Renderer)
	}
	return fmt.Sprintf("graphics: %s renderer unavailable: %s", e.Renderer, e.Reason)
}

func (e *UnsupportedRendererError) Unwrap() error { return ErrRendererUnavailable }

// VulkanHostProbe describes only the host Vulkan loader/device probe. It does
// not imply that the Android client's Vulkan surface contract is implemented.
type VulkanHostProbe struct {
	Library         bool
	Loader          bool
	APIVersion      uint32
	PhysicalDevices uint32
	Result          int32
	Detail          string
}

// RendererCapability describes whether a renderer is safe to select before
// starting the client. Available means the complete client-to-window path is
// constructed, not merely that a host shared library exists.
type RendererCapability struct {
	Renderer  Renderer
	Available bool
	Reason    string
	Host      VulkanHostProbe
}

// RendererCapabilities is the pre-client rendering capability snapshot.
type RendererCapabilities struct {
	OpenGL RendererCapability
	Vulkan RendererCapability
}

// ProbeRendererCapabilities probes host Vulkan without creating a surface and
// combines that result with the platform bridges implemented by this build.
func ProbeRendererCapabilities() RendererCapabilities {
	return buildRendererCapabilities(platformOpenGLConstructed(), platformProbeHostVulkan())
}

func buildRendererCapabilities(openGLConstructed bool, host VulkanHostProbe) RendererCapabilities {
	gl := RendererCapability{Renderer: RendererOpenGL, Available: openGLConstructed}
	if openGLConstructed {
		gl.Reason = "X11 EGL/OpenGL ES compatibility path is constructed"
	} else {
		gl.Reason = "this build has no native X11 EGL/OpenGL ES compatibility path"
	}

	vk := RendererCapability{Renderer: RendererVulkan, Host: host}
	switch {
	case !host.Library:
		vk.Reason = "Tipsy's Android Vulkan compatibility path is not implemented; host libvulkan was not found"
	case !host.Loader:
		vk.Reason = "Tipsy's Android Vulkan compatibility path is not implemented; the host Vulkan loader is unusable"
	case host.PhysicalDevices == 0:
		vk.Reason = "Tipsy's Android Vulkan compatibility path is not implemented; the host Vulkan loader found no usable physical device"
	default:
		vk.Reason = "Tipsy's Android Vulkan compatibility path is not implemented (libvulkan registration and VK_KHR_android_surface-to-X11 translation are missing)"
	}
	return RendererCapabilities{OpenGL: gl, Vulkan: vk}
}

// Capability returns the capability for renderer. Auto resolves to the only
// currently constructed client platform, OpenGL ES, without requiring callers
// to emit an OpenGL preference flag.
func (c RendererCapabilities) Capability(renderer Renderer) (RendererCapability, error) {
	switch renderer {
	case RendererAuto, RendererOpenGL:
		return c.OpenGL, nil
	case RendererVulkan:
		return c.Vulkan, nil
	default:
		return RendererCapability{}, fmt.Errorf("graphics: unknown renderer %q", renderer)
	}
}

// Resolve validates renderer against this snapshot and returns the coherent
// platform backend. Auto currently resolves to OpenGL ES while remaining an
// upstream-auto policy choice for client-settings flags.
func (c RendererCapabilities) Resolve(renderer Renderer) (Renderer, error) {
	capability, err := c.Capability(renderer)
	if err != nil {
		return "", err
	}
	if !capability.Available {
		return "", &UnsupportedRendererError{Renderer: renderer, Reason: capability.Reason}
	}
	return capability.Renderer, nil
}

// RequireRenderer performs the current host probe and rejects unsupported
// explicit selections before any client process is started.
func RequireRenderer(renderer Renderer) error {
	_, err := ProbeRendererCapabilities().Resolve(renderer)
	return err
}
