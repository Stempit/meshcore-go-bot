package mqttbridge

import "meshbot/internal/meshcore"

// DeviceInfoFromMesh converts a meshcore SelfInfo into the bridge's DeviceInfo.
// Returns nil when info is nil.
func DeviceInfoFromMesh(info *meshcore.SelfInfo) *DeviceInfo {
	if info == nil {
		return nil
	}
	return &DeviceInfo{
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

// DeviceStatsFromMesh converts meshcore CompanionRadioStats into the bridge's
// DeviceStats. Returns nil when stats is nil.
func DeviceStatsFromMesh(stats *meshcore.CompanionRadioStats) *DeviceStats {
	if stats == nil {
		return nil
	}
	return &DeviceStats{
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
