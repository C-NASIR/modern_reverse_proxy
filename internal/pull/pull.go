package pull

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"log"
	"math/rand"
	"net/http"
	"time"

	"modern_reverse_proxy/internal/bundle"
	"modern_reverse_proxy/internal/obs"
	"modern_reverse_proxy/internal/rollout"
	"modern_reverse_proxy/internal/runtime"
)

type Config struct {
	Enabled        bool
	BaseURL        string
	Interval       time.Duration
	Jitter         time.Duration
	PublicKey      ed25519.PublicKey
	RolloutManager *rollout.Manager
	Store          *runtime.Store
	HTTPClient     *http.Client
	Token          string
}

type Puller struct {
	enabled   bool
	baseURL   string
	interval  time.Duration
	jitter    time.Duration
	publicKey ed25519.PublicKey
	rollout   *rollout.Manager
	store     *runtime.Store
	client    *http.Client
	token     string
}

func NewPuller(cfg Config) *Puller {
	interval := cfg.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	jitter := cfg.Jitter
	if jitter < 0 {
		jitter = 0
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &Puller{
		enabled:   cfg.Enabled,
		baseURL:   cfg.BaseURL,
		interval:  interval,
		jitter:    jitter,
		publicKey: cfg.PublicKey,
		rollout:   cfg.RolloutManager,
		store:     cfg.Store,
		client:    client,
		token:     cfg.Token,
	}
}

func (p *Puller) Run(ctx context.Context) {
	if p == nil || !p.enabled {
		return
	}
	if p.baseURL == "" {
		return
	}
	p.pullOnce(ctx)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if p.jitter > 0 {
				jitter := time.Duration(rand.Int63n(int64(p.jitter)))
				select {
				case <-ctx.Done():
					return
				case <-time.After(jitter):
				}
			}
			p.pullOnce(ctx)
		}
	}
}

func (p *Puller) pullOnce(ctx context.Context) {
	if p == nil {
		return
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/bundles/latest", nil)
	if err != nil {
		return
	}
	if p.token != "" {
		request.Header.Set("X-Distributor-Token", p.token)
	}
	resp, err := p.client.Do(request)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var bundlePayload bundle.Bundle
	if err := json.NewDecoder(resp.Body).Decode(&bundlePayload); err != nil {
		return
	}
	if bundlePayload.Meta.Version == "" {
		return
	}
	current := ""
	if p.store != nil {
		if snap := p.store.Get(); snap != nil {
			current = snap.Version
		}
	}
	if current == bundlePayload.Meta.Version {
		return
	}
	metrics := obs.DefaultMetrics()
	if err := bundle.VerifyBundle(bundlePayload, p.publicKey); err != nil {
		result := "bad_sig"
		if errors.Is(err, bundle.ErrBadHash) {
			result = "bad_hash"
		}
		if metrics != nil {
			metrics.RecordBundleVerify(result)
		}
		log.Printf("bundle_version=%s verify_result=%s", bundlePayload.Meta.Version, result)
		return
	}
	if metrics != nil {
		metrics.RecordBundleVerify("ok")
	}
	log.Printf("bundle_version=%s verify_result=ok", bundlePayload.Meta.Version)
	if p.rollout == nil {
		return
	}
	if _, err := p.rollout.ApplyBundle(ctx, bundlePayload, ""); err != nil {
		log.Printf("bundle_version=%s rollout_result=error reason=%v", bundlePayload.Meta.Version, err)
	}
}
