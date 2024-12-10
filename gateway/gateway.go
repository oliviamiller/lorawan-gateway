// Package gateway implements the sx1302 gateway module.
package gateway

/*
#cgo CFLAGS: -I${SRCDIR}/../sx1302/libloragw/inc -I${SRCDIR}/../sx1302/libtools/inc
#cgo LDFLAGS: -L${SRCDIR}/../sx1302/libloragw -lloragw -L${SRCDIR}/../sx1302/libtools -lbase64 -lparson -ltinymt32  -lm

#include "../sx1302/libloragw/inc/loragw_hal.h"
#include "gateway.h"
#include <stdlib.h>
*/
import "C"

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"gateway/gpio"
	"gateway/node"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.viam.com/rdk/components/sensor"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/resource"
	"go.viam.com/utils"
)

// Error variables for validation and operations
var (
	// Config validation errors
	errResetPinRequired = errors.New("reset pin is required")
	errInvalidSpiBus    = errors.New("spi bus can be 0 or 1 - default 0")

	// Gateway operation errors
	errUnexpectedJoinType = errors.New("unexpected join type when adding node to gateway")
	errInvalidNodeMapType = errors.New("expected node map val to be type []interface{}, but it wasn't")
	errInvalidByteType    = errors.New("expected node byte array val to be float64, but it wasn't")
	errNoDevice           = errors.New("received packet from unknown device")
	errInvalidMIC         = errors.New("invalid MIC")
	errSendJoinAccept     = errors.New("failed to send join accept packet")
)

// Model represents a lorawan gateway model.
var Model = resource.NewModel("viam", "lorawan", "sx1302-gateway")

// Config describes the configuration of the gateway
type Config struct {
	Bus      int  `json:"spi_bus,omitempty"`
	PowerPin *int `json:"power_en_pin,omitempty"`
	ResetPin *int `json:"reset_pin"`
}

func init() {
	resource.RegisterComponent(
		sensor.API,
		Model,
		resource.Registration[sensor.Sensor, *Config]{
			Constructor: NewGateway,
		})
}

// Validate ensures all parts of the config are valid.
func (conf *Config) Validate(path string) ([]string, error) {
	if conf.ResetPin == nil {
		return nil, resource.NewConfigValidationError(path, errResetPinRequired)
	}
	if conf.Bus != 0 && conf.Bus != 1 {
		return nil, resource.NewConfigValidationError(path, errInvalidSpiBus)
	}
	return nil, nil
}

// Gateway defines a lorawan gateway.
type gateway struct {
	resource.Named
	logger logging.Logger

	workers *utils.StoppableWorkers
	mu      sync.Mutex

	lastReadings map[string]interface{} // map of devices to readings
	readingsMu   sync.Mutex

	devices map[string]*node.Node // map of node name to node struct

	started  bool
	bookworm *bool
	rstPin   string
}

// LoggingRoutineStarted is a global variable to track if the captureCOutputToLogs goroutine has
// started for each gateway. If the gateway build errors and needs to build again, we only want to start
// the logging routine once.
var LoggingRoutineStarted = make(map[string]bool)

// NewGateway creates a new gateway
func NewGateway(
	ctx context.Context,
	deps resource.Dependencies,
	conf resource.Config,
	logger logging.Logger,
) (sensor.Sensor, error) {
	g := &gateway{
		Named:   conf.ResourceName().AsNamed(),
		logger:  logger,
		started: false,
	}

	err := g.Reconfigure(ctx, deps, conf)
	if err != nil {
		return nil, err
	}

	return g, nil
}

// Reconfigure reconfigures the gateway.
func (g *gateway) Reconfigure(ctx context.Context, deps resource.Dependencies, conf resource.Config) error {
	cfg, err := resource.NativeConfig[*Config](conf)
	if err != nil {
		return err
	}
	// Determine if the pi is on bookworm or bullseye
	osRelease, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return fmt.Errorf("cannot determine os release: %w", err)
	}
	isBookworm := strings.Contains(string(osRelease), "bookworm")

	g.bookworm = &isBookworm
	g.rstPin = strconv.Itoa(*cfg.ResetPin)

	// If the gateway hardware was already started, stop gateway and the background worker.
	// Make sure to always call stopGateway() before making any changes to the c config or
	// errors will occur.
	// Unexpected behavior will also occur if you call stopGateway() when the gateway hasn't been
	// started, so only call stopGateway if this module already started the gateway.
	if g.started {
		err := g.Close(ctx)
		if err != nil {
			return err
		}
		g.started = false
	}

	// capture C log output
	loggingStarted, ok := LoggingRoutineStarted[g.Name().Name]
	if !ok || !loggingStarted {
		g.workers = utils.NewBackgroundStoppableWorkers(g.captureCOutputToLogs)
		LoggingRoutineStarted[g.Name().Name] = true
	}

	// maintain devices and lastReadings through reconfigure.
	if g.devices == nil {
		g.devices = make(map[string]*node.Node)
	}

	if g.lastReadings == nil {
		g.lastReadings = make(map[string]interface{})
	}

	err = gpio.InitGateway(cfg.ResetPin, cfg.PowerPin, isBookworm)
	if err != nil {
		return fmt.Errorf("error initializing the gateway: %w", err)
	}

	errCode := C.setUpGateway(C.int(cfg.Bus))
	if errCode != 0 {
		strErr := parseErrorCode(int(errCode))
		return fmt.Errorf("failed to set up the gateway: %s", strErr)
	}

	g.started = true
	g.workers.Add(g.receivePackets)

	return nil
}

