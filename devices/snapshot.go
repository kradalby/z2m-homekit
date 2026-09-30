package devices

import (
	"cmp"
	"slices"
)

// Snapshot is an immutable view of every device. The manager replaces it
// wholesale on each change, so a reader holding one never sees a torn or
// half-merged state. Fields stay unexported and reads return copies, since
// every reader shares the published pointer. The zero Snapshot is empty.
type Snapshot struct {
	version uint64
	devices map[string]DeviceState // by device ID
	changed chan struct{}
}

// DeviceState pairs a device's config with its state.
type DeviceState struct {
	Device  Device
	State   State
	Version uint64 // the Snapshot.Version that last changed this device
}

func newSnapshot(version uint64, devices map[string]DeviceState) *Snapshot {
	return &Snapshot{version: version, devices: devices, changed: make(chan struct{})}
}

// Version increases with every published snapshot.
func (s *Snapshot) Version() uint64 {
	return s.version
}

// Get returns the device with the given ID.
func (s *Snapshot) Get(id string) (DeviceState, bool) {
	ds, ok := s.devices[id]
	return ds, ok
}

// Len returns the number of devices.
func (s *Snapshot) Len() int {
	return len(s.devices)
}

// Changed is closed once a newer snapshot has been published. Waiting on the
// snapshot just read, rather than on a shared channel, cannot miss a change.
func (s *Snapshot) Changed() <-chan struct{} {
	return s.changed
}

// All returns every device, ordered by ID.
func (s *Snapshot) All() []DeviceState {
	return s.Since(0)
}

// Since returns the devices changed after version v, ordered by ID.
func (s *Snapshot) Since(v uint64) []DeviceState {
	var out []DeviceState
	for _, ds := range s.devices {
		if ds.Version > v {
			out = append(out, ds)
		}
	}
	slices.SortFunc(out, func(a, b DeviceState) int { return cmp.Compare(a.Device.ID, b.Device.ID) })

	return out
}
