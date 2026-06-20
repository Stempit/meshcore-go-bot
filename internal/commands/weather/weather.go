// Package weather answers .w [tomorrow] [city] using wttr.in.
package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"meshbot/internal/commands/registry"
	"meshbot/internal/proxy"
)

type plugin struct{}

func (plugin) Meta() registry.Meta {
	return registry.Meta{
		Name:      "weather",
		Triggers:  []string{".w"},
		Match:     registry.Prefix,
		HelpShort: "weather",
		HelpLong:  ".w [tomorrow] [city]\ncurrent or next-day weather",
	}
}

// Handle implements .w [завтра|tomorrow] [location].
func (plugin) Handle(c registry.Ctx) ([]string, error) {
	text := c.Arg(".w")

	day := 0
	textLower := strings.ToLower(text)
	for _, kw := range []string{"завтра", "tomorrow"} {
		if strings.HasPrefix(textLower, kw) {
			day = 1
			text = strings.TrimSpace(text[len(kw):])
			break
		}
	}

	location := strings.TrimSpace(text)
	if location == "" {
		location = conf.DefaultLocation
	}
	if location == "" {
		return []string{"погода: не указан город"}, nil
	}

	var (
		reply string
		err   error
	)
	if day == 0 {
		reply, err = fetchCurrentWeather(location, conf.Units)
	} else {
		reply, err = fetchTomorrowWeather(location, conf.Units)
	}
	if err != nil {
		log().Warn("weather fetch error", "error", err, "location", location)
		return []string{"погода: сервис недоступен"}, nil
	}

	log().Info("weather reply", "reply", reply, "bytes", len(reply))

	return []string{reply}, nil
}

func wttrGet(rawURL string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "curl/7.79.1")

	resp, err := proxy.HTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wttr.in status %d", resp.StatusCode)
	}

	return body, nil
}

type wttrCurrentCondition struct {
	TempC          string `json:"temp_C"`
	TempF          string `json:"temp_F"`
	WeatherCode    string `json:"weatherCode"`
	WindspeedKmph  string `json:"windspeedKmph"`
	WindspeedMiles string `json:"windspeedMiles"`
	Winddir16Point string `json:"winddir16Point"`
}

type wttrHourly struct {
	WeatherCode    string `json:"weatherCode"`
	WindspeedKmph  string `json:"windspeedKmph"`
	WindspeedMiles string `json:"windspeedMiles"`
	Winddir16Point string `json:"winddir16Point"`
}

type wttrDay struct {
	AvgtempC    string       `json:"avgtempC"`
	AvgtempF    string       `json:"avgtempF"`
	WeatherCode string       `json:"weatherCode"`
	Hourly      []wttrHourly `json:"hourly"`
}

type wttrNearestArea struct {
	AreaName []struct {
		Value string `json:"value"`
	} `json:"areaName"`
}

type wttrResponse struct {
	CurrentCondition []wttrCurrentCondition `json:"current_condition"`
	NearestArea      []wttrNearestArea      `json:"nearest_area"`
	Weather          []wttrDay              `json:"weather"`
}

func wttrFetchJSON(location string) (*wttrResponse, []byte, error) {
	u := &url.URL{
		Scheme:   "https",
		Host:     "wttr.in",
		Path:     "/" + location,
		RawQuery: "format=j1",
	}

	body, err := wttrGet(u.String())
	if err != nil {
		return nil, nil, err
	}

	var data wttrResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, body, fmt.Errorf("parse json: %w", err)
	}

	return &data, body, nil
}

func fetchCurrentWeather(location, units string) (string, error) {
	data, body, err := wttrFetchJSON(location)
	if err != nil {
		return "", err
	}
	log().Debug("wttr.in current response", "body", string(body))

	if len(data.CurrentCondition) == 0 {
		return "", fmt.Errorf("no current_condition in response")
	}

	cur := data.CurrentCondition[0]
	emoji := weatherCodeEmoji(cur.WeatherCode)
	temp := formatTemp(cur.TempC, cur.TempF, units)
	cond := conditionText(cur.WeatherCode)
	wind := buildWindStr(cur.WindspeedKmph, cur.WindspeedMiles, cur.Winddir16Point, units)

	return fmt.Sprintf("%s %s: %s, %s, 💨 %s", emoji, location, temp, cond, wind), nil
}

