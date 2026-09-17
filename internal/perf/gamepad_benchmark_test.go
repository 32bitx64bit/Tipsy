package perf

import (
	"testing"

	"github.com/tipsy-linux/tipsy/internal/gamepad"
)

// BenchmarkGamepadTranslation is a deterministic public xpad-shaped fixture.
// It deliberately measures the current reader snapshot plus Android-frame
// translation allocation shape, not evdev readiness, device discovery, or
// gameplay input latency.
func BenchmarkGamepadTranslation(b *testing.B) {
	abs := map[uint16]gamepad.AbsInfo{
		gamepad.AbsX:     {Minimum: -32768, Maximum: 32767, Flat: 512},
		gamepad.AbsY:     {Minimum: -32768, Maximum: 32767, Flat: 512},
		gamepad.AbsZ:     {Minimum: 0, Maximum: 255, Flat: 8},
		gamepad.AbsRX:    {Minimum: -32768, Maximum: 32767, Flat: 512},
		gamepad.AbsRY:    {Minimum: -32768, Maximum: 32767, Flat: 512},
		gamepad.AbsRZ:    {Minimum: 0, Maximum: 255, Flat: 8},
		gamepad.AbsHat0X: {Minimum: -1, Maximum: 1},
		gamepad.AbsHat0Y: {Minimum: -1, Maximum: 1},
	}
	mapping := gamepad.Mapping{
		Name:        "xpad-fixture",
		RightX:      gamepad.AbsRX,
		RightY:      gamepad.AbsRY,
		TriggerL:    gamepad.AbsZ,
		TriggerR:    gamepad.AbsRZ,
		DpadButtons: true,
		DpadHat:     true,
	}
	events := []gamepad.InputEvent{
		{Type: gamepad.EvKey, Code: gamepad.BtnSouth, Value: 1},
		{Type: gamepad.EvKey, Code: gamepad.BtnTL2, Value: 1},
		{Type: gamepad.EvKey, Code: gamepad.BtnDpadRight, Value: 1},
		{Type: gamepad.EvAbs, Code: gamepad.AbsX, Value: 4096},
		{Type: gamepad.EvAbs, Code: gamepad.AbsY, Value: -4096},
		{Type: gamepad.EvAbs, Code: gamepad.AbsRX, Value: 8192},
		{Type: gamepad.EvAbs, Code: gamepad.AbsRY, Value: -8192},
		{Type: gamepad.EvAbs, Code: gamepad.AbsZ, Value: 192},
		{Type: gamepad.EvAbs, Code: gamepad.AbsRZ, Value: 160},
		{Type: gamepad.EvAbs, Code: gamepad.AbsHat0X, Value: 1},
		{Type: gamepad.EvAbs, Code: gamepad.AbsHat0Y, Value: -1},
		{Type: gamepad.EvSyn, Code: gamepad.SynReport},
	}
	reader := gamepad.NewReader(abs)
	reader.SetMapping(mapping)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		var frame *gamepad.Frame
		for _, event := range events {
			if got := reader.Feed(event); got != nil {
				frame = got
			}
		}
		if frame == nil {
			b.Fatal("fixture did not reach SYN_REPORT")
		}
		translated := gamepad.MapFrame(frame, 7, mapping, abs)
		if len(translated.Buttons) < 3 || len(translated.Axes) < 8 || len(translated.Ranges) < 8 {
			b.Fatalf("incomplete translation: %#v", translated)
		}
	}
}

// BenchmarkGamepadMapFrameInto measures the JNI pump reuse path: one
// prebuilt SYN_REPORT Frame translated into a retained AndroidFrame.
// Reader.Feed is outside the loop so allocs/op is the translation itself.
func BenchmarkGamepadMapFrameInto(b *testing.B) {
	abs := map[uint16]gamepad.AbsInfo{
		gamepad.AbsX:     {Minimum: -32768, Maximum: 32767, Flat: 512},
		gamepad.AbsY:     {Minimum: -32768, Maximum: 32767, Flat: 512},
		gamepad.AbsZ:     {Minimum: 0, Maximum: 255, Flat: 8},
		gamepad.AbsRX:    {Minimum: -32768, Maximum: 32767, Flat: 512},
		gamepad.AbsRY:    {Minimum: -32768, Maximum: 32767, Flat: 512},
		gamepad.AbsRZ:    {Minimum: 0, Maximum: 255, Flat: 8},
		gamepad.AbsHat0X: {Minimum: -1, Maximum: 1},
		gamepad.AbsHat0Y: {Minimum: -1, Maximum: 1},
	}
	mapping := gamepad.Mapping{
		Name:        "xpad-fixture",
		RightX:      gamepad.AbsRX,
		RightY:      gamepad.AbsRY,
		TriggerL:    gamepad.AbsZ,
		TriggerR:    gamepad.AbsRZ,
		DpadButtons: true,
		DpadHat:     true,
	}
	events := []gamepad.InputEvent{
		{Type: gamepad.EvKey, Code: gamepad.BtnSouth, Value: 1},
		{Type: gamepad.EvKey, Code: gamepad.BtnTL2, Value: 1},
		{Type: gamepad.EvKey, Code: gamepad.BtnDpadRight, Value: 1},
		{Type: gamepad.EvAbs, Code: gamepad.AbsX, Value: 4096},
		{Type: gamepad.EvAbs, Code: gamepad.AbsY, Value: -4096},
		{Type: gamepad.EvAbs, Code: gamepad.AbsRX, Value: 8192},
		{Type: gamepad.EvAbs, Code: gamepad.AbsRY, Value: -8192},
		{Type: gamepad.EvAbs, Code: gamepad.AbsZ, Value: 192},
		{Type: gamepad.EvAbs, Code: gamepad.AbsRZ, Value: 160},
		{Type: gamepad.EvAbs, Code: gamepad.AbsHat0X, Value: 1},
		{Type: gamepad.EvAbs, Code: gamepad.AbsHat0Y, Value: -1},
		{Type: gamepad.EvSyn, Code: gamepad.SynReport},
	}
	reader := gamepad.NewReader(abs)
	reader.SetMapping(mapping)
	var frame *gamepad.Frame
	for _, event := range events {
		if got := reader.Feed(event); got != nil {
			frame = got
		}
	}
	if frame == nil {
		b.Fatal("fixture did not reach SYN_REPORT")
	}
	var translated gamepad.AndroidFrame
	gamepad.MapFrameInto(&translated, frame, 7, mapping, abs)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		got := gamepad.MapFrameInto(&translated, frame, 7, mapping, abs)
		if len(got.Buttons) < 3 || len(got.Axes) < 8 || len(got.Ranges) < 8 {
			b.Fatalf("incomplete translation: %#v", got)
		}
	}
}
