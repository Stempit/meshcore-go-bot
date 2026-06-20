// Package path answers .path with a network summary or the best route between
// two repeater nodes, computed from the Let's Mesh analyzer edge-stats data.
package path

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"meshbot/internal/commands/registry"
	"meshbot/internal/proxy"
)

type plugin struct{}

func (plugin) Meta() registry.Meta {
	return registry.Meta{
		Name:      "path",
		Triggers:  []string{".path"},
		Match:     registry.Prefix,
		HelpShort: "best path",
		HelpLong:  ".path — network summary\n.path <from> to <to> — best path\nex: .path TIM to CHEP",
	}
}

func (plugin) Handle(c registry.Ctx) ([]string, error) {
	return processPath(c.Trimmed)
}

// ── JSON models (mirrors Let's Mesh analyzer edge-stats API) ─────────────────

type statsNode struct {
	PublicKeyHex string  `json:"public_key_hex"`
	Name         string  `json:"name"`
	Lat          float64 `json:"lat"`
	Lon          float64 `json:"lon"`
	LastSeenAt   string  `json:"last_seen_at"`
	IsRepeater   bool    `json:"is_repeater"`
}

type statsEdge struct {
	NodeA     string   `json:"node_a"`
	NodeB     string   `json:"node_b"`
	LinkCount int      `json:"link_count"`
	RSSIMin   *float64 `json:"rssi_min"`
	RSSIAvg   *float64 `json:"rssi_avg"`
	RSSIMax   *float64 `json:"rssi_max"`
	SNRMin    *float64 `json:"snr_min"`
	SNRAvg    *float64 `json:"snr_avg"`
	SNRMax    *float64 `json:"snr_max"`
}

type statsResponse struct {
	Nodes []statsNode `json:"nodes"`
	Edges []statsEdge `json:"edges"`
}

// ── In-memory cache ───────────────────────────────────────────────────────────

var (
	statsMu     sync.Mutex
	statsCache  *statsResponse
	statsExpiry time.Time
)

func getStats() (*statsResponse, error) {
	statsMu.Lock()
	defer statsMu.Unlock()

	if statsCache != nil && time.Now().Before(statsExpiry) {
		return statsCache, nil
	}

	if conf.URL == "" {
		return nil, fmt.Errorf("Stats.url not configured")
	}

	var raw []byte
	var err error
	if strings.HasPrefix(conf.URL, "file://") {
		raw, err = os.ReadFile(strings.TrimPrefix(conf.URL, "file://"))
	} else {
		raw, err = statsHTTPGet(conf.URL)
	}
	if err != nil {
		return nil, fmt.Errorf("fetch stats: %w", err)
	}

	var resp statsResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse stats: %w", err)
	}

	ttl := time.Duration(conf.CacheTTLSec) * time.Second
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	statsCache = &resp
	statsExpiry = time.Now().Add(ttl)
	log().Info("stats cache refreshed", "nodes", len(resp.Nodes), "edges", len(resp.Edges))
	return &resp, nil
}

