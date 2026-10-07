package environment

// fakeFS is an in-memory FileReader.
//
// It exists so the probe suite can be exercised against a synthetic host. That
// is not a testing convenience: without it every assertion about detection
// depends on whether the machine running the test happens to be a VM, which is
// exactly the non-determinism that makes a CI result untrustworthy. A test that
// passes on a laptop and fails on a cloud runner is not a test.

type fakeFS struct {
	files map[string]string
	dirs  map[string][]string
}

func newFakeFS() *fakeFS {
	return &fakeFS{
		files: make(map[string]string),
		dirs:  make(map[string][]string),
	}
}

// addFile registers a file with contents.
func (f *fakeFS) addFile(path, content string) *fakeFS {
	f.files[path] = content
	// Register the parent directory so Exists on a directory behaves.
	f.addDirFor(path)
	return f
}

// addDirFor synthesises the parent directory entry list from registered files.
func (f *fakeFS) addDirFor(path string) {
	for {
		idx := lastSlash(path)
		if idx <= 0 {
			return
		}
		parent, base := path[:idx], path[idx+1:]
		list := f.dirs[parent]
		found := false
		for _, e := range list {
			if e == base {
				found = true
				break
			}
		}
		if !found {
			f.dirs[parent] = append(list, base)
		}
		path = parent
	}
}

// addDir registers a directory with an explicit entry list.
func (f *fakeFS) addDir(path string, entries ...string) *fakeFS {
	f.dirs[path] = entries
	f.addDirFor(path)
	return f
}

func (f *fakeFS) ReadFile(path string) ([]byte, error) {
	c, ok := f.files[path]
	if !ok {
		return nil, errNotFound{path}
	}
	return []byte(c), nil
}

func (f *fakeFS) ReadDir(path string) ([]string, error) {
	e, ok := f.dirs[path]
	if !ok {
		return nil, errNotFound{path}
	}
	return e, nil
}

func (f *fakeFS) Exists(path string) bool {
	if _, ok := f.files[path]; ok {
		return true
	}
	_, ok := f.dirs[path]
	return ok
}

// errNotFound mirrors os.ErrNotExist closely enough for the probes, which only
// check that the error is non-nil.
type errNotFound struct{ path string }

func (e errNotFound) Error() string { return "fakefs: not found: " + e.path }

// lastSlash returns the index of the final '/', or -1.
func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}

// ── Synthetic host fixtures ─────────────────────────────────────────────────

// bareMetalHost is a host with no virtualisation signal whatsoever. Every
// detection test asserts against this, so a probe that fires on nothing is
// caught.
func bareMetalHost() *fakeFS {
	return newFakeFS().
		addFile("/proc/cpuinfo", "processor\t: 0\nflags\t\t: fpu vme de pse tsc\n").
		addFile("/proc/self/status", "Name:\tnode\nTracerPid:\t0\n").
		addDir("/sys/class/net", "lo", "eth0").
		addFile("/sys/class/net/eth0/address", "a4:bb:6d:11:22:33\n").
		addFile("/sys/class/net/lo/address", "00:00:00:00:00:00\n").
		addFile("/sys/firmware/dmi/id/sys_vendor", "LENOVO\n").
		addFile("/sys/firmware/dmi/id/product_name", "ThinkPad T14\n").
		addDir("/sys/bus/pci/devices", "0000:00:1f.6").
		addFile("/sys/bus/pci/devices/0000:00:1f.6/vendor", "0x8086\n").
		addFile("/sys/bus/pci/devices/0000:00:1f.6/device", "0x9dc3\n")
}

