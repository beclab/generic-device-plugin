package platformdevices

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStableUID(t *testing.T) {
	first := stableUID("1a86:7523:abc", true)
	if len(first) != 34 {
		t.Fatalf("UID %q must contain a prefix and 32 hex characters", first)
	}
	if first != stableUID("1a86:7523:abc", true) {
		t.Fatal("same physical identity produced different UIDs")
	}
	if first == stableUID("1a86:7523:def", true) || first == stableUID("1a86:7523:abc", false) {
		t.Fatal("different identity kind or value produced the same UID")
	}
}

func TestParseUdevDeviceDataReadsPropertiesAndIgnoresSymlinks(t *testing.T) {
	data := parseUdevDeviceData(strings.Join([]string{
		"S:serial/by-id/usb-SMLIGHT_SMLIGHT_SLZB-07Mg24_2cdd4878f9d6ef11b30c49878f302768-if00-port0",
		"E:ID_SERIAL=SMLIGHT_SMLIGHT_SLZB-07Mg24_legacy",
		"E:ID_PATH=pci-0000:00:14.0-usb-0:1:1.0",
	}, "\n"))

	if data.properties["ID_SERIAL"] != "SMLIGHT_SMLIGHT_SLZB-07Mg24_legacy" {
		t.Fatalf("ID_SERIAL = %q", data.properties["ID_SERIAL"])
	}
	if data.properties["ID_PATH"] != "pci-0000:00:14.0-usb-0:1:1.0" {
		t.Fatalf("ID_PATH = %q", data.properties["ID_PATH"])
	}
}

func TestMarshalInventoryReturnsCompleteJSON(t *testing.T) {
	devices := make([]Device, 20)
	for i := range devices {
		devices[i] = Device{UID: stableUID(strings.Repeat("x", i+1), true), Type: "hid", DisplayName: strings.Repeat("device", 10)}
	}
	inventory := BuildInventory(devices)
	want, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	got, err := marshalInventory(inventory, len(want))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("marshalInventory() returned modified inventory")
	}
}

func TestMarshalInventoryRejectsOversizedJSON(t *testing.T) {
	inventory := Inventory{SchemaVersion: 1, HID: []Device{{DisplayName: strings.Repeat("x", 256)}}}
	if _, err := marshalInventory(inventory, 100); err == nil {
		t.Fatal("marshalInventory() accepted oversized JSON")
	}
}

func TestMergeDeviceGroupsCombinesKernelNodes(t *testing.T) {
	devices := mergeDeviceGroups([]Device{
		{UID: "v-1", ResourceName: "devices.bytetrade.io/video-v-1", Type: "video", KernelPaths: []string{"/dev/video1"}, Selectable: true},
		{UID: "v-1", ResourceName: "devices.bytetrade.io/video-v-1", Type: "video", KernelPaths: []string{"/dev/video0"}, Selectable: true},
	})
	if len(devices) != 1 || len(devices[0].KernelPaths) != 2 || devices[0].KernelPaths[0] != "/dev/video0" {
		t.Fatalf("physical device nodes were not grouped: %#v", devices)
	}
}

func TestMergeDeviceGroups(t *testing.T) {
	devices := mergeDeviceGroups([]Device{
		{UID: "v-1", ResourceName: "devices.bytetrade.io/video-v-1", Type: "video", Selectable: true, KernelPaths: []string{"/dev/video1"}},
		{UID: "v-1", ResourceName: "devices.bytetrade.io/video-v-1", Type: "video", Selectable: true, KernelPaths: []string{"/dev/video0"}},
	})
	if len(devices) != 1 || len(devices[0].KernelPaths) != 2 || devices[0].KernelPaths[0] != "/dev/video0" {
		t.Fatalf("unexpected merged group: %#v", devices)
	}
}

func TestMergeDeviceGroupsRejectsDuplicateSerialAcrossPhysicalPaths(t *testing.T) {
	devices := mergeDeviceGroups([]Device{
		{UID: "s-1", ResourceName: "devices.bytetrade.io/video-s-1", Type: "video", Selectable: true, KernelPaths: []string{"/dev/video0"}, serialBased: true, physicalPath: "usb-a"},
		{UID: "s-1", ResourceName: "devices.bytetrade.io/video-s-1", Type: "video", Selectable: true, KernelPaths: []string{"/dev/video1"}, serialBased: true, physicalPath: "usb-b"},
	})
	if len(devices) != 1 || devices[0].Selectable || devices[0].Reason != "duplicateSerial" {
		t.Fatalf("duplicate serial was not rejected: %#v", devices)
	}
}

func TestGroupVideoPathsUsesUSBParent(t *testing.T) {
	parents := map[string]string{
		"/dev/video0": "/sys/devices/usb1/1-10",
		"/dev/video1": "/sys/devices/usb1/1-10",
		"/dev/media0": "/sys/devices/usb1/1-10",
		"/dev/video2": "/sys/devices/usb1/1-11",
		"/dev/video3": "/sys/devices/usb1/1-11",
		"/dev/media1": "/sys/devices/usb1/1-11",
	}
	groups := groupVideoPaths([]string{
		"/dev/video0", "/dev/video1", "/dev/media0",
		"/dev/video2", "/dev/video3", "/dev/media1",
	}, func(path string) (string, bool) {
		parent, ok := parents[path]
		return parent, ok
	})
	if len(groups) != 2 {
		t.Fatalf("video groups = %#v", groups)
	}
	if got := groups[0].paths; len(got) != 3 || got[0] != "/dev/media0" || got[1] != "/dev/video0" || got[2] != "/dev/video1" {
		t.Fatalf("first camera paths = %#v", got)
	}
	if got := groups[1].paths; len(got) != 3 || got[0] != "/dev/media1" || got[1] != "/dev/video2" || got[2] != "/dev/video3" {
		t.Fatalf("second camera paths = %#v", got)
	}
}

