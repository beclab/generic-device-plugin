package platformdevices

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/squat/generic-device-plugin/deviceplugin"
)

const scanInterval = 5 * time.Second

type ManagerConfig struct {
	NodeName   string
	PluginDir  string
	Logger     log.Logger
	Registerer prometheus.Registerer
}

type runningPlugin struct {
	cancel context.CancelFunc
	done   <-chan error
}

type Manager struct {
	config  ManagerConfig
	mu      sync.Mutex
	plugins map[string]runningPlugin
}

func NewManager(config ManagerConfig) *Manager {
	if config.Logger == nil {
		config.Logger = log.NewNopLogger()
	}
	return &Manager{config: config, plugins: make(map[string]runningPlugin)}
}

func (m *Manager) Run(ctx context.Context) error {
	publisher, err := NewPublisher(m.config.NodeName)
	if err != nil {
		return err
	}
	ticker := time.NewTicker(scanInterval)
	defer ticker.Stop()
	defer m.stopAll()
	for {
		if err := m.reconcile(ctx, publisher); err != nil {
			_ = level.Error(m.config.Logger).Log("msg", "platform device reconciliation failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (m *Manager) reconcile(ctx context.Context, publisher *Publisher) error {
	devices, err := Scan()
	if err != nil {
		return err
	}
	current := make(map[string]Device, len(devices))
	for _, device := range devices {
		current[device.ResourceName] = device
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for resourceName, running := range m.plugins {
		select {
		case runErr := <-running.done:
			delete(m.plugins, resourceName)
			if runErr != nil && ctx.Err() == nil {
				_ = level.Warn(m.config.Logger).Log("msg", "platform device plugin exited", "resource", resourceName, "err", runErr)
			}
			continue
		default:
		}
		if _, ok := current[resourceName]; !ok {
			running.cancel()
			delete(m.plugins, resourceName)
		}
	}
	for resourceName, device := range current {
		if !device.Selectable {
			continue
		}
		if _, ok := m.plugins[resourceName]; ok {
			continue
		}
		spec := &deviceplugin.DeviceSpec{Name: resourceName, Groups: []*deviceplugin.Group{{Count: 1}}}
		for _, devicePath := range device.KernelPaths {
			spec.Groups[0].Paths = append(spec.Groups[0].Paths, &deviceplugin.Path{Path: devicePath, MountPath: devicePath, Permissions: "mrw", Type: deviceplugin.DevicePathType})
		}
		spec.Default()
		pluginCtx, cancel := context.WithCancel(ctx)
		registerer := prometheus.WrapRegistererWith(prometheus.Labels{"resource": resourceName}, m.config.Registerer)
		plugin := deviceplugin.NewGenericPlugin(spec, m.config.PluginDir, log.With(m.config.Logger, "resource", resourceName), registerer, false)
		done := make(chan error, 1)
		go func() { done <- plugin.Run(pluginCtx) }()
		m.plugins[resourceName] = runningPlugin{cancel: cancel, done: done}
		_ = level.Info(m.config.Logger).Log("msg", "started platform device plugin", "resource", resourceName)
	}
	return publisher.Publish(ctx, BuildInventory(devices), BluetoothCapable())
}

func (m *Manager) stopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, plugin := range m.plugins {
		plugin.cancel()
		select {
		case err := <-plugin.done:
			if err != nil {
				_ = level.Warn(m.config.Logger).Log("msg", fmt.Sprintf("plugin %s stopped with error", name), "err", err)
			}
		case <-time.After(time.Second):
		}
	}
}
