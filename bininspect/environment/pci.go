package environment

// PCI-based hypervisor detection.
//
// This is the probe the kernel's decoded sysfs attributes cannot give us. DMI
// names the firmware; /proc/cpuinfo reports a flag. Neither tells you which
// virtual devices are actually present in the PCI bus — and a hypervisor that
// advertises no DMI string still necessarily exposes virtual hardware.
//
// virtio deserves specific mention because it is the most common case and the
// least self-identifying. A virtio-net NIC presents as an ordinary
// /sys/class/net entry, and a virtio-blk disk as an ordinary block device: the
// guest sees paravirtual hardware that behaves like real hardware. The only
// reliable tell is the PCI vendor ID, which is why this probe exists.
//
// The vendor and device tables below are written from the PCI-SIG vendor ID
// assignments, not imported from an external detection database. That is the
// point of owning them: a curated third-party list is a dependency whose
// contents and update cadence we would not control.

import "path/filepath"

// pciVendor identifies a PCI vendor whose presence indicates a hypervisor.
type pciVendor struct {
	name       string
	confidence float64
	// hypervisor is true when the vendor is not itself a real hardware maker,
	// which is what makes the ID decisive rather than suggestive.
	hypervisor bool
}

// pciHypervisorVendors is the vendor table.
//
// Confidence is high across the board because these IDs are assigned to
// paravirtual front-ends: no physical device ships under them. The exception is
// Microsoft, which is also an ordinary OEM, so a 0x1414 device is suggestive
// rather than conclusive unless it is a Hyper-V-specific device ID.
var pciHypervisorVendors = map[uint16]pciVendor{
	0x1af4: {"Red Hat / virtio", 0.95, true},
	0x15ad: {"VMware", 0.95, true},
	0x1b36: {"QEMU (modern)", 0.95, true},
	0x1d0f: {"Amazon (Nitro)", 0.93, true},
	0x5853: {"Xen", 0.93, true},
	0x80ee: {"VirtualBox", 0.95, true},
	0x1b21: {"Parallels", 0.93, true},
	0x1414: {"Microsoft (Hyper-V)", 0.70, false},
	0x1234: {"QEMU/Bochs (legacy)", 0.88, true},
	0x1013: {"Cirrus Logic (QEMU legacy)", 0.80, false},
}

// virtioDeviceNames maps virtio device IDs under vendor 0x1af4 to names.
//
// The split matters: legacy IDs (0x1000-0x1003) are the pre-2.6 paravirtual
// devices, while modern IDs begin at 0x1040. Seeing a modern virtio device is a
// stronger signal than a legacy one, because legacy IDs are also assigned to
// non-hypervisor paravirtual stacks such as gVisor and older jails.
var virtioDeviceNames = map[uint16]string{
	// Legacy (virtio 0.9 transport)
	0x1000: "legacy network",
	0x1001: "legacy block",
	0x1002: "legacy balloon",
	0x1003: "legacy console",
	0x1009: "legacy 9p",
	// Modern (virtio 1.0 transport)
	0x1040: "network",
	0x1041: "console",
	0x1042: "gpu",
	0x1043: "guest agent",
	0x1044: "input",
	0x1045: "gpu (virtio-gpu)",
	0x1048: "scsi",
	0x1049: "rdma",
	0x1050: "rng",
	0x1052: "socket",
	0x1053: "fs",
	0x1054: "scsi (virtio-scsi)",
	0x1055: "rdma",
}

// pciDevice is one parsed /sys/bus/pci/devices entry.
type pciDevice struct {
	addr    string
	vendor  uint16
	device  uint16
	class   string
	product string
}

