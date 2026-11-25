package platformdevices

import (
	"testing"

	"github.com/go-kit/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/squat/generic-device-plugin/deviceplugin"
)

func TestPluginMetricsCanRegisterAgainAfterUnregister(t *testing.T) {
	root := prometheus.NewRegistry()
	const resourceName = "devices.bytetrade.io/serial-s-test"
	spec := &deviceplugin.DeviceSpec{
		Name:   resourceName,
		Groups: []*deviceplugin.Group{{Count: 1}},
	}
	spec.Default()

	first := newPluginMetrics(root, resourceName)
	deviceplugin.NewGenericPlugin(spec, t.TempDir(), log.NewNopLogger(), first.registry, false)
	if err := first.register(); err != nil {
		t.Fatalf("register first plugin metrics: %v", err)
	}
	first.unregister()

	second := newPluginMetrics(root, resourceName)
	deviceplugin.NewGenericPlugin(spec, t.TempDir(), log.NewNopLogger(), second.registry, false)
	if err := second.register(); err != nil {
		t.Fatalf("register replacement plugin metrics: %v", err)
	}
}

func TestPluginMetricsUseResourceLabel(t *testing.T) {
	root := prometheus.NewRegistry()
	const resourceName = "devices.bytetrade.io/audio-p-test"
	metrics := newPluginMetrics(root, resourceName)
	metrics.registry.MustRegister(prometheus.NewGauge(prometheus.GaugeOpts{Name: "platform_device_test_metric"}))
	if err := metrics.register(); err != nil {
		t.Fatal(err)
	}

	families, err := root.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 1 || len(families[0].Metric) != 1 {
		t.Fatalf("unexpected gathered metrics: %#v", families)
	}
	labels := families[0].Metric[0].Label
	if len(labels) != 1 || labels[0].GetName() != "resource" || labels[0].GetValue() != resourceName {
		t.Fatalf("resource label = %#v", labels)
	}
}
