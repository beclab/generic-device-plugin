package platformdevices

import "testing"

func TestShouldRunDevicePlugin(t *testing.T) {
	tests := []struct {
		name   string
		device Device
		want   bool
	}{
		{name: "managed selectable device", device: Device{Selectable: true, PluginManaged: true}, want: true},
		{name: "unselectable device", device: Device{Selectable: false, PluginManaged: true}, want: false},
		{name: "Bluetooth inventory only", device: Device{Type: "bluetooth", Selectable: true, PluginManaged: false}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldRunDevicePlugin(tt.device); got != tt.want {
				t.Fatalf("shouldRunDevicePlugin() = %t, want %t", got, tt.want)
			}
		})
	}
}