func parseErrorCode(errCode int) string {
	switch errCode {
	case 1:
		return "error setting the board config"
	case 2:
		return "error setting the radio frequency config for radio 0"
	case 3:
		return "error setting the radio frequency config for radio 1"
	case 4:
		return "error setting the intermediate frequency chain config"
	case 5:
		return "error starting the gateway"
	default:
		return "unknown error"
	}
}

// captureOutput is a background routine to capture C's stdout and log to the module's logger.
// This is necessary because the sx1302 library only uses printf to report errors.
func (g *gateway) captureCOutputToLogs(ctx context.Context) {
	// Need to disable buffering on stdout so C logs can be displayed in real time.
	C.disableBuffering()

	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		g.logger.Errorf("unable to create pipe for C logs")
		return
	}
	// Redirect C's stdout to the write end of the pipe
	C.redirectToPipe(C.int(stdoutW.Fd()))
	scanner := bufio.NewScanner(stdoutR)

	//nolint:errcheck
	defer stdoutR.Close()
	//nolint:errcheck
	defer stdoutW.Close()

	// loop to read lines from the scanner and log them
	for {
		select {
		case <-ctx.Done():
			return
		default:
			if scanner.Scan() {
				line := scanner.Text()
				if strings.Contains(line, "ERROR") {
					g.logger.Error(line)
				} else {
					g.logger.Debug(line)
				}
			}
		}
	}
}

func (g *gateway) receivePackets(ctx context.Context) {
	// receive the radio packets
	packet := C.createRxPacketArray()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		numPackets := int(C.receive(packet))
		switch numPackets {
		case 0:
			// no packet received, wait 10 ms to receive again.
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		case 1:
			// received a LORA packet
			var payload []byte
			if packet.size == 0 {
				continue
			}
			// Convert packet to go byte array
			for i := range int(packet.size) {
				payload = append(payload, byte(packet.payload[i]))
			}
			g.handlePacket(ctx, payload)
		default:
			g.logger.Errorf("error receiving lora packet")
		}
	}
}

func (g *gateway) handlePacket(ctx context.Context, payload []byte) {
	g.workers.Add(func(ctx context.Context) {
		// first byte is MHDR - specifies message type
		switch payload[0] {
		case 0x0:
			g.logger.Infof("received join request")
			err := g.handleJoin(ctx, payload)
			if err != nil {
				// don't log as error if it was a request from unknown device.
				if errors.Is(errNoDevice, err) {
					return
				}
				g.logger.Errorf("couldn't handle join request: %s", err)
			}
		case 0x40:
			g.logger.Infof("received data uplink")
			name, readings, err := g.parseDataUplink(ctx, payload)
			if err != nil {
				// don't log as error if it was a request from unknown device.
				if errors.Is(errNoDevice, err) {
					return
				}
				g.logger.Errorf("error parsing uplink message: %s", err)
				return
			}
			g.updateReadings(name, readings)
		default:
			g.logger.Warnf("received unsupported packet type")
		}
	})
}

func (g *gateway) updateReadings(name string, newReadings map[string]interface{}) {
	g.readingsMu.Lock()
	defer g.readingsMu.Unlock()
	readings, ok := g.lastReadings[name].(map[string]interface{})
	if !ok {
		// readings for this device does not exist yet
		g.lastReadings[name] = newReadings
		return
	}

	if readings == nil {
		g.lastReadings[name] = make(map[string]interface{})
	}
	for key, val := range newReadings {
		readings[key] = val
	}

	g.lastReadings[name] = readings
}

