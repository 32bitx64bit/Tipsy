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

// VulkanHostProbe describes the host Vulkan loader/device/WSI probe. It does
// not by itself mean the Android client's Vulkan surface contract is usable;
// combine it with the adapter constructed by this build.
type VulkanHostProbe struct {
	Library         bool
	Loader          bool
	APIVersion      uint32
	PhysicalDevices uint32
	XcbSurface      bool
	XlibSurface     bool
	Result          int32
	Detail          string
}

// HasWSI reports whether the host loader advertised a desktop surface
// extension Tipsy can use to back VK_KHR_android_surface.
func (h VulkanHostProbe) HasWSI() bool {
	return h.XcbSurface || h.XlibSurface
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
	return buildRendererCapabilities(platformOpenGLConstructed(), platformVulkanConstructed(), platformProbeHostVulkan())
}

func buildRendererCapabilities(openGLConstructed, vulkanBridge bool, host VulkanHostProbe) RendererCapabilities {
	gl := RendererCapability{Renderer: RendererOpenGL, Available: openGLConstructed}
	if openGLConstructed {
		gl.Reason = "X11 EGL/OpenGL ES compatibility path is constructed"
	} else {
		gl.Reason = "this build has no native X11 EGL/OpenGL ES compatibility path"
	}

	vk := RendererCapability{Renderer: RendererVulkan, Host: host}
	switch {
	case !vulkanBridge:
		vk.Reason = "this build has no Android Vulkan loader adapter"
	case !host.Library:
		vk.Reason = "host libvulkan was not found"
	case !host.Loader:
		vk.Reason = "the host Vulkan loader is unusable"
	case host.PhysicalDevices == 0:
		vk.Reason = "the host Vulkan loader found no usable physical device"
	case !host.HasWSI():
		vk.Reason = "the host Vulkan loader has no VK_KHR_xcb_surface or VK_KHR_xlib_surface to back VK_KHR_android_surface"
	default:
		vk.Available = true
		vk.Reason = "Android Vulkan WSI adapter can translate VK_KHR_android_surface to the host X11 window"
	}
	return RendererCapabilities{OpenGL: gl, Vulkan: vk}
}

// Capability returns the capability for renderer. Auto prefers Vulkan when
// that complete client platform is available, otherwise OpenGL ES.
func (c RendererCapabilities) Capability(renderer Renderer) (RendererCapability, error) {
	switch renderer {
	case RendererAuto:
		if c.Vulkan.Available {
			return c.Vulkan, nil
		}
		return c.OpenGL, nil
	case RendererOpenGL:
		return c.OpenGL, nil
	case RendererVulkan:
		return c.Vulkan, nil
	default:
		return RendererCapability{}, fmt.Errorf("graphics: unknown renderer %q", renderer)
	}
}

// Resolve validates renderer against this snapshot and returns the coherent
// platform backend. Auto selects Vulkan when it is available, otherwise OpenGL ES.
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

// WindowRefreshRates reports XRandR refresh rates for the CRTC containing xid.
// Zero current / empty supported means the X server did not expose a usable mode.
func WindowRefreshRates(xdisplay, xid uintptr) (current float64, supported []float32) {
	return platformDisplayRefreshRates(xdisplay, xid)
}
