// Package update tells whether a newer Dialga release is published on the
// public GitHub mirror. The only network traffic is one unauthenticated GET
// of the "latest release" document; the answer is cached for 12 hours in the
// user cache directory. Nothing about the user or the machine is sent.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// Repository is the public mirror whose releases are checked.
	Repository = "fpellizz/dialga"
	// LatestURL is the GitHub API document of the latest published release
	// (GitHub leaves out drafts and prereleases).
	LatestURL = "https://api.github.com/repos/" + Repository + "/releases/latest"
	// TTL is how long an answer is reused.
	TTL = 12 * time.Hour
	// Timeout bounds one request.
	Timeout = 10 * time.Second

	maxBody = 1 << 20
)

// Info is the answer of the check, also the body of GET /api/v1/update.
type Info struct {
	Current     string `json:"current"`
	Latest      string `json:"latest,omitempty"`
	Newer       bool   `json:"newer"`
	URL         string `json:"url,omitempty"`
	PublishedAt string `json:"publishedAt,omitempty"`
	CheckedAt   string `json:"checkedAt,omitempty"`
	Error       string `json:"error,omitempty"`
}

// Checker asks GitHub, with a cache. The zero value is not usable: use New.
type Checker struct {
	// Version is the running version ("dev" or not semver: never newer).
	Version string
	// Endpoint is the latest-release document (default LatestURL).
	Endpoint string
	// CacheFile holds the last good answer ("": no cache).
	CacheFile string
	Client    *http.Client
	Now       func() time.Time

	insecure bool // tests: allow http://
}

// New returns a Checker for the given running version, caching in
// os.UserCacheDir()/dialga/update-check.json (no cache if there is none).
func New(version string) *Checker {
	c := &Checker{Version: version, Endpoint: LatestURL, Now: time.Now}
	if dir, err := os.UserCacheDir(); err == nil {
		c.CacheFile = filepath.Join(dir, "dialga", "update-check.json")
	}
	return c
}

// cached is the file content: the last good answer from GitHub.
type cached struct {
	CheckedAt   time.Time `json:"checkedAt"`
	Tag         string    `json:"tag,omitempty"` // empty: no release published
	PublishedAt string    `json:"publishedAt,omitempty"`
}

var stableTag = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// ReleaseURL is the release page of a tag.
func ReleaseURL(tag string) string {
	return "https://github.com/" + Repository + "/releases/tag/" + tag
}

// Check returns the update state. With force it ignores a fresh cache. On a
// failure it returns the last good answer (if any) plus the error in Error;
// it never returns a Go error and never panics on a bad answer.
func (c *Checker) Check(ctx context.Context, force bool) Info {
	now := c.now()
	last, haveLast := c.readCache()
	if haveLast && !force && now.Sub(last.CheckedAt) >= 0 && now.Sub(last.CheckedAt) < TTL {
		return c.info(last, "")
	}
	fresh, err := c.fetch(ctx)
	if err != nil {
		if haveLast {
			return c.info(last, err.Error())
		}
		return Info{Current: c.Version, Error: err.Error()}
	}
	fresh.CheckedAt = now.UTC()
	c.writeCache(fresh)
	return c.info(fresh, "")
}

func (c *Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Checker) info(k cached, errMsg string) Info {
	in := Info{Current: c.Version, Error: errMsg}
	if !k.CheckedAt.IsZero() {
		in.CheckedAt = k.CheckedAt.UTC().Format(time.RFC3339)
	}
	if k.Tag == "" {
		return in
	}
	in.Latest = strings.TrimPrefix(k.Tag, "v")
	in.PublishedAt = k.PublishedAt
	in.URL = ReleaseURL(k.Tag)
	in.Newer = Newer(k.Tag, c.Version)
	return in
}

type release struct {
	TagName     string `json:"tag_name"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	PublishedAt string `json:"published_at"`
}

func (c *Checker) fetch(ctx context.Context) (cached, error) {
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = LatestURL
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "https" && !(c.insecure && u.Scheme == "http")) {
		return cached{}, errors.New("update check: the endpoint must be an https URL")
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return cached{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "dialga/"+c.Version)
	client := c.Client
	if client == nil {
		client = &http.Client{}
	}
	cl := *client
	cl.CheckRedirect = func(r *http.Request, _ []*http.Request) error {
		if r.URL.Scheme != "https" && !c.insecure {
			return errors.New("redirect to a non-https URL")
		}
		return nil
	}
	resp, err := cl.Do(req)
	if err != nil {
		return cached{}, fmt.Errorf("update check: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return cached{}, nil // no release published yet
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return cached{}, errors.New("update check: GitHub rate limit reached, try again later")
	case resp.StatusCode != http.StatusOK:
		return cached{}, fmt.Errorf("update check: GitHub answered %s", resp.Status)
	}
	var rel release
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&rel); err != nil {
		return cached{}, fmt.Errorf("update check: unreadable answer: %w", err)
	}
	if rel.Draft || rel.Prerelease || !stableTag.MatchString(rel.TagName) {
		return cached{}, nil
	}
	return cached{Tag: rel.TagName, PublishedAt: rel.PublishedAt}, nil
}

func (c *Checker) readCache() (cached, bool) {
	if c.CacheFile == "" {
		return cached{}, false
	}
	data, err := os.ReadFile(c.CacheFile)
	if err != nil {
		return cached{}, false
	}
	var k cached
	if json.Unmarshal(data, &k) != nil || k.CheckedAt.IsZero() {
		return cached{}, false
	}
	if k.Tag != "" && !stableTag.MatchString(k.Tag) {
		return cached{}, false
	}
	return k, true
}

// writeCache is best effort: a read-only cache dir only costs a new request.
func (c *Checker) writeCache(k cached) {
	if c.CacheFile == "" {
		return
	}
	data, err := json.Marshal(k)
	if err != nil || os.MkdirAll(filepath.Dir(c.CacheFile), 0o700) != nil {
		return
	}
	tmp := c.CacheFile + ".tmp"
	if os.WriteFile(tmp, data, 0o600) != nil {
		return
	}
	if os.Rename(tmp, c.CacheFile) != nil {
		_ = os.Remove(tmp)
	}
}

// Newer reports whether latest is a higher semantic version than current.
// Either one that is not semver (a "dev" build) gives false.
func Newer(latest, current string) bool {
	l, ok1 := parseSemver(latest)
	c, ok2 := parseSemver(current)
	return ok1 && ok2 && l.compare(c) > 0
}

type semver struct {
	core [3]int
	pre  []string
}

var semverRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$`)

func parseSemver(s string) (semver, bool) {
	m := semverRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return semver{}, false
	}
	var v semver
	for i := range 3 {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return semver{}, false
		}
		v.core[i] = n
	}
	if m[4] != "" {
		v.pre = strings.Split(m[4], ".")
	}
	return v, true
}

func (a semver) compare(b semver) int {
	for i := range 3 {
		if a.core[i] != b.core[i] {
			return cmp(a.core[i], b.core[i])
		}
	}
	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return 0
	case len(a.pre) == 0:
		return 1
	case len(b.pre) == 0:
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		x, y := a.pre[i], b.pre[i]
		nx, ex := strconv.Atoi(x)
		ny, ey := strconv.Atoi(y)
		switch {
		case ex == nil && ey == nil && nx != ny:
			return cmp(nx, ny)
		case ex == nil && ey != nil:
			return -1
		case ex != nil && ey == nil:
			return 1
		case ex != nil && ey != nil && x != y:
			return strings.Compare(x, y)
		}
	}
	return cmp(len(a.pre), len(b.pre))
}

func cmp(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
