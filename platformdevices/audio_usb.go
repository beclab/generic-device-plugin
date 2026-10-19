package platformdevices

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
	"k8s.io/klog/v2"
)

// AudioUSBID is a USB vendor/product pair used to decide when an ALSA card
// should also expose its usbfs control node in KernelPaths.
type AudioUSBID struct {
	Vendor  string
	Product string
}

// DefaultAudioUSBControl is the built-in allowlist (Pollen Reachy Mini Audio).
func DefaultAudioUSBControl() []AudioUSBID {
	return []AudioUSBID{{Vendor: "38fb", Product: "1001"}}
}

// ParseAudioUSBControl parses "vendor:product" entries (optional 0x prefix).
func ParseAudioUSBControl(values []string) ([]AudioUSBID, error) {
	out := make([]AudioUSBID, 0, len(values))
	for _, raw := range values {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		vendor, product, ok := strings.Cut(raw, ":")
		if !ok || vendor == "" || product == "" {
			return nil, fmt.Errorf("invalid audio-usb-control %q; want vendor:product", raw)
		}
		out = append(out, AudioUSBID{
			Vendor:  normalizeUSBID(vendor),
			Product: normalizeUSBID(product),
		})
	}
	return out, nil
}

func normalizeUSBID(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.TrimPrefix(value, "0x")
	return value
}

func matchesAudioUSBControl(allow []AudioUSBID, vendor, product string) bool {
	vendor = normalizeUSBID(vendor)
	product = normalizeUSBID(product)
	for _, id := range allow {
		if normalizeUSBID(id.Vendor) == vendor && normalizeUSBID(id.Product) == product {
			return true
		}
	}
	return false
}

// ScanConfig controls platform device discovery.
type ScanConfig struct {
	AudioUSBControl []AudioUSBID
}

func usbfsPathForSoundControl(controlPath string) (usbfsPath, vendor, product string, ok bool) {
	name := filepath.Base(controlPath)
	sysPath, err := sysfsDevicePath(controlPath, "sound", name)
	if err != nil {
		return "", "", "", false
	}
	for current := sysPath; current != "/" && current != "." && strings.HasPrefix(current, sysfsRoot); current = filepath.Dir(current) {
		vendor = readTrimmed(filepath.Join(current, "idVendor"))
		product = readTrimmed(filepath.Join(current, "idProduct"))
		if vendor == "" || product == "" {
			continue
		}
		bus := readTrimmed(filepath.Join(current, "busnum"))
		dev := readTrimmed(filepath.Join(current, "devnum"))
		if bus == "" || dev == "" {
			return "", "", "", false
		}
		busN, err1 := strconv.Atoi(bus)
		devN, err2 := strconv.Atoi(dev)
		if err1 != nil || err2 != nil {
			return "", "", "", false
		}
		return fmt.Sprintf("/dev/bus/usb/%03d/%03d", busN, devN), normalizeUSBID(vendor), normalizeUSBID(product), true
	}
	return "", "", "", false
}

// normalizeKernelPaths keeps audio identity stable: controlC* first, then other
// /dev/snd nodes, then usbfs, then anything else. Lexicographic sort alone would
// put /dev/bus/usb ahead of /dev/snd and break by-id / UID anchoring.
func normalizeKernelPaths(class string, paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	uniq := uniqueStrings(paths)
	if class != "audio" {
		sort.Strings(uniq)
		return uniq
	}
	var control, snd, usbfs, other []string
	for _, path := range uniq {
		switch {
		case audioCardPattern.MatchString(path):
			control = append(control, path)
		case strings.HasPrefix(path, "/dev/snd/"):
			snd = append(snd, path)
		case strings.HasPrefix(path, "/dev/bus/usb/"):
			usbfs = append(usbfs, path)
		default:
			other = append(other, path)
		}
	}
	sort.Strings(control)
	sort.Strings(snd)
	sort.Strings(usbfs)
	sort.Strings(other)
	out := make([]string, 0, len(uniq))
	out = append(out, control...)
	out = append(out, snd...)
	out = append(out, usbfs...)
	out = append(out, other...)
	return out
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func kernelPathsEqual(a, b []string) bool {
	a = normalizeKernelPaths("audio", a)
	b = normalizeKernelPaths("audio", b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// olaresAudioGID is the host audio group used by app-service supplementalGroups
// for device.audio (Debian/Ubuntu audio=29). Do not LookupGroup("audio") inside
// the plugin image: Alpine's audio group is 18 and would chown host nodes to a
// GID that application pods cannot use.
const olaresAudioGID = 29

// ensureUSBFSPermissions sets root:audio 0660 on a usbfs node.
func ensureUSBFSPermissions(path string) error {
	if !strings.HasPrefix(path, "/dev/bus/usb/") {
		return fmt.Errorf("not a usbfs path: %s", path)
	}
	gid := olaresAudioGID
	if err := unix.Chown(path, 0, gid); err != nil {
		return fmt.Errorf("chown %s root:%d: %w", path, gid, err)
	}
	if err := unix.Chmod(path, 0o660); err != nil {
		return fmt.Errorf("chmod %s 0660: %w", path, err)
	}
	return nil
}

func applyUSBFSPermissions(paths []string) {
	for _, path := range paths {
		if !strings.HasPrefix(path, "/dev/bus/usb/") {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			klog.Warningf("platformdevices: skip usbfs permissions for %s: %v", path, err)
			continue
		}
		if err := ensureUSBFSPermissions(path); err != nil {
			klog.Warningf("platformdevices: set usbfs permissions failed for %s: %v", path, err)
		}
	}
}
