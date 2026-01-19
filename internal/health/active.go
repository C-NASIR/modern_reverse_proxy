package health

import (
	"net/http"
	"time"
)

func ActiveProbeLoop(cfg Config, addr string, stop <-chan struct{}, onSuccess func(), onFailure func()) {
	client := &http.Client{Timeout: cfg.Timeout}
	path := cfg.Path
	if path == "" {
		path = "/healthz"
	}

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			safeProbe(client, addr, path, onSuccess, onFailure)
		}
	}
}

func safeProbe(client *http.Client, addr string, path string, onSuccess func(), onFailure func()) {
	defer func() {
		if recover() != nil {
			onFailure()
		}
	}()

	req, err := http.NewRequest(http.MethodGet, "http://"+addr+path, nil)
	if err != nil {
		onFailure()
		return
	}

	resp, err := client.Do(req)
	if err != nil {
		onFailure()
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		onSuccess()
		return
	}
	onFailure()
}
