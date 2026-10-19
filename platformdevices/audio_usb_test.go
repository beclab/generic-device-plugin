package platformdevices

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseAudioUSBControl(t *testing.T) {
	ids, err := ParseAudioUSBControl([]string{"38FB:1001", "0x1234:0xabcd", ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0].Vendor != "38fb" || ids[0].Product != "1001" || ids[1].Vendor != "1234" || ids[1].Product != "abcd" {
		t.Fatalf("parsed %#v", ids)
	}
	if _, err := ParseAudioUSBControl([]string{"bad"}); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestMatchesAudioUSBControl(t *testing.T) {
	allow := []AudioUSBID{{Vendor: "38fb", Product: "1001"}}
	if !matchesAudioUSBControl(allow, "38FB", "1001") {
		t.Fatal("expected match")
	}
	if matchesAudioUSBControl(allow, "38fb", "1002") {
		t.Fatal("unexpected match")
	}
	if matchesAudioUSBControl(nil, "38fb", "1001") {
		t.Fatal("empty allowlist should not match")
	}
}

func TestNormalizeKernelPathsAudioKeepsControlFirst(t *testing.T) {
	paths := normalizeKernelPaths("audio", []string{
		"/dev/bus/usb/001/023",
		"/dev/snd/pcmC2D0c",
		"/dev/snd/controlC2",
		"/dev/snd/pcmC2D0p",
	})
	if len(paths) != 4 || paths[0] != "/dev/snd/controlC2" {
		t.Fatalf("controlC must stay first: %#v", paths)
	}
	if paths[len(paths)-1] != "/dev/bus/usb/001/023" {
		t.Fatalf("usbfs should follow snd nodes: %#v", paths)
	}
}

func TestInspectDeviceAudioStableWithUSBFS(t *testing.T) {
	device := inspectDevice("audio", []string{
		"/dev/bus/usb/001/023",
		"/dev/snd/pcmC2D0p",
		"/dev/snd/controlC2",
	})
	if device.KernelPaths[0] != "/dev/snd/controlC2" {
		t.Fatalf("KernelPaths[0]=%q want controlC2", device.KernelPaths[0])
	}
	if device.Type != "audio" {
		t.Fatalf("type=%q", device.Type)
	}
}

func TestMergeDeviceGroupsAudioKeepsControlFirstWithUSBFS(t *testing.T) {
	devices := mergeDeviceGroups([]Device{
		{
			UID:          "a-1",
			ResourceName: "devices.bytetrade.io/audio-a-1",
			Type:         "audio",
			Selectable:   true,
			KernelPaths:  []string{"/dev/snd/controlC2", "/dev/snd/pcmC2D0c", "/dev/bus/usb/001/023"},
		},
	})
	if len(devices) != 1 || devices[0].KernelPaths[0] != "/dev/snd/controlC2" {
		t.Fatalf("merge reordered audio paths: %#v", devices)
	}
	foundUSBFS := false
	for _, path := range devices[0].KernelPaths {
		if path == "/dev/bus/usb/001/023" {
			foundUSBFS = true
		}
	}
	if !foundUSBFS {
		t.Fatalf("usbfs missing after merge: %#v", devices[0].KernelPaths)
	}
}

func TestKernelPathsEqual(t *testing.T) {
	a := []string{"/dev/snd/controlC2", "/dev/bus/usb/001/023"}
	b := []string{"/dev/bus/usb/001/023", "/dev/snd/controlC2"}
	if !kernelPathsEqual(a, b) {
		t.Fatal("same set should compare equal after normalize")
	}
	if kernelPathsEqual(a, []string{"/dev/snd/controlC2", "/dev/bus/usb/001/024"}) {
		t.Fatal("different usbfs should not be equal")
	}
}

func TestEnsureUSBFSPermissionsRejectsNonUSBFS(t *testing.T) {
	if err := ensureUSBFSPermissions("/tmp/not-usbfs"); err == nil {
		t.Fatal("expected error for non-usbfs path")
	}
}

func TestOlaresAudioGIDMatchesAppService(t *testing.T) {
	if olaresAudioGID != 29 {
		t.Fatalf("olaresAudioGID=%d want 29 (app-service device.audio supplementalGroups)", olaresAudioGID)
	}
}

func TestUSBFSPathForSoundControlAllowlist(t *testing.T) {
	root := t.TempDir()
	usbDev := filepath.Join(root, "devices", "pci0000:00", "usb1", "1-4", "1-4.1")
	card := filepath.Join(usbDev, "1-4.1:1.0", "sound", "card9")
	controlSys := filepath.Join(card, "controlC9")
	classLinkDir := filepath.Join(root, "class", "sound")
	for _, dir := range []string{usbDev, card, classLinkDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(controlSys, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(usbDev, "idVendor"), "38fb")
	write(filepath.Join(usbDev, "idProduct"), "1001")
	write(filepath.Join(usbDev, "busnum"), "1")
	write(filepath.Join(usbDev, "devnum"), "23")
	controlClass := filepath.Join(classLinkDir, "controlC9")
	if err := os.MkdirAll(controlClass, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(card, filepath.Join(controlClass, "device")); err != nil {
		t.Fatal(err)
	}

	prev := sysfsRoot
	sysfsRoot = root
	defer func() { sysfsRoot = prev }()

	usbfsPath, vendor, product, ok := usbfsPathForSoundControl("/dev/snd/controlC9")
	if !ok {
		t.Fatal("expected usbfs resolution")
	}
	if usbfsPath != "/dev/bus/usb/001/023" || vendor != "38fb" || product != "1001" {
		t.Fatalf("got %s %s:%s", usbfsPath, vendor, product)
	}
	if !matchesAudioUSBControl(DefaultAudioUSBControl(), vendor, product) {
		t.Fatal("default allowlist should include 38fb:1001")
	}
	if matchesAudioUSBControl([]AudioUSBID{}, vendor, product) {
		t.Fatal("empty allowlist must not match")
	}
}
