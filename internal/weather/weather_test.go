package weather

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fakeService stands in for Open-Meteo and counts how often it is called, which is
// how the caching behaviour is checked.
type fakeService struct {
	server        *httptest.Server
	geocodeCalls  atomic.Int32
	forecastCalls atomic.Int32
	geocodeBody   string
	forecastBody  string
	geocodeStatus int
}

func newFakeService(t *testing.T) *fakeService {
	t.Helper()

	f := &fakeService{
		geocodeBody:   `{"results":[{"latitude":31.2222,"longitude":121.4581}]}`,
		forecastBody:  `{"current":{"temperature_2m":21.4,"weather_code":3}}`,
		geocodeStatus: http.StatusOK,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/geocode", func(w http.ResponseWriter, _ *http.Request) {
		f.geocodeCalls.Add(1)
		w.WriteHeader(f.geocodeStatus)
		w.Write([]byte(f.geocodeBody))
	})
	mux.HandleFunc("/forecast", func(w http.ResponseWriter, _ *http.Request) {
		f.forecastCalls.Add(1)
		w.Write([]byte(f.forecastBody))
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)

	// Point the package at the fake for the duration of the test.
	oldGeocode, oldForecast := geocodeEndpoint, forecastEndpoint
	geocodeEndpoint = f.server.URL + "/geocode"
	forecastEndpoint = f.server.URL + "/forecast"
	t.Cleanup(func() {
		geocodeEndpoint, forecastEndpoint = oldGeocode, oldForecast
	})

	return f
}

func TestForFetchesAndCaches(t *testing.T) {
	fake := newFakeService(t)
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	client := New()
	client.SetClock(func() time.Time { return now })

	conditions, ok := client.For(context.Background(), "Shanghai")
	if !ok {
		t.Fatal("For() returned ok = false, want a reading")
	}
	if conditions.TemperatureC != 21.4 {
		t.Errorf("TemperatureC = %v, want 21.4", conditions.TemperatureC)
	}
	if conditions.Icon != "☁️" {
		t.Errorf("Icon = %q, want the overcast cloud for code 3", conditions.Icon)
	}

	// A second call inside the window must not touch the network again.
	for range 5 {
		client.For(context.Background(), "Shanghai")
	}
	if got := fake.forecastCalls.Load(); got != 1 {
		t.Errorf("forecast was requested %d times, want 1 (the rest cached)", got)
	}
	if got := fake.geocodeCalls.Load(); got != 1 {
		t.Errorf("geocoding was requested %d times, want 1", got)
	}
}

func TestCacheExpires(t *testing.T) {
	fake := newFakeService(t)
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	client := New()
	client.SetClock(func() time.Time { return now })
	client.For(context.Background(), "Shanghai")

	now = now.Add(cacheTTL + time.Minute)
	client.For(context.Background(), "Shanghai")

	if got := fake.forecastCalls.Load(); got != 2 {
		t.Errorf("forecast was requested %d times, want 2 after the cache expired", got)
	}
	// Coordinates last far longer, so the city is not looked up again.
	if got := fake.geocodeCalls.Load(); got != 1 {
		t.Errorf("geocoding was requested %d times, want 1", got)
	}
}

func TestFailuresAreQuietAndNotHammered(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*fakeService)
	}{
		{
			name:  "the place is not found",
			setup: func(f *fakeService) { f.geocodeBody = `{"results":[]}` },
		},
		{
			name:  "geocoding is down",
			setup: func(f *fakeService) { f.geocodeStatus = http.StatusInternalServerError },
		},
		{
			name:  "the response is not json",
			setup: func(f *fakeService) { f.geocodeBody = `not json at all` },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newFakeService(t)
			tt.setup(fake)

			now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
			client := New()
			client.SetClock(func() time.Time { return now })

			if _, ok := client.For(context.Background(), "Nowhere"); ok {
				t.Error("For() returned ok = true despite the failure")
			}

			// A failure is cached too, so an unreachable service is not retried on
			// every page view.
			for range 4 {
				client.For(context.Background(), "Nowhere")
			}
			if got := fake.geocodeCalls.Load(); got != 1 {
				t.Errorf("geocoding was retried %d times, want the failure cached", got)
			}
		})
	}
}

func TestForIgnoresAnEmptyCity(t *testing.T) {
	fake := newFakeService(t)
	client := New()

	for _, city := range []string{"", "   "} {
		if _, ok := client.For(context.Background(), city); ok {
			t.Errorf("For(%q) returned ok = true", city)
		}
	}
	if got := fake.geocodeCalls.Load(); got != 0 {
		t.Errorf("an empty city made %d requests, want none", got)
	}
}

func TestIcon(t *testing.T) {
	tests := map[int]string{
		0:    "☀️",
		1:    "🌤️",
		2:    "🌤️",
		3:    "☁️",
		45:   "🌫️",
		48:   "🌫️",
		53:   "🌦️",
		63:   "🌧️",
		73:   "🌨️",
		81:   "🌧️",
		86:   "🌨️",
		95:   "⛈️",
		99:   "⛈️",
		1234: "☁️",
		-1:   "☁️",
	}

	for code, want := range tests {
		if got := Icon(code); got != want {
			t.Errorf("Icon(%d) = %q, want %q", code, got, want)
		}
	}
}
