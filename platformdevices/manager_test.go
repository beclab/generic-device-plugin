package platformdevices

import "testing"

func TestReconcileKernelPathsChangeDetection(t *testing.T) {
	before := []string{"/dev/snd/controlC2", "/dev/snd/pcmC2D0c"}
	after := []string{"/dev/snd/controlC2", "/dev/snd/pcmC2D0c", "/dev/bus/usb/001/023"}
	if kernelPathsEqual(before, after) {
		t.Fatal("adding usbfs must count as a KernelPaths change")
	}
	replug := []string{"/dev/snd/controlC2", "/dev/snd/pcmC2D0c", "/dev/bus/usb/001/024"}
	if kernelPathsEqual(after, replug) {
		t.Fatal("usbfs bus/dev change must restart the plugin")
	}
	same := []string{"/dev/bus/usb/001/023", "/dev/snd/pcmC2D0c", "/dev/snd/controlC2"}
	if !kernelPathsEqual(after, same) {
		t.Fatal("order-only differences must not restart")
	}
}
