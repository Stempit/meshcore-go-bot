// Status publishing: the retained device-status document (radio settings,
// stats) and its serialization.
package mqttbridge

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"time"
)

// ============================================================================
// Status Publishing
// ============================================================================

// publishStatusIfNeeded publishes status message if interval has passed
func (b *MQTTBridge) publishStatusIfNeeded() {
	if !b.config.StatusEnabled {
		b.logger.Debug("Status publish skipped: StatusEnabled is false")
		return
	}

	// Check if any broker is connected
	if !b.isAnyConnected() {
		b.logger.Debug("Status publish skipped: no broker connected",
			"custom", b.brokers["custom"].connected.Load(),
			"analyzer_us", b.brokers["us"].connected.Load(),
			"analyzer_eu", b.brokers["eu"].connected.Load())
		return
	}

	now := time.Now()
	elapsed := time.Duration(0)
	if !b.lastStatusPublish.IsZero() {
		elapsed = now.Sub(b.lastStatusPublish)
	}

	if !b.lastStatusPublish.IsZero() && elapsed < b.statusInterval {
		b.logger.Debug("Status publish skipped: interval not reached",
			"elapsed", elapsed, "interval", b.statusInterval)
		return
	}

	b.logger.Debug("Status publish triggered",
		"last_publish", b.lastStatusPublish,
		"elapsed", elapsed,
		"interval", b.statusInterval,
		"custom_connected", b.brokers["custom"].connected.Load())
	b.publishStatus()
}

// publishStatus publishes a status message to all connected brokers.
// The message is marked as retained so newly connected subscribers receive
// the latest state immediately.
func (b *MQTTBridge) publishStatus() {
	b.mu.RLock()
	deviceInfo := b.deviceInfo
	stats := b.stats
	b.mu.RUnlock()

	if deviceInfo == nil {
		b.logger.Warn("Cannot publish status: no device info")
		return
	}

	originID := hex.EncodeToString(deviceInfo.PublicKey)
	origin := deviceInfo.Name
	if origin == "" {
		origin = "Unknown"
	}

	// Build status message with all available device parameters.
	model := deviceInfo.Model
	if model == "" {
		model = "MeshCore"
	}
	fwVersion := deviceInfo.FirmwareVersion
	if fwVersion == "" {
		fwVersion = "unknown"
	}
	mqttStatus := &MQTTStatus{
		Status:          "online",
		Timestamp:       time.Now().UTC().Format("2006-01-02T15:04:05.000000"),
		Origin:          origin,
		OriginID:        originID,
		Model:           model,
		FirmwareVersion: fwVersion,
		Radio:           buildRadioString(deviceInfo),
		ClientVersion:   "meshcore/" + fwVersion,
	}

	// GPS coordinates — include only when the device has a valid fix.
	// In MeshCore, (0,0) means "no GPS" (it is in the ocean and unreachable by LoRa).
	if deviceInfo.Latitude != 0 || deviceInfo.Longitude != 0 {
		mqttStatus.Lat = deviceInfo.Latitude
		mqttStatus.Lon = deviceInfo.Longitude
	}

	// TX power — include when non-zero (0 dBm is an invalid TX power for any
	// real LoRa device; the field is simply unset in that case).
	if deviceInfo.TxPower != 0 {
		mqttStatus.TxPower = int(deviceInfo.TxPower)
	}

	// Comprehensive stats — all counters from the last CMD_GET_STATS response.
	if stats != nil {
		mqttStatus.Stats = buildMQTTStats(stats)
	}

	// Build topic and serialize.
	iata := b.config.IATA
	if iata == "" {
		iata = "XXX"
	}

	topic := fmt.Sprintf(topicPattern, iata, originID, "status")
	jsonData, err := json.Marshal(mqttStatus)
	if err != nil {
		b.logger.Error("Failed to marshal status", "error", err)
		return
	}

	// Publish to custom broker and to the analyzer servers, all retained so newly
	// connected subscribers immediately receive the latest state.
	b.publishCustomOne(topic, jsonData, true)
	b.publishAnalyzers(topic, jsonData, true)

	b.lastStatusPublish = time.Now()
	b.logger.Debug("Status published", "topic", topic,
		"lat", mqttStatus.Lat, "lon", mqttStatus.Lon,
		"tx_power", mqttStatus.TxPower,
		"has_stats", mqttStatus.Stats != nil)
}

// buildRadioString constructs the radio-config string from DeviceInfo.
//
// Format expected by Let's Mesh Analyzer: "{freq_khz},{bw_khz},{sf},{cr}"
//   - freq_khz — carrier frequency in kHz  (e.g. 869525.000)
//   - bw_khz   — channel bandwidth in kHz  (e.g. 62.5, 125.0, 250.0, 500.0)
//   - sf        — LoRa spreading factor     (7–12)
//   - cr        — LoRa coding rate          (5=4/5 … 8=4/8)
func buildRadioString(info *DeviceInfo) string {
	if info == nil || info.FreqHz == 0 {
		return ""
	}
	freqKHz := float64(info.FreqHz) / 1e3
	bwKHz := float64(info.BwHz) / 1e3
	return fmt.Sprintf("%.3f,%.1f,%d,%d", freqKHz, bwKHz, info.Sf, info.Cr)
}

// buildMQTTStats maps all fields from DeviceStats → MQTTStats.
//
// Airtime utilization is derived as:
//
//	(TxAirSecs + RxAirSecs) / UptimeSecs × 100   [rounded to 2 decimal places]
//
// Fields whose source value is naturally zero when not-yet-populated (e.g.
// BatteryMv=0 means no battery telemetry) are left at their Go zero value and
// therefore omitted from the JSON output by the `omitempty` tag.
func buildMQTTStats(stats *DeviceStats) *MQTTStats {
	if stats == nil {
		return nil
	}

	s := &MQTTStats{
		// Core
		BatteryMV:  int(stats.BatteryMv),
		UptimeSecs: int(stats.UptimeSecs),
		Errors:     int(stats.Errors),
		QueueLen:   int(stats.QueueLen),

		// Radio channel
		NoiseFloor: int(stats.NoiseFloorDbm),
		TxAirSecs:  int(stats.TxAirSecs),
		RxAirSecs:  int(stats.RxAirSecs),
		LastRssi:   int(stats.LastRssi),
		LastSnr:    math.Round(float64(stats.LastSnrDb)*100) / 100, // 2 dp

		// Packet counters
		PacketsRecv:     int(stats.PacketsRecv),
		PacketsSent:     int(stats.PacketsSent),
		PacketsFloodTx:  int(stats.PacketsFloodTx),
		PacketsDirectTx: int(stats.PacketsDirectTx),
		PacketsFloodRx:  int(stats.PacketsFloodRx),
		PacketsDirectRx: int(stats.PacketsDirectRx),
		RecvErrors:      int(stats.PacketsRecvErrors),
	}

	// Airtime utilization percentage — only meaningful once the device has been
	// up for at least one second and has logged some air time.
	if stats.UptimeSecs > 0 {
		airSecs := float64(stats.TxAirSecs + stats.RxAirSecs)
		s.AirtimeUtilPct = math.Round(airSecs/float64(stats.UptimeSecs)*10000) / 100
	}

	return s
}
