//go:build !linux

package clientsettings

func AcquireClientLock() (func(), error) { return func() {}, nil }