// probePCI enumerates the PCI bus and reports hypervisor-attributable devices.
//
// Two indicators are produced, because they carry different weight: one per
// distinct hypervisor vendor (strong), and one for virtio specifically (named
// because virtio is the most common and least obviously virtual stack).
func probePCI(fs FileReader) ([]Indicator, []string) {
	if !fs.Exists("/sys/bus/pci/devices") {
		return nil, nil
	}
	addrs, err := fs.ReadDir("/sys/bus/pci/devices")
	if err != nil {
		return nil, nil
	}

	var devices []pciDevice
	for _, addr := range addrs {
		base := filepath.Join("/sys/bus/pci/devices", addr)
		vendor, ok := readHex16(fs, filepath.Join(base, "vendor"))
		if !ok {
			continue
		}
		dev, _ := readHex16(fs, filepath.Join(base, "device"))
		class, _ := readTrimmed(fs, filepath.Join(base, "class"))
		product, _ := readTrimmed(fs, filepath.Join(base, "uevent"))
		devices = append(devices, pciDevice{
			addr: addr, vendor: vendor, device: dev, class: class, product: product,
		})
	}
	if len(devices) == 0 {
		return nil, nil
	}

	// Group by vendor so one hypervisor yields one indicator, not one per device.
	byVendor := make(map[uint16][]pciDevice)
	var evidence []string
	for _, d := range devices {
		entry := d.addr + " vendor=" + hex16(d.vendor)
		if name, ok := virtioDeviceNames[d.device]; ok && d.vendor == 0x1af4 {
			entry += " device=" + hex16(d.device) + " (" + name + ")"
		} else if d.device != 0 {
			entry += " device=" + hex16(d.device)
		}
		evidence = append(evidence, "pci "+entry)
		byVendor[d.vendor] = append(byVendor[d.vendor], d)
	}

	var out []Indicator
	// Deterministic order so evidence is stable across runs.
	for _, id := range sortedUint16Keys(byVendor) {
		meta, known := pciHypervisorVendors[id]
		if !known || !meta.hypervisor {
			continue
		}
		conf := meta.confidence
		devs := byVendor[id]
		// Several distinct devices from one vendor is a stronger signal than a
		// single device, which could be a passed-through physical card.
		if len(devs) >= 2 {
			conf = 0.97
		}
		out = append(out, Indicator{
			ID:         "ENV-VM-PCI-" + slugVendor(id),
			Category:   CategoryVirtualisation,
			Severity:   SeverityHigh,
			Confidence: conf,
			Title:      "PCI bus exposes " + meta.name + " virtual devices",
			Evidence:   evidence,
		})
	}

	// virtio specifically, called out because it is the case most likely to be
	// mistaken for real hardware.
	if devs, ok := byVendor[0x1af4]; ok && len(devs) > 0 {
		var modern int
		var names []string
		for _, d := range devs {
			if d.device >= 0x1040 {
				modern++
			}
			if n, ok := virtioDeviceNames[d.device]; ok {
				names = append(names, n)
			}
		}
		conf := 0.90
		desc := "virtio paravirtual devices present"
		if modern > 0 {
			// A virtio 1.0 device cannot predate Linux 2.6.25 and only ever
			// appears under a hypervisor or a paravirtual runtime.
			conf = 0.95
			desc = "virtio 1.0 paravirtual devices present"
		}
		if len(names) > 0 {
			desc += ": " + joinComma(names)
		}
		out = append(out, Indicator{
			ID:         "ENV-VM-PCI-VIRTIO",
			Category:   CategoryVirtualisation,
			Severity:   SeverityHigh,
			Confidence: conf,
			Title:      desc,
			Evidence:   evidence,
		})
	}

	return out, evidence
}

// probeVirtioBound looks for bound virtio devices directly.
//
// /sys/bus/virtio/devices only lists devices whose driver has bound, so this
// corroborates probePCI from the driver's side rather than from the bus side.
func probeVirtioBound(fs FileReader) ([]Indicator, []string) {
	if !fs.Exists("/sys/bus/virtio/devices") {
		return nil, nil
	}
	names, err := fs.ReadDir("/sys/bus/virtio/devices")
	if err != nil || len(names) == 0 {
		return nil, nil
	}
	evidence := make([]string, 0, len(names))
	for _, n := range names {
		evidence = append(evidence, "bound virtio device: "+n)
	}
	return []Indicator{{
		ID:         "ENV-VM-VIRTIO-BOUND",
		Category:   CategoryVirtualisation,
		Severity:   SeverityMedium,
		Confidence: 0.88,
		Title:      "virtio drivers are bound in this guest",
		Evidence:   evidence,
	}}, evidence
}

// slugVendor renders a vendor ID as a stable identifier fragment.
func slugVendor(id uint16) string {
	const hexdigits = "0123456789abcdef"
	b := []byte{'0', 'x', hexdigits[id>>12&0xF], hexdigits[id>>8&0xF], hexdigits[id>>4&0xF], hexdigits[id&0xF]}
	return string(b)
}

// hex16 renders a 16-bit value as 0x-prefixed hex.
func hex16(v uint16) string {
	const d = "0123456789abcdef"
	return string([]byte{'0', 'x', d[v>>12&0xF], d[v>>8&0xF], d[v>>4&0xF], d[v&0xF]})
}

// sortedUint16Keys gives deterministic iteration over a vendor-keyed map.
func sortedUint16Keys(m map[uint16][]pciDevice) []uint16 {
	out := make([]uint16, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// joinComma joins strings with ", " without importing strings.
func joinComma(in []string) string {
	if len(in) == 0 {
		return ""
	}
	out := ""
	for i, s := range in {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