func TestInspectUSBVideoDeviceWithoutHardwareSerialUsesPortIdentity(t *testing.T) {
	usbParent := t.TempDir()
	for name, value := range map[string]string{
		"idVendor":     "0c45\n",
		"idProduct":    "6368\n",
		"manufacturer": "Sonix Technology Co., Ltd.\n",
		"product":      "USB 2.0 Camera\n",
	} {
		if err := os.WriteFile(filepath.Join(usbParent, name), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	device := inspectUSBVideoDevice([]string{"/dev/video1", "/dev/media0", "/dev/video0"}, usbParent)
	identity, stable := buildUSBIdentity(usbParent, usbParent, "class=video")
	wantUID := stableUID(identity, stable)
	if device.UID != wantUID || !device.PortUnstable || device.serialBased {
		t.Fatalf("port-based video identity = %#v", device)
	}
	if device.DisplayName != "Sonix Technology Co., Ltd. USB 2.0 Camera" {
		t.Fatalf("display name = %q", device.DisplayName)
	}
	if got := device.KernelPaths; len(got) != 3 || got[0] != "/dev/media0" || got[1] != "/dev/video0" || got[2] != "/dev/video1" {
		t.Fatalf("kernel paths = %#v", got)
	}
}

func TestInspectUSBVideoDeviceWithHardwareSerialUsesStableIdentity(t *testing.T) {
	usbParent := t.TempDir()
	for name, value := range map[string]string{
		"idVendor":  "0c45\n",
		"idProduct": "6368\n",
		"serial":    "camera-123\n",
	} {
		if err := os.WriteFile(filepath.Join(usbParent, name), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	device := inspectUSBVideoDevice([]string{"/dev/video0", "/dev/media0"}, usbParent)
	identity, stable := buildUSBIdentity(usbParent, usbParent, "class=video")
	wantUID := stableUID(identity, stable)
	if device.UID != wantUID || device.PortUnstable || !device.serialBased {
		t.Fatalf("serial-based video identity = %#v", device)
	}
}

func TestBuildUSBIdentityUsesHardwareSerial(t *testing.T) {
	usbParent := t.TempDir()
	for name, value := range map[string]string{
		"idVendor":  "10c4\n",
		"idProduct": "ea60\n",
		"serial":    "2cdd4878f9d6ef11b30c49878f302768\n",
	} {
		if err := os.WriteFile(filepath.Join(usbParent, name), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	identity, stable := buildUSBIdentity(usbParent, "ignored-path",
		"class=serial", "interface=00", "port=0")
	want := strings.Join([]string{
		"uid=v2", "bus=usb", "vendor=10c4", "product=ea60",
		"serial=2cdd4878f9d6ef11b30c49878f302768",
		"class=serial", "interface=00", "port=0",
	}, "\x00")
	if identity != want || !stable {
		t.Fatalf("serial identity = %q stable=%t", identity, stable)
	}
}

func TestBuildUSBIdentityUsesPhysicalPathWithoutHardwareSerial(t *testing.T) {
	usbParent := t.TempDir()
	for name, value := range map[string]string{
		"idVendor":  "0c45\n",
		"idProduct": "6368\n",
	} {
		if err := os.WriteFile(filepath.Join(usbParent, name), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	physicalPath := "pci-0000:00:14.0-usb-0:10:1.0"
	identity, stable := buildUSBIdentity(usbParent, physicalPath, "class=video")
	want := strings.Join([]string{
		"uid=v2", "bus=usb", "vendor=0c45", "product=6368",
		"path=" + physicalPath, "class=video",
	}, "\x00")
	if identity != want || stable {
		t.Fatalf("path identity = %q stable=%t", identity, stable)
	}
}

func TestUSBPhysicalPathRemovesInterfaceSuffix(t *testing.T) {
	properties := map[string]string{"ID_PATH": "pci-0000:00:14.0-usb-0:2.2:1.0"}
	if got := usbPhysicalPath(properties, "/sys/fallback"); got != "pci-0000:00:14.0-usb-0:2.2" {
		t.Fatalf("USB physical path = %q", got)
	}
	if got := usbPhysicalPath(nil, "/sys/fallback"); got != "/sys/fallback" {
		t.Fatalf("fallback USB physical path = %q", got)
	}
}

func TestSyncDeviceIndex(t *testing.T) {
	root := t.TempDir()
	device := Device{UID: "s-1", Type: "serial", KernelPaths: []string{"/dev/ttyACM0"}}
	if err := syncDeviceIndexAt(root, []Device{device}); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(root, "serial", "s-1")
	if target, err := os.Readlink(indexPath); err != nil || target != "/dev/ttyACM0" {
		t.Fatalf("device index target=%q err=%v", target, err)
	}
	if err := syncDeviceIndexAt(root, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(indexPath); !os.IsNotExist(err) {
		t.Fatalf("stale device index was not removed: %v", err)
	}
}
