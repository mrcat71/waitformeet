// Package weather fetches the current conditions in the two cities.
//
// It talks to Open-Meteo, which needs no API key and no account. The feature is off
// by default, and every failure is silent by design: a site about missing someone
// should not show an error because a weather service was slow.
package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	geocodeURL  = "https://geocoding-api.open-meteo.com/v1/search"
	forecastURL = "https://api.open-meteo.com/v1/forecast"

	// requestTimeout bounds one call. The page render waits on this, so it is
	// short: a missing weather line is much better than a slow page.
	requestTimeout = 3 * time.Second
	// cacheTTL is how long a reading is reused. Weather does not move fast, and
	// this keeps a busy page from hammering a free service.
	cacheTTL = 30 * time.Minute
	// coordinateTTL is far longer: cities do not move.
	coordinateTTL = 30 * 24 * time.Hour
)

// Conditions is the current weather somewhere.
type Conditions struct {
	// TemperatureC is degrees Celsius.
	TemperatureC float64
	// Code is the WMO weather code the icon is chosen from.
	Code int
	// Icon is a single emoji summarising the sky.
	Icon string
}

// Client fetches and caches weather.
type Client struct {
	http *http.Client

	mu          sync.Mutex
	conditions  map[string]cachedConditions
	coordinates map[string]cachedCoordinates

	now func() time.Time
}

type cachedConditions struct {
	value   Conditions
	fetched time.Time
	// ok records that the last attempt succeeded. A failure is cached too, briefly,
	// so an unreachable service is not retried on every single page view.
	ok bool
}

type cachedCoordinates struct {
	latitude  float64
	longitude float64
	fetched   time.Time
	ok        bool
}

// New builds a client.
func New() *Client {
	return &Client{
		http:        &http.Client{Timeout: requestTimeout},
		conditions:  make(map[string]cachedConditions),
		coordinates: make(map[string]cachedCoordinates),
		now:         time.Now,
	}
}

// SetClock replaces the client's clock. Intended for tests.
func (c *Client) SetClock(now func() time.Time) { c.now = now }

// SetHTTPClient replaces the transport. Intended for tests.
func (c *Client) SetHTTPClient(client *http.Client) { c.http = client }

// The endpoints are variables rather than constants so that tests in this package
// can point them at a local server instead of reaching the real service.
var (
	geocodeEndpoint  = geocodeURL
	forecastEndpoint = forecastURL
)

// For reports the weather in a city, or ok=false when it is not available.
//
// The boolean rather than an error is deliberate: there is exactly one thing a
// caller can do about a failure, which is not show the line.
func (c *Client) For(ctx context.Context, city string) (Conditions, bool) {
	city = strings.TrimSpace(city)
	if city == "" {
		return Conditions{}, false
	}

	if cached, ok := c.cachedConditions(city); ok {
		return cached.value, cached.ok
	}

	latitude, longitude, ok := c.coordinatesFor(ctx, city)
	if !ok {
		c.storeConditions(city, Conditions{}, false)
		return Conditions{}, false
	}

	conditions, err := c.fetchConditions(ctx, latitude, longitude)
	if err != nil {
		c.storeConditions(city, Conditions{}, false)
		return Conditions{}, false
	}

	c.storeConditions(city, conditions, true)
	return conditions, true
}

func (c *Client) cachedConditions(city string) (cachedConditions, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	cached, ok := c.conditions[city]
	if !ok {
		return cachedConditions{}, false
	}
	// A failed lookup is remembered for a shorter time, so a service that comes
	// back is picked up reasonably soon.
	ttl := cacheTTL
	if !cached.ok {
		ttl = cacheTTL / 6
	}
	if c.now().Sub(cached.fetched) > ttl {
		return cachedConditions{}, false
	}
	return cached, true
}

func (c *Client) storeConditions(city string, value Conditions, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.conditions[city] = cachedConditions{value: value, fetched: c.now(), ok: ok}
}

// coordinatesFor resolves a city name to coordinates, remembering the answer.
func (c *Client) coordinatesFor(ctx context.Context, city string) (latitude, longitude float64, ok bool) {
	c.mu.Lock()
	cached, found := c.coordinates[city]
	c.mu.Unlock()

	if found && c.now().Sub(cached.fetched) < coordinateTTL {
		return cached.latitude, cached.longitude, cached.ok
	}

	latitude, longitude, err := c.fetchCoordinates(ctx, city)
	entry := cachedCoordinates{latitude: latitude, longitude: longitude, fetched: c.now(), ok: err == nil}

	c.mu.Lock()
	c.coordinates[city] = entry
	c.mu.Unlock()

	return latitude, longitude, entry.ok
}

func (c *Client) fetchCoordinates(ctx context.Context, city string) (latitude, longitude float64, err error) {
	query := url.Values{
		"name":   {city},
		"count":  {"1"},
		"format": {"json"},
	}

	var payload struct {
		Results []struct {
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
		} `json:"results"`
	}
	if err := c.getJSON(ctx, geocodeEndpoint+"?"+query.Encode(), &payload); err != nil {
		return 0, 0, err
	}
	if len(payload.Results) == 0 {
		return 0, 0, fmt.Errorf("weather: no place called %q", city)
	}
	return payload.Results[0].Latitude, payload.Results[0].Longitude, nil
}

func (c *Client) fetchConditions(ctx context.Context, latitude, longitude float64) (Conditions, error) {
	query := url.Values{
		"latitude":  {strconv.FormatFloat(latitude, 'f', 4, 64)},
		"longitude": {strconv.FormatFloat(longitude, 'f', 4, 64)},
		"current":   {"temperature_2m,weather_code"},
		"timezone":  {"UTC"},
	}

	var payload struct {
		Current struct {
			Temperature float64 `json:"temperature_2m"`
			Code        int     `json:"weather_code"`
		} `json:"current"`
	}
	if err := c.getJSON(ctx, forecastEndpoint+"?"+query.Encode(), &payload); err != nil {
		return Conditions{}, err
	}

	return Conditions{
		TemperatureC: payload.Current.Temperature,
		Code:         payload.Current.Code,
		Icon:         Icon(payload.Current.Code),
	}, nil
}

func (c *Client) getJSON(ctx context.Context, target string, into any) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("weather: build the request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("weather: request %s: %w", target, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("weather: %s returned %s", target, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		return fmt.Errorf("weather: decode the response: %w", err)
	}
	return nil
}

// Icon maps a WMO weather code to one emoji.
//
// The codes come in ranges rather than a flat list, so this is written as ordered
// bands. Anything unrecognised gets a neutral cloud rather than nothing.
func Icon(code int) string {
	switch {
	case code == 0:
		return "☀️"
	case code == 1 || code == 2:
		return "🌤️"
	case code == 3:
		return "☁️"
	case code == 45 || code == 48:
		return "🌫️"
	case code >= 51 && code <= 57:
		return "🌦️"
	case code >= 61 && code <= 67:
		return "🌧️"
	case code >= 71 && code <= 77:
		return "🌨️"
	case code >= 80 && code <= 82:
		return "🌧️"
	case code >= 85 && code <= 86:
		return "🌨️"
	// Thunderstorms are 95, 96 and 99. An open-ended test here would swallow any
	// out-of-range value and report it as a storm.
	case code >= 95 && code <= 99:
		return "⛈️"
	default:
		return "☁️"
	}
}