func fetchTomorrowWeather(location, units string) (string, error) {
	data, body, err := wttrFetchJSON(location)
	if err != nil {
		return "", err
	}
	log().Debug("wttr.in tomorrow response", "body", string(body))

	if len(data.Weather) < 2 {
		return "", fmt.Errorf("weather array too short (%d elements)", len(data.Weather))
	}

	d := data.Weather[1]
	temp := formatTemp(d.AvgtempC, d.AvgtempF, units)

	code := d.WeatherCode
	if code == "" && len(d.Hourly) > 4 {
		code = d.Hourly[4].WeatherCode
	}
	emoji := weatherCodeEmoji(code)
	cond := conditionText(code)

	wind := ""
	if len(d.Hourly) > 4 {
		h := d.Hourly[4]
		wind = buildWindStr(h.WindspeedKmph, h.WindspeedMiles, h.Winddir16Point, units)
	}

	return fmt.Sprintf("%s %s: %s, %s, 💨 %s", emoji, location, temp, cond, wind), nil
}

func formatTemp(cVal, f, units string) string {
	val, unit := cVal, "°C"
	if units == "imperial" {
		val, unit = f, "°F"
	}
	t, _ := strconv.Atoi(val)
	if t >= 0 {
		return fmt.Sprintf("+%d%s", t, unit)
	}
	return fmt.Sprintf("%d%s", t, unit)
}

func buildWindStr(kmph, miles, dir16pt, units string) string {
	dir := compassRU(dir16pt)
	if units == "imperial" {
		return fmt.Sprintf("%smph %s", miles, dir)
	}
	kmphInt, _ := strconv.Atoi(kmph)
	ms := int(float64(kmphInt)/3.6 + 0.5)
	return fmt.Sprintf("%dм/с %s", ms, dir)
}

// conditionRU maps wttr.in weather codes to Russian condition descriptions.
// The wttr.in lang=ru parameter translates some conditions but has incomplete
// coverage (e.g. code 353 "Light rain shower" is not in their translation db),
// so we maintain a complete local table.
var conditionRU = map[int]string{
	113: "ясно",
	116: "переменная облачность",
	119: "облачно",
	122: "пасмурно",
	143: "дымка",
	176: "местами дождь",
	179: "местами снег",
	182: "местами мокрый снег",
	185: "местами ледяная морось",
	200: "местами гроза",
	227: "метель",
	230: "буран",
	248: "туман",
	260: "ледяной туман",
	263: "слабая морось",
	266: "морось",
	281: "ледяная морось",
	284: "сильная ледяная морось",
	293: "слабый дождь",
	296: "дождь",
	299: "умеренный дождь",
	302: "дождь",
	305: "сильный дождь",
	308: "очень сильный дождь",
	311: "ледяной дождь",
	314: "сильный ледяной дождь",
	317: "мокрый снег",
	320: "сильный мокрый снег",
	323: "слабый снегопад",
	326: "снег",
	329: "умеренный снегопад",
	332: "снегопад",
	335: "сильный снегопад",
	338: "очень сильный снегопад",
	350: "ледяная крупа",
	353: "ливень",
	356: "сильный ливень",
	359: "проливной дождь",
	362: "мокрый снег с дождём",
	365: "сильный мокрый снег с дождём",
	368: "снежный ливень",
	371: "сильный снежный ливень",
	374: "ледяной ливень",
	377: "сильный ледяной ливень",
	386: "гроза",
	389: "гроза с дождём",
	392: "снег с грозой",
	395: "сильный снег с грозой",
}

func conditionText(code string) string {
	cVal, _ := strconv.Atoi(code)
	if desc, ok := conditionRU[cVal]; ok {
		return desc
	}
	return ""
}

func weatherCodeEmoji(code string) string {
	cVal, _ := strconv.Atoi(code)
	switch cVal {
	case 113:
		return "☀️"
	case 116:
		return "⛅"
	case 119, 122:
		return "☁️"
	case 143, 248, 260:
		return "🌫️"
	case 176, 263, 266, 293, 296, 299, 302, 305, 308, 353, 356, 359:
		return "🌧️"
	case 179, 182, 185, 281, 284, 311, 314, 317, 320, 362, 365, 374, 377:
		return "🌨️"
	case 200, 386, 389, 392, 395:
		return "⛈️"
	case 227, 230, 323, 326, 329, 332, 335, 338, 350, 368, 371:
		return "❄️"
	default:
		return "🌡️"
	}
}

var compassToRU = map[string]string{
	"N":   "С",
	"NNE": "ССВ",
	"NE":  "СВ",
	"ENE": "ВСВ",
	"E":   "В",
	"ESE": "ВЮВ",
	"SE":  "ЮВ",
	"SSE": "ЮЮВ",
	"S":   "Ю",
	"SSW": "ЮЮЗ",
	"SW":  "ЮЗ",
	"WSW": "ЗЮЗ",
	"W":   "З",
	"WNW": "ЗСЗ",
	"NW":  "СЗ",
	"NNW": "ССЗ",
}

func compassRU(dir string) string {
	if ru, ok := compassToRU[dir]; ok {
		return ru
	}
	return dir
}