// DoCommand validates that the dependency is a gateway, and adds and removes nodes from the device maps.
func (g *gateway) DoCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	// Validate that the dependency is correct.
	if _, ok := cmd["validate"]; ok {
		return map[string]interface{}{"validate": 1}, nil
	}
	// Add the nodes to the list of devices.
	if newNode, ok := cmd["register_device"]; ok {
		if newN, ok := newNode.(map[string]interface{}); ok {
			node, err := convertToNode(newN)
			if err != nil {
				return nil, err
			}

			oldNode, exists := g.devices[node.NodeName]
			if !exists {
				g.devices[node.NodeName] = node
				return map[string]interface{}{}, nil
			}
			// node with that name already exists, merge them
			mergedNode, err := mergeNodes(node, oldNode)
			if err != nil {
				return nil, err
			}
			g.devices[node.NodeName] = mergedNode
		}
	}
	// Remove a node from the device map and readings map.
	if name, ok := cmd["remove_device"]; ok {
		if n, ok := name.(string); ok {
			delete(g.devices, n)
			g.readingsMu.Lock()
			delete(g.lastReadings, n)
			g.readingsMu.Unlock()
		}
	}

	return map[string]interface{}{}, nil
}

// mergeNodes merge the fields from the oldNode and the newNode sent from reconfigure.
func mergeNodes(newNode, oldNode *node.Node) (*node.Node, error) {
	mergedNode := &node.Node{}
	mergedNode.DecoderPath = newNode.DecoderPath
	mergedNode.NodeName = newNode.NodeName
	mergedNode.JoinType = newNode.JoinType

	switch mergedNode.JoinType {
	case "OTAA":
		// if join type is OTAA - keep the appSKey, dev addr from the old node.
		// These fields were determined by the gateway if the join procedure was done.
		mergedNode.Addr = oldNode.Addr
		mergedNode.AppSKey = oldNode.AppSKey
		// The appkey and deveui are obtained by the config in OTAA,
		// if these were changed during reconfigure the join procedure needs to be redone
		mergedNode.AppKey = newNode.AppKey
		mergedNode.DevEui = newNode.DevEui
	case "ABP":
		// if join type is ABP get the new appSKey and addr from the new config.
		// Don't need appkey and DevEui for ABP.
		mergedNode.Addr = newNode.Addr
		mergedNode.AppSKey = newNode.AppSKey
	default:
		return nil, errUnexpectedJoinType
	}

	return mergedNode, nil
}

// convertToNode converts the map from the docommand into the node struct.
func convertToNode(mapNode map[string]interface{}) (*node.Node, error) {
	node := &node.Node{DecoderPath: mapNode["DecoderPath"].(string)}

	var err error
	node.AppKey, err = convertToBytes(mapNode["AppKey"])
	if err != nil {
		return nil, err
	}
	node.AppSKey, err = convertToBytes(mapNode["AppSKey"])
	if err != nil {
		return nil, err
	}
	node.DevEui, err = convertToBytes(mapNode["DevEui"])
	if err != nil {
		return nil, err
	}
	node.Addr, err = convertToBytes(mapNode["Addr"])
	if err != nil {
		return nil, err
	}

	node.NodeName = mapNode["NodeName"].(string)
	node.JoinType = mapNode["JoinType"].(string)

	return node, nil
}

// convertToBytes converts the interface{} field from the docommand map into a byte array.
func convertToBytes(key interface{}) ([]byte, error) {
	bytes, ok := key.([]interface{})
	if !ok {
		return nil, errInvalidNodeMapType
	}
	res := make([]byte, 0)

	if len(bytes) > 0 {
		for _, b := range bytes {
			val, ok := b.(float64)
			if !ok {
				return nil, errInvalidByteType
			}
			res = append(res, byte(val))
		}
	}

	return res, nil
}

// Close closes the gateway.
func (g *gateway) Close(ctx context.Context) error {
	if g.rstPin != "" && g.bookworm != nil {
		err := gpio.ResetGPIO(g.rstPin, *g.bookworm)
		if err != nil {
			g.logger.Error("error reseting gateway")
		}
	}
	if g.workers != nil {
		g.workers.Stop()
		LoggingRoutineStarted[g.Name().Name] = false
	}
	errCode := C.stopGateway()
	if errCode != 0 {
		g.logger.Error("error stopping gateway")
	}
	return nil
}

// Readings returns all the node's readings.
func (g *gateway) Readings(ctx context.Context, extra map[string]interface{}) (map[string]interface{}, error) {
	g.readingsMu.Lock()
	defer g.readingsMu.Unlock()
	return g.lastReadings, nil
}
