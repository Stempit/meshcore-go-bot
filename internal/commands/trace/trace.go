// Package trace answers .trace with hop count and distance from the last ping.
package trace

import (
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"

	"meshbot/internal/support/registry"
)

func init() { registry.Register(plugin{}) }

type plugin struct{}

func (plugin) Meta() registry.Meta {
	return registry.Meta{
		Name:      "trace",
		Triggers:  []string{".trace"},
		Match:     registry.Exact,
		HelpShort: "hop count & distance",
		HelpLong:  ".trace — hop count & distance from last ping",
		Group:     "Service",
	}
}

func (plugin) Handle(c registry.Ctx) ([]string, error) {
	return []string{formatTechPong(c.PingMeta)}, nil
}

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

func formatTechPong(meta *registry.PingMeta) string {
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

	// If sender coordinates are unknown, fall back to the first repeater's coordinates.
	// In that case we append "+" to the distance unit to signal it's a minimum estimate.
	var endLat, endLon float64
	approx := false
	if senderKnown {
		endLat, endLon = *meta.SenderLat, *meta.SenderLon
	} else if len(meta.HopCoords) > 0 && len(meta.HopKnown) > 0 && meta.HopKnown[0] {
		endLat, endLon = meta.HopCoords[0][0], meta.HopCoords[0][1]
		approx = true
	}

	if selfKnown && (senderKnown || approx) {
		directKm := haversineKm(meta.SelfLat, meta.SelfLon, endLat, endLon)
		unit := "km"
		unitM := "m"
		if approx {
			unit = "km+"
			unitM = "m+"
		}

		// Try to compute total path distance if all intermediate hops have known positions.
		allHopsKnown := senderKnown && len(meta.HopKnown) == len(meta.HopCoords) && len(meta.HopCoords) == meta.PathLength
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
				fmt.Fprintf(&sb, " %.0f%s->%.0f%s", directKm*1000, unitM, totalKm*1000, unitM)
			} else {
				fmt.Fprintf(&sb, " %.1f%s->%.1f%s", directKm, unit, totalKm, unit)
			}
		} else {
			if directKm < 1 {
				fmt.Fprintf(&sb, " %.0f%s", directKm*1000, unitM)
			} else {
				fmt.Fprintf(&sb, " %.1f%s", directKm, unit)
			}
		}
	}

	return sb.String()
}