// vmwareHost is a VMware guest: DMI names the vendor, PCI exposes a virtio NIC,
// the kernel reports the hypervisor flag, and virtio drivers are bound.
func vmwareHost() *fakeFS {
	return newFakeFS().
		addFile("/proc/cpuinfo", "processor\t: 0\nflags\t\t: fpu vme de pse tsc hypervisor\n").
		addFile("/proc/self/status", "TracerPid:\t0\n").
		addDir("/sys/class/net", "ens33").
		addFile("/sys/class/net/ens33/address", "00:50:56:aa:bb:cc\n").
		addFile("/sys/class/dmi/id/sys_vendor", "VMware, Inc.\n").
		addFile("/sys/class/dmi/id/product_name", "VMware7,1\n").
		addDir("/sys/bus/pci/devices", "0000:00:03.0", "0000:00:04.0").
		addFile("/sys/bus/pci/devices/0000:00:03.0/vendor", "0x1af4\n").
		addFile("/sys/bus/pci/devices/0000:00:03.0/device", "0x1040\n").
		addFile("/sys/bus/pci/devices/0000:00:04.0/vendor", "0x15ad\n").
		addFile("/sys/bus/pci/devices/0000:00:04.0/device", "0x07b0\n").
		addDir("/sys/bus/virtio/devices", "vnet0")
}

// qemuVirtioHost is a QEMU guest whose only real evidence is virtio: no vendor
// string anywhere in DMI. This is the fixture that proves the PCI probe carries
// its weight rather than merely corroborating DMI.
func qemuVirtioHost() *fakeFS {
	return newFakeFS().
		addFile("/proc/cpuinfo", "processor\t: 0\nflags\t\t: fpu vme de pse tsc\n").
		addFile("/proc/self/status", "TracerPid:\t0\n").
		addDir("/sys/class/net", "eth0").
		addFile("/sys/class/net/eth0/address", "52:54:00:12:34:56\n").
		addFile("/sys/class/dmi/id/sys_vendor", "QEMU\n").
		addDir("/sys/bus/pci/devices", "0000:00:02.0").
		addFile("/sys/bus/pci/devices/0000:00:02.0/vendor", "0x1af4\n").
		addFile("/sys/bus/pci/devices/0000:00:02.0/device", "0x1000\n")
}

// containerHost is a CI-like container: a cgroup marker, no DMI, no PCI.
func containerHost() *fakeFS {
	return newFakeFS().
		addFile("/proc/cpuinfo", "processor\t: 0\nflags\t\t: fpu\n").
		addFile("/proc/1/cgroup", "0::/docker/abc123\n").
		addFile("/.dockerenv", "").
		addFile("/proc/self/status", "TracerPid:\t0\n")
}

// tracedHost has a debugger attached.
func tracedHost() *fakeFS {
	return newFakeFS().
		addFile("/proc/cpuinfo", "processor\t: 0\nflags\t\t: fpu\n").
		addFile("/proc/self/status", "Name:\tnode\nTracerPid:\t4242\n")
}

// buildSMBIOS assembles a raw SMBIOS table from structures.
//
// Written so a test can construct a fixture rather than embedding an opaque
// blob: the structure encoding is then visible in the test, which is the point
// of parsing it ourselves.
func buildSMBIOS(structs ...smbiosStructure) []byte {
	var out []byte
	for _, st := range structs {
		length := len(st.Formatted)
		out = append(out, st.Type, byte(length), byte(st.Handle), byte(st.Handle>>8))
		out = append(out, st.Formatted...)
		for _, s := range st.Strings {
			out = append(out, []byte(s)...)
			out = append(out, 0)
		}
		// Double NUL terminates the string table for this structure.
		out = append(out, 0)
	}
	return out
}

// smbiosSystemInfo builds a Type 1 structure padded to a plausible formatted
// length, with the vendor as the first string slot.
//
// Type 1's formatted area is defined by spec revision, so a fixture must pad it
// to the right length for the string offsets to land where the spec says. 0x1b
// is the common vendor-specific length and is what real Type 1 records use.
func smbiosSystemInfo(vendor, product string) smbiosStructure {
	formatted := make([]byte, 0x1b)
	formatted[0x04] = 1 // string index for manufacturer
	return smbiosStructure{
		Type:      1,
		Handle:    1,
		Formatted: formatted,
		Strings:   []string{vendor, product},
	}
}

// smbiosOEM builds a Type 11 OEM strings structure. Type 11 is an all-strings
// structure: its formatted length is zero, which is the legal "bare string
// table" case the parser must handle.
func smbiosOEM(strs ...string) smbiosStructure {
	return smbiosStructure{Type: 11, Handle: 2, Strings: strs}
}
