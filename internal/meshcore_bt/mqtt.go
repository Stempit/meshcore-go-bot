package meshcore_bt

import (
	"fmt"
	"meshbot/internal/mqttbridge"
)

// ============================================================================
// MQTT Integration
// ============================================================================

// MQTTBridge returns the MQTT bridge instance (nil if not configured).
func (c *MeshCoreClient) MQTTBridge() *mqttbridge.MQTTBridge {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.mqtt
}

// SetContactLookupFunc sets a function to lookup contact names by public key.
// This is used by the MQTT bridge to provide meaningful sender names in messages.
func (c *MeshCoreClient) SetContactLookupFunc(f func(pubKey []byte) string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.contactLookupFunc = f
}

// toDeviceInfo converts SelfInfo to mqttbridge.DeviceInfo.
func toDeviceInfo(info *SelfInfo) *mqttbridge.DeviceInfo {
	if info == nil {
		return nil
	}
	return &mqttbridge.DeviceInfo{
		PublicKey:       info.PublicKey,
		Name:            info.Name,
		TxPower:         info.TxPower,
		Latitude:        info.Latitude,
		Longitude:       info.Longitude,
		FreqHz:          info.FreqHz,
		BwHz:            info.BwHz,
		Sf:              info.Sf,
		Cr:              info.Cr,
		FirmwareVersion: info.FirmwareVersion,
		Model:           info.Model,
	}
}

// toDeviceStats converts CompanionRadioStats to mqttbridge.DeviceStats.
func toDeviceStats(stats *CompanionRadioStats) *mqttbridge.DeviceStats {
	if stats == nil {
		return nil
	}
	return &mqttbridge.DeviceStats{
		BatteryMv:         stats.BatteryMv,
		UptimeSecs:        stats.UptimeSecs,
		Errors:            stats.Errors,
		QueueLen:          stats.QueueLen,
		TxAirSecs:         stats.TxAirSecs,
		RxAirSecs:         stats.RxAirSecs,
		NoiseFloorDbm:     stats.NoiseFloorDbm,
		LastRssi:          stats.LastRssi,
		LastSnrDb:         stats.LastSnrDb,
		PacketsRecv:       stats.PacketsRecv,
		PacketsSent:       stats.PacketsSent,
		PacketsFloodTx:    stats.PacketsFloodTx,
		PacketsDirectTx:   stats.PacketsDirectTx,
		PacketsFloodRx:    stats.PacketsFloodRx,
		PacketsDirectRx:   stats.PacketsDirectRx,
		PacketsRecvErrors: stats.PacketsRecvErrors,
	}
}

// SetMQTTConfig configures and starts the MQTT bridge.
// Returns the MQTTBridge instance or nil if configuration is invalid.
func (c *MeshCoreClient) SetMQTTConfig(config *mqttbridge.MQTTConfig) (*mqttbridge.MQTTBridge, error) {
	if config == nil {
		return nil, fmt.Errorf("mqtt config is required")
	}

	// CRITICAL: Stop existing MQTT bridge before creating new one
	// This prevents connection conflicts and stale state issues
	c.StopMQTT()

	// Create MQTT bridge
	bridge := mqttbridge.NewMQTTBridge(config)
	bridge.SetLogger(c.logger)

	// Set initial device info if available
	c.mu.RLock()
	if c.selfInfo != nil {
		bridge.SetDeviceInfo(toDeviceInfo(c.selfInfo))
	}
	c.mu.RUnlock()

	// Set MQTT callbacks to forward events
	bridge.SetCallbacks(mqttbridge.MQTTCallbacks{
		OnConnected: func(broker string) {
			c.logger.Info("MQTT connected", "broker", broker)
		},
		OnDisconnected: func(broker string) {
			c.logger.Info("MQTT disconnected", "broker", broker)
		},
		OnStatus: func(status *mqttbridge.MQTTStatus) {
			c.logger.Debug("MQTT status received", "status", status.Status)
		},
		OnPacket: func(packet *mqttbridge.MQTTPacket) {
			c.logger.Debug("MQTT packet received", "direction", packet.Direction)
		},
		OnRaw: func(raw *mqttbridge.MQTTRaw) {
			c.logger.Debug("MQTT raw received")
		},
	})

	// Start the bridge
	if err := bridge.Start(); err != nil {
		return nil, fmt.Errorf("failed to start MQTT bridge: %w", err)
	}

	// Update client reference
	c.mu.Lock()
	c.mqtt = bridge
	c.mu.Unlock()

	// When self info is received, update MQTT device info
	originalOnSelfInfo := c.onSelfInfo
	c.onSelfInfo = func(info *SelfInfo) {
		bridge.SetDeviceInfo(toDeviceInfo(info))
		if originalOnSelfInfo != nil {
			originalOnSelfInfo(info)
		}
	}

	// When stats are received, update MQTT device stats
	originalOnStats := c.onStats
	c.onStats = func(stats *CompanionRadioStats) {
		bridge.SetDeviceStats(toDeviceStats(stats))
		if originalOnStats != nil {
			originalOnStats(stats)
		}
	}

	return bridge, nil
}

// StopMQTT stops the MQTT bridge and cleans up.
func (c *MeshCoreClient) StopMQTT() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mqtt != nil {
		c.mqtt.Stop()
		c.mqtt = nil
	}
}

// MQTTStatus returns a formatted string describing MQTT connection state.
func (c *MeshCoreClient) MQTTStatus() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.mqtt == nil {
		return "mqtt: not configured"
	}
	return c.mqtt.FormatStatusReply()
}

// OnPacketReceived is called by the frame handler when a mesh packet is received.
// This forwards packets to MQTT for publishing if enabled.
func (c *MeshCoreClient) OnPacketReceived(rawData []byte, snr, rssi float64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.mqtt != nil {
		c.mqtt.OnPacketReceived(rawData, snr, rssi)
	}
}

// OnPacketSent is called when a mesh packet is transmitted.
// This forwards packets to MQTT for publishing if enabled.
func (c *MeshCoreClient) OnPacketSent(rawData []byte) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.mqtt != nil {
		c.mqtt.OnPacketSent(rawData)
	}
}

// MQTTConfig returns the current MQTT configuration.
func (c *MeshCoreClient) MQTTConfig() *mqttbridge.MQTTConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.mqtt == nil {
		return nil
	}
	return c.mqtt.GetConfig()
}

// UpdateMQTTConfig updates the MQTT configuration at runtime.
func (c *MeshCoreClient) UpdateMQTTConfig(config *mqttbridge.MQTTConfig) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.mqtt == nil {
		return fmt.Errorf("mqtt not configured")
	}
	c.mqtt.SetConfig(config)
	return nil
}

// GetMQTTConnectionState returns the connection state of all MQTT brokers.
func (c *MeshCoreClient) GetMQTTConnectionState() []mqttbridge.BrokerInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.mqtt == nil {
		return nil
	}
	return c.mqtt.GetConnectionState()
}
