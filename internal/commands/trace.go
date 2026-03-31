package commands

import (
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"
)

// haversineKm returns the great-circle distance in km between two lat/lon points.
func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	return r * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func formatTechPong(meta *PingMeta) string {
	var sb strings.Builder

	// 0xFF means direct delivery (no repeaters)
	if meta.PathLength == 0xFF {
		sb.WriteString("Pong direct")
	} else {
		fmt.Fprintf(&sb, "Pong %d hops", meta.PathLength)
		if len(meta.PathBytes) > 0 && meta.PathLength > 0 {
			totalBytes := len(meta.PathBytes)
			hops := meta.PathLength
			// detect 1-byte or 2-byte per hop
			var bytesPerHop int
			if totalBytes == hops {
				bytesPerHop = 1
			} else if totalBytes == hops*2 {
				bytesPerHop = 2
			}
			if bytesPerHop > 0 {
				sb.WriteString(": ")
				for i := 0; i < hops; i++ {
					chunk := meta.PathBytes[i*bytesPerHop : (i+1)*bytesPerHop]
					fmt.Fprintf(&sb, "[%s]", strings.ToUpper(hex.EncodeToString(chunk)))
				}
			}
		}
	}

	if !meta.Timestamp.IsZero() {
		pingTime := time.Since(meta.Timestamp)
		if pingTime > 0 && pingTime < 24*time.Hour {
			fmt.Fprintf(&sb, " (%.0fs)", pingTime.Seconds())
		}
	}

	selfKnown := meta.SelfLat != 0 || meta.SelfLon != 0
	senderKnown := meta.SenderLat != nil && meta.SenderLon != nil

	if selfKnown && senderKnown {
		directKm := haversineKm(meta.SelfLat, meta.SelfLon, *meta.SenderLat, *meta.SenderLon)

		// Try to compute total path distance if all intermediate hops have known positions.
		allHopsKnown := len(meta.HopKnown) == len(meta.HopCoords) && len(meta.HopCoords) == meta.PathLength
		if allHopsKnown {
			for _, known := range meta.HopKnown {
				if !known {
					allHopsKnown = false
					break
				}
			}
		}

		if allHopsKnown && meta.PathLength > 0 {
			// Path direction: self → hops[0] → hops[1] → ... → sender
			totalKm := 0.0
			prevLat, prevLon := meta.SelfLat, meta.SelfLon
			for _, coord := range meta.HopCoords {
				totalKm += haversineKm(prevLat, prevLon, coord[0], coord[1])
				prevLat, prevLon = coord[0], coord[1]
			}
			totalKm += haversineKm(prevLat, prevLon, *meta.SenderLat, *meta.SenderLon)
			if directKm < 1 {
				fmt.Fprintf(&sb, " %.0fm->%.0fm", directKm*1000, totalKm*1000)
			} else {
				fmt.Fprintf(&sb, " %.1fkm->%.1fkm", directKm, totalKm)
			}
		} else {
			if directKm < 1 {
				fmt.Fprintf(&sb, " %.0fm", directKm*1000)
			} else {
				fmt.Fprintf(&sb, " %.1fkm", directKm)
			}
		}
	}

	return sb.String()
}

func ProcessTraceMessage(meta *PingMeta) (string, error) {
	return formatTechPong(meta), nil
}