func statsHTTPGet(rawURL string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := proxy.HTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// ── Link cost model ───────────────────────────────────────────────────────────
//
// Goal: maximise delivery probability on the chosen path.
//
// Each edge is scored with a "cost" where lower = more reliable. We combine
// three signals, all of which matter for LoRa delivery:
//
//  1. SNR (dB) — primary signal quality indicator. We model per-packet success
//     probability as a sigmoid around the LoRa demodulation floor (~−5 dB):
//       p_snr = 1 / (1 + exp(−(snr+5)/3))
//     SNR  12 → p≈0.998; SNR 0 → p≈0.731; SNR −5 → p=0.5; SNR −12 → p≈0.085
//
//  2. RSSI (dBm) — complements SNR; deep negative RSSI predicts frame loss
//     even when SNR looks borderline. We map to p_rssi via:
//       p_rssi = 1 / (1 + exp(−(rssi+110)/8))
//     RSSI −80 → p≈0.997; RSSI −100 → p≈0.775; RSSI −110 → p=0.5; RSSI −120 → p≈0.269
//
//  3. link_count — observed traffic volume. A link with 500 observations is
//     empirically more reliable than one seen 5 times. We use a log saturation:
//       p_count = 1 − 1/(1 + log2(1 + count))
//     count 1 → p≈0.5; count 20 → p≈0.82; count 100 → p≈0.88; count 500 → p≈0.91
//
// When RSSI/SNR are present we weight them 70 % and link_count 30 %.
// When only link_count is available we use it alone.
//
// Final cost = −log(combined_p), so Dijkstra accumulates log-probabilities
// (path cost = −log P(all hops delivered)).

func linkCost(e statsEdge) float64 {
	hasSNR := e.SNRAvg != nil
	hasRSSI := e.RSSIAvg != nil

	var p float64

	if hasSNR || hasRSSI {
		var pSig float64 = 0.5 // default when one of the two is missing
		if hasSNR {
			snr := *e.SNRAvg
			pSig = 1.0 / (1.0 + math.Exp(-(snr+5)/3.0))
		}

		var pRSSI float64 = 0.5
		if hasRSSI {
			rssi := *e.RSSIAvg
			pRSSI = 1.0 / (1.0 + math.Exp(-(rssi+110)/8.0))
		}

		// combined radio probability (geometric mean of the two signals)
		pRadio := math.Sqrt(pSig * pRSSI)

		// activity confidence (log-saturating)
		pCount := 1.0 - 1.0/(1.0+math.Log2(1.0+float64(e.LinkCount)))

		// 70 % radio, 30 % activity
		p = 0.70*pRadio + 0.30*pCount
	} else {
		// No RF measurements — rely purely on observed traffic.
		p = 1.0 - 1.0/(1.0+math.Log2(1.0+float64(e.LinkCount)))
	}

	// Guard against p=0 to avoid −log(0)=+∞.
	if p < 1e-9 {
		p = 1e-9
	}
	return -math.Log(p)
}

// ── Dijkstra ──────────────────────────────────────────────────────────────────

type adjEntry struct {
	toKey string
	cost  float64
	edge  statsEdge
}

// findBestPath runs Dijkstra and returns ordered node names, the corresponding
// full public-key hex strings, and per-hop edges.
// Only repeater nodes participate: non-repeaters are excluded from the graph
// because they cannot forward packets.
// Returns (nil, nil, nil, false) when no path exists.
func findBestPath(stats *statsResponse, srcKey, dstKey string) (names, pubkeys []string, edges []statsEdge, ok bool) {
	// Index only repeater nodes.
	repeaters := make(map[string]bool, len(stats.Nodes))
	nameOf := make(map[string]string, len(stats.Nodes))
	for _, n := range stats.Nodes {
		if n.IsRepeater {
			repeaters[n.PublicKeyHex] = true
			nameOf[n.PublicKeyHex] = n.Name
		}
	}

	// Build adjacency from edges where both endpoints are repeaters.
	adj := make(map[string][]adjEntry, len(repeaters))
	for _, e := range stats.Edges {
		if !repeaters[e.NodeA] || !repeaters[e.NodeB] {
			continue
		}
		c := linkCost(e)
		adj[e.NodeA] = append(adj[e.NodeA], adjEntry{e.NodeB, c, e})
		adj[e.NodeB] = append(adj[e.NodeB], adjEntry{e.NodeA, c, e})
	}

	const inf = math.MaxFloat64
	dist := make(map[string]float64, len(repeaters))
	prevKey := make(map[string]string, len(repeaters))
	prevEdge := make(map[string]statsEdge, len(repeaters))
	visited := make(map[string]bool, len(repeaters))

	for k := range repeaters {
		dist[k] = inf
	}
	dist[srcKey] = 0

	for {
		// O(n) min-scan — node counts here are small (dozens, not millions)
		u := ""
		for k := range repeaters {
			if !visited[k] && (u == "" || dist[k] < dist[u]) {
				u = k
			}
		}
		if u == "" || dist[u] == inf {
			break
		}
		if u == dstKey {
			break
		}
		visited[u] = true
		for _, nb := range adj[u] {
			if visited[nb.toKey] {
				continue
			}
			if alt := dist[u] + nb.cost; alt < dist[nb.toKey] {
				dist[nb.toKey] = alt
				prevKey[nb.toKey] = u
				prevEdge[nb.toKey] = nb.edge
			}
		}
	}

	if dist[dstKey] == inf {
		return nil, nil, nil, false
	}

	// Reconstruct path (back-track from dst → src, then reverse)
	var pathKeys []string
	for cur := dstKey; cur != ""; cur = prevKey[cur] {
		pathKeys = append([]string{cur}, pathKeys...)
	}

	names = make([]string, len(pathKeys))
	pubkeys = make([]string, len(pathKeys))
	for i, k := range pathKeys {
		names[i] = nameOf[k]
		pubkeys[i] = k
	}
	edges = make([]statsEdge, len(pathKeys)-1)
	for i := 1; i < len(pathKeys); i++ {
		edges[i-1] = prevEdge[pathKeys[i]]
	}
	return names, pubkeys, edges, true
}

// ── Node name resolution ──────────────────────────────────────────────────────

// isHexStr returns true when s is a non-empty string of hex characters only.
func isHexStr(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// resolveNode returns the public key and canonical name for query.
//
// Resolution order:
//  1. Exact name match (case-insensitive)
//  2. Public-key hex prefix (1–N bytes; query must be all hex digits, matched
//     case-insensitively against the start of the 64-char key hex)
//  3. Partial name substring match (case-insensitive)
//
// Returns (key, name, nil) on unambiguous match.
// Returns ("", "", candidates) when multiple matches exist.
// Returns ("", "", nil) when nothing found.
func resolveNode(stats *statsResponse, query string) (key, name string, ambiguous []string) {
	lower := strings.ToLower(query)

	// 1. Exact name match.
	for _, n := range stats.Nodes {
		if strings.ToLower(n.Name) == lower {
			return n.PublicKeyHex, n.Name, nil
		}
	}

	// 2. Public-key hex prefix match.
	if isHexStr(query) {
		upper := strings.ToUpper(query)
		var hexMatches []statsNode
		for _, n := range stats.Nodes {
			if strings.HasPrefix(n.PublicKeyHex, upper) {
				hexMatches = append(hexMatches, n)
			}
		}
		if len(hexMatches) == 1 {
			return hexMatches[0].PublicKeyHex, hexMatches[0].Name, nil
		}
		if len(hexMatches) > 1 {
			names := make([]string, len(hexMatches))
			for i, m := range hexMatches {
				names[i] = m.Name
			}
			return "", "", names
		}
	}

	// 3. Partial name substring match.
	var matches []statsNode
	for _, n := range stats.Nodes {
		if strings.Contains(strings.ToLower(n.Name), lower) {
			matches = append(matches, n)
		}
	}
	if len(matches) == 1 {
		return matches[0].PublicKeyHex, matches[0].Name, nil
	}
	if len(matches) > 1 {
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = m.Name
		}
		return "", "", names
	}
	return "", "", nil
}

// ── Quality summary ───────────────────────────────────────────────────────────

// pathQuality returns a short label describing the weakest link on the path.
func pathQuality(edges []statsEdge) string {
	minSNR := math.MaxFloat64
	hasSNR := false
	for _, e := range edges {
		if e.SNRAvg != nil {
			hasSNR = true
			if *e.SNRAvg < minSNR {
				minSNR = *e.SNRAvg
			}
		}
	}
	if !hasSNR {
		return "?"
	}
	switch {
	case minSNR >= 5:
		return "strong"
	case minSNR >= 0:
		return "ok"
	case minSNR >= -5:
		return "weak"
	default:
		return "marginal"
	}
}

// deliveryPct estimates the end-to-end delivery probability in percent.
func deliveryPct(edges []statsEdge) int {
	p := 1.0
	for _, e := range edges {
		// −linkCost reverses our −log(p) transform: p = exp(−cost)
		p *= math.Exp(-linkCost(e))
	}
	return int(math.Round(p * 100))
}

// ── Argument splitting ────────────────────────────────────────────────────────

// trySplits tries every cut-point in rawArgs and returns all (from, to) pairs
// where both parts resolve to an unambiguous node.
func trySplits(stats *statsResponse, rawArgs []string) [][2]string {
	var results [][2]string
	for i := 1; i < len(rawArgs); i++ {
		f := strings.Join(rawArgs[:i], " ")
		t := strings.Join(rawArgs[i:], " ")
		fKey, _, _ := resolveNode(stats, f)
		tKey, _, _ := resolveNode(stats, t)
		if fKey != "" && tKey != "" {
			results = append(results, [2]string{f, t})
		}
	}
	return results
}

// ── Command handler ───────────────────────────────────────────────────────────

// processPath handles:
//
//	.path                    — network summary
//	.path <from> to <to>     — best path; "to" is the explicit separator
//	.path <from> <to>        — both names may contain spaces; the unique split
//	                           where both parts match a node is chosen automatically
func processPath(text string) ([]string, error) {
	stats, err := getStats()
	if err != nil {
		log().Warn("path command: stats unavailable", "error", err)
		return []string{"path: stats not available"}, nil
	}

	// Strip prefix, preserve original case for name matching.
	rawArgs := strings.Fields(strings.TrimSpace(strings.TrimPrefix(text, ".path")))

	if len(rawArgs) == 0 {
		rCount, eCount := 0, 0
		repeaterSet := make(map[string]bool, len(stats.Nodes))
		for _, n := range stats.Nodes {
			if n.IsRepeater {
				rCount++
				repeaterSet[n.PublicKeyHex] = true
			}
		}
		for _, e := range stats.Edges {
			if repeaterSet[e.NodeA] && repeaterSet[e.NodeB] {
				eCount++
			}
		}
		return []string{fmt.Sprintf("net: %d repeaters, %d edges", rCount, eCount)}, nil
	}

	// Find "to" keyword used as explicit separator (e.g. ".path TIM to Repeater GRIN").
	var fromQuery, toQuery string
	toIdx := -1
	for i, w := range rawArgs {
		if strings.ToLower(w) == "to" && i > 0 {
			toIdx = i
			break
		}
	}

	if toIdx > 0 {
		// Explicit "to" separator — honour it directly.
		fromQuery = strings.Join(rawArgs[:toIdx], " ")
		toQuery = strings.Join(rawArgs[toIdx+1:], " ")
		if fromQuery == "" || toQuery == "" {
			return []string{"usage: .path <from> to <to>"}, nil
		}
	} else {
		// No explicit separator — try every cut-point and pick the unique match.
		splits := trySplits(stats, rawArgs)
		switch len(splits) {
		case 0:
			return []string{"usage: .path <from> to <to>"}, nil
		case 1:
			fromQuery, toQuery = splits[0][0], splits[0][1]
		default:
			opts := make([]string, len(splits))
			for i, s := range splits {
				opts[i] = s[0] + " to " + s[1]
			}
			return []string{"ambiguous, be explicit: " + strings.Join(opts, " / ")}, nil
		}
	}

	srcKey, srcName, srcAmb := resolveNode(stats, fromQuery)
	if srcKey == "" {
		if len(srcAmb) > 0 {
			return []string{fmt.Sprintf("ambiguous '%s': %s", fromQuery, strings.Join(srcAmb, ", "))}, nil
		}
		return []string{fmt.Sprintf("node not found: %s", fromQuery)}, nil
	}

	dstKey, dstName, dstAmb := resolveNode(stats, toQuery)
	if dstKey == "" {
		if len(dstAmb) > 0 {
			return []string{fmt.Sprintf("ambiguous '%s': %s", toQuery, strings.Join(dstAmb, ", "))}, nil
		}
		return []string{fmt.Sprintf("node not found: %s", toQuery)}, nil
	}

	if srcKey == dstKey {
		return []string{srcName + " is the same node"}, nil
	}

	// Verify both endpoints can participate in routing.
	for _, n := range stats.Nodes {
		if n.PublicKeyHex == srcKey && !n.IsRepeater {
			return []string{srcName + " is not a repeater"}, nil
		}
		if n.PublicKeyHex == dstKey && !n.IsRepeater {
			return []string{dstName + " is not a repeater"}, nil
		}
	}

	pathNodes, pubkeys, edges, ok := findBestPath(stats, srcKey, dstKey)
	if !ok {
		return []string{fmt.Sprintf("no path: %s→%s", srcName, dstName)}, nil
	}

	hops := len(pathNodes) - 1
	quality := pathQuality(edges)
	pct := deliveryPct(edges)
	route := strings.Join(pathNodes, "→")

	// Build [aa,bb,cc] hash list from the first byte (2 hex chars) of each key.
	hashes := make([]string, len(pubkeys))
	for i, pk := range pubkeys {
		hashes[i] = strings.ToLower(pk[:2])
	}
	hashList := "[" + strings.Join(hashes, ",") + "]"

	reply := fmt.Sprintf("%d hop(s) [%s ~%d%%]: %s %s", hops, quality, pct, route, hashList)

	// Abbreviate node names to first word when the line is too long for mesh.
	if len(reply) > 130 {
		short := make([]string, len(pathNodes))
		for i, nm := range pathNodes {
			parts := strings.Fields(nm)
			short[i] = parts[0]
		}
		route = strings.Join(short, "→")
		reply = fmt.Sprintf("%d hop(s) [%s ~%d%%]: %s %s", hops, quality, pct, route, hashList)
	}

	return []string{reply}, nil
}
