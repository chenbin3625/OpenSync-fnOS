package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const latestReleaseAPI = "https://api.github.com/repos/chenbin3625/OpenSync-fnOS/releases/latest"
const latestReleaseURL = "https://github.com/chenbin3625/OpenSync-fnOS/releases/latest"

const (
	// successTTL is how long a successful GitHub answer is reused. Unauthenticated
	// api.github.com allows 60 requests per hour, so page loads must not hit it
	// every time; half an hour still surfaces a release published right after a
	// check without restarting the app. The version entry's "检查更新" button
	// bypasses this cache for users who do not want to wait.
	successTTL = 30 * time.Minute
	failureTTL = 5 * time.Minute
	// forcedRefreshInterval collapses repeated clicks on that button into one
	// upstream request. Much shorter than successTTL on purpose: the first click
	// should return a real answer, not the automatic-check cache the user sees.
	forcedRefreshInterval = 30 * time.Second
)

type versionResult struct {
	LatestVersion string `json:"latestVersion"`
	ReleaseURL    string `json:"releaseURL"`
	HasUpdate     bool   `json:"hasUpdate"`
}

type versionChecker struct {
	mu        sync.Mutex
	endpoint  string
	client    *http.Client
	latest    string
	err       error
	expiresAt time.Time
	// lastForcedRefresh gates repeated manual refreshes without preventing the
	// first button click from bypassing a fresh automatic-check cache.
	lastForcedRefresh time.Time
}

func newVersionChecker(endpoint string, client *http.Client) *versionChecker {
	return &versionChecker{endpoint: endpoint, client: client}
}

func parseReleaseVersion(value string) ([3]uint64, bool) {
	value = strings.TrimPrefix(value, "v")
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return [3]uint64{}, false
	}
	var version [3]uint64
	for i, part := range parts {
		if part == "" {
			return [3]uint64{}, false
		}
		for _, digit := range part {
			if digit < '0' || digit > '9' {
				return [3]uint64{}, false
			}
		}
		n, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return [3]uint64{}, false
		}
		version[i] = n
	}
	return version, true
}

func newerRelease(local, latest string) bool {
	current, validCurrent := parseReleaseVersion(local)
	remote, validRemote := parseReleaseVersion(latest)
	if !validCurrent || !validRemote {
		return false
	}
	for i := range current {
		if remote[i] != current[i] {
			return remote[i] > current[i]
		}
	}
	return false
}

// check reports the cached release status, refreshing it once successTTL expired.
func (c *versionChecker) check(ctx context.Context, local string) (versionResult, error) {
	return c.checkWithRefresh(ctx, local, false)
}

// checkWithRefresh additionally serves the manual "检查更新" action: force asks
// for a real upstream answer even while the cached one is still fresh, but a
// forced check is itself rate limited by forcedRefreshInterval so a button (or
// a script) cannot turn into an unauthenticated request flood against GitHub.
func (c *versionChecker) checkWithRefresh(ctx context.Context, local string, force bool) (versionResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	stale := now.After(c.expiresAt)
	forced := force && now.Sub(c.lastForcedRefresh) >= forcedRefreshInterval
	if stale || forced {
		c.latest, c.err = c.fetch(ctx)
		if force {
			c.lastForcedRefresh = now
		}
		ttl := successTTL
		if c.err != nil {
			ttl = failureTTL
		}
		c.expiresAt = now.Add(ttl)
	}
	if c.err != nil {
		return versionResult{}, c.err
	}
	return versionResult{
		LatestVersion: c.latest,
		ReleaseURL:    latestReleaseURL,
		HasUpdate:     newerRelease(local, c.latest),
	}, nil
}

func (c *versionChecker) fetch(ctx context.Context) (string, error) {
	// This result is shared across users; one closed tab must not cache its
	// canceled request as a failed GitHub check for everyone else.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "OpenSync-fnOS")
	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return c.fetchReleaseRedirect(ctx)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub release status: %d", resp.StatusCode)
	}
	var release struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<10)).Decode(&release); err != nil {
		return "", err
	}
	if release.Draft || release.Prerelease {
		return "", errors.New("latest release is not stable")
	}
	if _, valid := parseReleaseVersion(release.TagName); !valid {
		return "", errors.New("latest release has an invalid version")
	}
	return release.TagName, nil
}

func (c *versionChecker) fetchReleaseRedirect(ctx context.Context) (string, error) {
	client := *c.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, latestReleaseURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "OpenSync-fnOS")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 300 || resp.StatusCode >= 400 {
		return "", fmt.Errorf("GitHub release redirect status: %d", resp.StatusCode)
	}
	target, err := resp.Location()
	if err != nil {
		return "", err
	}
	const prefix = "/chenbin3625/OpenSync-fnOS/releases/tag/"
	if target.Scheme != "https" || target.Host != "github.com" || !strings.HasPrefix(target.Path, prefix) {
		return "", errors.New("latest release redirect is invalid")
	}
	tag := strings.TrimPrefix(target.Path, prefix)
	if _, valid := parseReleaseVersion(tag); !valid {
		return "", errors.New("latest release redirect has an invalid version")
	}
	return tag, nil
}
