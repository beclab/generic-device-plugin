package platformdevices

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"k8s.io/klog/v2"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	resourceDomain  = "devices.bytetrade.io"
	deviceIndexRoot = "/dev/olares/by-id"
)

var audioCardPattern = regexp.MustCompile(`^/dev/snd/controlC([0-9]+)$`)
var usbInterfaceSuffixPattern = regexp.MustCompile(`:[0-9]+\.[0-9]+$`)

type Device struct {
	UID          string   `json:"uid"`
	ResourceName string   `json:"resourceName"`
	DisplayName  string   `json:"displayName,omitempty"`
	Online       bool     `json:"online"`
	Selectable   bool     `json:"selectable"`
	KernelPaths  []string `json:"kernelPaths,omitempty"`
	PortUnstable bool     `json:"portUnstable,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	Type         string   `json:"-"`
	physicalPath string
	serialBased  bool
}

type Inventory struct {
	SchemaVersion int      `json:"schemaVersion"`
	Serial        []Device `json:"serial,omitempty"`
	Video         []Device `json:"video,omitempty"`
	Audio         []Device `json:"audio,omitempty"`
	HID           []Device `json:"hid,omitempty"`
}

func Scan() ([]Device, error) {
	groups := []struct {
		class    string
		patterns []string
	}{
		{class: "serial", patterns: []string{"/dev/ttyUSB*", "/dev/ttyACM*", "/dev/rfcomm*"}},
		{class: "hid", patterns: []string{"/dev/hidraw*"}},
	}
	var result []Device
	for _, group := range groups {
		for _, pattern := range group.patterns {
			paths, err := filepath.Glob(pattern)
			if err != nil {
				return nil, err
			}
			for _, devicePath := range paths {
				if group.class == "hid" && isKeyboardDevice(devicePath) {
					continue
				}
				if group.class == "serial" {
					if usbParent, ok := usbParentForDevicePath(devicePath, "tty"); ok {
						result = append(result, inspectUSBSerialDevice(devicePath, usbParent))
						continue
					}
				}
				result = append(result, inspectDevice(group.class, []string{devicePath}))
			}
		}
	}
	var videoPaths []string
	for _, pattern := range []string{"/dev/video[0-9]*", "/dev/media[0-9]*"} {
		paths, err := filepath.Glob(pattern)
		if err != nil {
			return nil, err
		}
		videoPaths = append(videoPaths, paths...)
	}
	for _, group := range groupVideoPaths(videoPaths, usbParentForVideoPath) {
		if group.usbParent == "" {
			result = append(result, inspectDevice("video", group.paths))
			continue
		}
		result = append(result, inspectUSBVideoDevice(group.paths, group.usbParent))
	}
	controls, err := filepath.Glob("/dev/snd/controlC*")
	if err != nil {
		return nil, err
	}
	for _, control := range controls {
		match := audioCardPattern.FindStringSubmatch(control)
		if len(match) != 2 {
			continue
		}
		paths, _ := filepath.Glob("/dev/snd/*C" + match[1] + "D*")
		paths = append([]string{control}, paths...)
		result = append(result, inspectDevice("audio", paths))
	}
	result = mergeDeviceGroups(result)
	if err := syncDeviceIndexAt(deviceIndexRoot, result); err != nil {
		return nil, err
	}
	return result, nil
}

type videoPathGroup struct {
	key       string
	usbParent string
	paths     []string
}

func groupVideoPaths(paths []string, parentFor func(string) (string, bool)) []videoPathGroup {
	byKey := make(map[string]videoPathGroup)
	for _, path := range paths {
		parent, ok := parentFor(path)
		key := "node:" + path
		if ok {
			key = "usb:" + parent
		}
		group := byKey[key]
		group.key = key
		if ok {
			group.usbParent = parent
		}
		group.paths = append(group.paths, path)
		byKey[key] = group
	}
	result := make([]videoPathGroup, 0, len(byKey))
	for _, group := range byKey {
		sort.Strings(group.paths)
		result = append(result, group)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].key < result[j].key })
	return result
}

func usbParentForVideoPath(devicePath string) (string, bool) {
	name := filepath.Base(devicePath)
	class := "video4linux"
	if strings.HasPrefix(name, "media") {
		class = "media"
	}
	return usbParentForDevicePath(devicePath, class)
}

func usbParentForDevicePath(devicePath, class string) (string, bool) {
	name := filepath.Base(devicePath)
	sysPath, err := sysfsDevicePath(devicePath, class, name)
	if err != nil {
		return "", false
	}
	for current := sysPath; ; current = filepath.Dir(current) {
		if readTrimmed(filepath.Join(current, "idVendor")) != "" && readTrimmed(filepath.Join(current, "idProduct")) != "" {
			return current, true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false
		}
	}
}

func sysfsDevicePath(devicePath, class, name string) (string, error) {
	classPath := filepath.Join("/sys/class", class, name, "device")
	if sysPath, err := filepath.EvalSymlinks(classPath); err == nil {
		return sysPath, nil
	}
	var stat unix.Stat_t
	if err := unix.Stat(devicePath, &stat); err != nil {
		return "", err
	}
	deviceNumberPath := fmt.Sprintf("/sys/dev/char/%d:%d", unix.Major(uint64(stat.Rdev)), unix.Minor(uint64(stat.Rdev)))
	return filepath.EvalSymlinks(deviceNumberPath)
}

func inspectUSBSerialDevice(devicePath, usbParent string) Device {
	properties := readUdevProperties(devicePath)
	physicalPath := usbPhysicalPath(properties, usbParent)
	interfaceNumber, portNumber := usbSerialFunction(devicePath, usbParent, properties)
	identity, stable := buildUSBIdentity(usbParent, physicalPath,
		"class=serial",
		"interface="+interfaceNumber,
		"port="+portNumber,
	)
	displayName := udevDisplayName(properties)
	if displayName == "" {
		displayName = usbDisplayName(usbParent)
	}
	if displayName == "" {
		displayName = filepath.Base(devicePath)
	}
	uid := stableUID(identity, stable)
	return Device{
		UID:          uid,
		ResourceName: fmt.Sprintf("%s/serial-%s", resourceDomain, uid),
		DisplayName:  displayName,
		Online:       true,
		Selectable:   true,
		KernelPaths:  []string{devicePath},
		PortUnstable: !stable,
		Type:         "serial",
		physicalPath: physicalPath,
		serialBased:  stable,
	}
}

func usbSerialFunction(devicePath, usbParent string, properties map[string]string) (interfaceNumber, portNumber string) {
	interfaceNumber = properties["ID_USB_INTERFACE_NUM"]
	sysPath, err := sysfsDevicePath(devicePath, "tty", filepath.Base(devicePath))
	if err != nil {
		return interfaceNumber, ""
	}
	for current := sysPath; ; current = filepath.Dir(current) {
		if portNumber == "" {
			portNumber = readTrimmed(filepath.Join(current, "port_number"))
		}
		if interfaceNumber == "" {
			interfaceNumber = readTrimmed(filepath.Join(current, "bInterfaceNumber"))
		}
		if current == usbParent {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return interfaceNumber, portNumber
}

func buildUSBIdentity(usbParent, physicalPath string, functionParts ...string) (identity string, stable bool) {
	vendor := readTrimmed(filepath.Join(usbParent, "idVendor"))
	product := readTrimmed(filepath.Join(usbParent, "idProduct"))
	hardwareSerial := readTrimmed(filepath.Join(usbParent, "serial"))
	parts := []string{"uid=v2", "bus=usb", "vendor=" + vendor, "product=" + product}
	if hardwareSerial != "" {
		parts = append(parts, "serial="+hardwareSerial)
		stable = true
	} else {
		parts = append(parts, "path="+physicalPath)
	}
	parts = append(parts, functionParts...)
	return strings.Join(parts, "\x00"), stable
}

func usbPhysicalPath(properties map[string]string, usbParent string) string {
	physicalPath := properties["ID_PATH"]
	if physicalPath == "" {
		return usbParent
	}
	return usbInterfaceSuffixPattern.ReplaceAllString(physicalPath, "")
}

func usbDisplayName(usbParent string) string {
	return strings.TrimSpace(strings.Join([]string{
		readTrimmed(filepath.Join(usbParent, "manufacturer")),
		readTrimmed(filepath.Join(usbParent, "product")),
	}, " "))
}

func inspectUSBVideoDevice(paths []string, usbParent string) Device {
	sort.Strings(paths)
	primary := paths[0]
	for _, path := range paths {
		if strings.HasPrefix(filepath.Base(path), "video") {
			primary = path
			break
		}
	}
	properties := readUdevProperties(primary)
	displayName := udevDisplayName(properties)
	physicalPath := usbPhysicalPath(properties, usbParent)
	identity, stable := buildUSBIdentity(usbParent, physicalPath, "class=video")
	if displayName == "" {
		displayName = usbDisplayName(usbParent)
	}
	if displayName == "" {
		displayName = filepath.Base(primary)
	}
	uid := stableUID(identity, stable)
	return Device{
		UID:          uid,
		ResourceName: fmt.Sprintf("%s/video-%s", resourceDomain, uid),
		DisplayName:  displayName,
		Online:       true,
		Selectable:   true,
		KernelPaths:  paths,
		PortUnstable: !stable,
		Type:         "video",
		physicalPath: physicalPath,
		serialBased:  stable,
	}
}

func syncDeviceIndexAt(root string, devices []Device) error {
	desired := make(map[string]string, len(devices))
	for _, device := range devices {
		if len(device.KernelPaths) == 0 {
			continue
		}
		desired[filepath.Join(root, device.Type, device.UID)] = device.KernelPaths[0]
	}
	for _, class := range []string{"serial", "video", "audio", "hid"} {
		directory := filepath.Join(root, class)
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return fmt.Errorf("create device index %s: %w", directory, err)
		}
		entries, err := os.ReadDir(directory)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			indexPath := filepath.Join(directory, entry.Name())
			if _, keep := desired[indexPath]; keep || entry.IsDir() {
				continue
			}
			if err := os.Remove(indexPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove stale device index %s: %w", indexPath, err)
			}
		}
	}
	for indexPath, target := range desired {
		current, err := os.Readlink(indexPath)
		if err == nil && current == target {
			continue
		}
		if err == nil || !os.IsNotExist(err) {
			if removeErr := os.Remove(indexPath); removeErr != nil {
				return fmt.Errorf("replace device index %s: %w", indexPath, removeErr)
			}
		}
		if err := os.Symlink(target, indexPath); err != nil {
			return fmt.Errorf("create device index %s: %w", indexPath, err)
		}
	}
	return nil
}

func mergeDeviceGroups(devices []Device) []Device {
	byResource := make(map[string]Device)
	for _, device := range devices {
		current, exists := byResource[device.ResourceName]
		if !exists {
			byResource[device.ResourceName] = device
			continue
		}
		current.KernelPaths = append(current.KernelPaths, device.KernelPaths...)
		if current.serialBased && device.serialBased && current.physicalPath != "" && device.physicalPath != "" && current.physicalPath != device.physicalPath {
			current.Selectable = false
			current.Reason = "duplicateSerial"
		}
		current.Selectable = current.Selectable && device.Selectable
		if current.Reason == "" {
			current.Reason = device.Reason
		}
		byResource[device.ResourceName] = current
	}
	result := make([]Device, 0, len(byResource))
	for _, device := range byResource {
		sort.Strings(device.KernelPaths)
		result = append(result, device)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ResourceName < result[j].ResourceName })
	return result
}

func inspectDevice(class string, paths []string) Device {
	sort.Strings(paths)
	primary := paths[0]
	identity, displayName, physicalPath, stable := sysfsIdentity(primary)
	uid := stableUID(identity, stable)
	device := Device{
		UID:          uid,
		ResourceName: fmt.Sprintf("%s/%s-%s", resourceDomain, class, uid),
		DisplayName:  displayName,
		Online:       true,
		Selectable:   true,
		KernelPaths:  paths,
		PortUnstable: !stable,
		Type:         class,
		physicalPath: physicalPath,
		serialBased:  stable,
	}
	if device.DisplayName == "" {
		device.DisplayName = filepath.Base(primary)
	}
	return device
}

func stableUID(identity string, stable bool) string {
	kind := "path"
	prefix := "p-"
	if stable {
		kind = "serial"
		prefix = "s-"
	}
	sum := sha256.Sum256([]byte(kind + "\x00" + identity))
	return prefix + hex.EncodeToString(sum[:16])
}

func sysfsIdentity(devicePath string) (identity, displayName, physicalPath string, stable bool) {
	name := filepath.Base(devicePath)
	class := ""
	switch {
	case strings.HasPrefix(name, "video"):
		class = "video4linux"
	case strings.HasPrefix(name, "media"):
		class = "media"
	case strings.HasPrefix(name, "hidraw"):
		class = "hidraw"
	case strings.HasPrefix(devicePath, "/dev/snd/"):
		class = "sound"
	default:
		class = "tty"
	}

	udevData := readUdevDeviceData(devicePath)
	properties := udevData.properties
	displayName = udevDisplayName(properties)
	sysPath, err := sysfsDevicePath(devicePath, class, name)
	if err != nil {
		sysPath = devicePath
	}
	physicalPath = properties["ID_PATH"]
	if physicalPath == "" {
		physicalPath = sysPath
	}
	klog.Infof("name: %s, devicePath: %s,udevData: %#v", name, devicePath, udevData)
	if serial := properties["ID_SERIAL"]; serial != "" {
		return serial, displayName, physicalPath, true
	}
	if err != nil {
		return strings.Join([]string{physicalPath, properties["ID_VENDOR_ID"], properties["ID_MODEL_ID"]}, "\x00"), displayName, physicalPath, false
	}
	var serial, vendor, product, manufacturer string
	for current := sysPath; current != "/" && strings.HasPrefix(current, "/sys/"); current = filepath.Dir(current) {
		if serial == "" {
			serial = readTrimmed(filepath.Join(current, "serial"))
		}
		if vendor == "" {
			vendor = readTrimmed(filepath.Join(current, "idVendor"))
		}
		if product == "" {
			product = readTrimmed(filepath.Join(current, "product"))
		}
		if manufacturer == "" {
			manufacturer = readTrimmed(filepath.Join(current, "manufacturer"))
		}
	}
	if displayName == "" {
		displayName = strings.TrimSpace(strings.Join([]string{manufacturer, product}, " "))
	}
	if serial != "" {
		return strings.Join([]string{vendor, product, serial}, ":"), displayName, sysPath, true
	}
	pathID := properties["ID_PATH"]
	if pathID == "" {
		pathID = sysPath
	}
	return strings.Join([]string{pathID, properties["ID_VENDOR_ID"], properties["ID_MODEL_ID"]}, "\x00"), displayName, pathID, false
}

type udevDeviceData struct {
	properties map[string]string
}

func readUdevDeviceData(devicePath string) udevDeviceData {
	result := udevDeviceData{properties: make(map[string]string)}
	var stat unix.Stat_t
	if err := unix.Stat(devicePath, &stat); err != nil {
		return result
	}
	name := fmt.Sprintf("/run/udev/data/c%d:%d", unix.Major(uint64(stat.Rdev)), unix.Minor(uint64(stat.Rdev)))
	data, err := os.ReadFile(name)
	if err != nil {
		return result
	}
	result = parseUdevDeviceData(string(data))
	return result
}

func parseUdevDeviceData(raw string) udevDeviceData {
	result := udevDeviceData{properties: make(map[string]string)}
	for _, line := range strings.Split(raw, "\n") {
		switch {
		case strings.HasPrefix(line, "E:"):
			key, value, ok := strings.Cut(strings.TrimPrefix(line, "E:"), "=")
			if ok {
				result.properties[key] = value
			}
		}
	}
	return result
}

func udevDisplayName(properties map[string]string) string {
	displayName := strings.TrimSpace(strings.Join([]string{properties["ID_VENDOR_FROM_DATABASE"], properties["ID_MODEL_FROM_DATABASE"]}, " "))
	if displayName == "" {
		displayName = strings.TrimSpace(strings.Join([]string{properties["ID_VENDOR"], properties["ID_MODEL"]}, " "))
	}
	return displayName
}

func readUdevProperties(devicePath string) map[string]string {
	return readUdevDeviceData(devicePath).properties
}

func isKeyboardDevice(devicePath string) bool {
	properties := readUdevProperties(devicePath)
	return properties["ID_INPUT_KEYBOARD"] == "1" || properties["ID_INPUT_KEY"] == "1" && strings.Contains(strings.ToLower(properties["ID_MODEL"]), "keyboard")
}

func readTrimmed(name string) string {
	data, err := os.ReadFile(name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func BuildInventory(devices []Device) Inventory {
	result := Inventory{SchemaVersion: 1}
	for _, device := range devices {
		switch device.Type {
		case "serial":
			result.Serial = append(result.Serial, device)
		case "video":
			result.Video = append(result.Video, device)
		case "audio":
			result.Audio = append(result.Audio, device)
		case "hid":
			result.HID = append(result.HID, device)
		}
	}
	return result
}

func BluetoothCapable() bool {
	connection, err := net.DialTimeout("unix", "/run/dbus/system_bus_socket", time.Second)
	if err != nil {
		return false
	}
	_ = connection.Close()
	adapters, err := filepath.Glob("/sys/class/bluetooth/hci*")
	return err == nil && len(adapters) > 0
}
