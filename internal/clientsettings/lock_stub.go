//go:build !linux

package clientsettings

func AcquireClientLock() (func(), error) { return func() {}, nil }

func AcquireSettingsDocumentLock() (func(), error) { return func() {}, nil }
