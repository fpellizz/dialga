package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fake struct {
	srv   *httptest.Server
	hits  atomic.Int32
	code  int
	body  string
	agent string
	acc   string
}

func newFake(t *testing.T, code int, body string) *fake {
	t.Helper()
	f := &fake{code: code, body: body}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		f.agent, f.acc = r.Header.Get("User-Agent"), r.Header.Get("Accept")
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected request: %s, auth %q", r.Method, r.Header.Get("Authorization"))
		}
		w.WriteHeader(f.code)
		_, _ = w.Write([]byte(f.body))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) checker(t *testing.T, version string) *Checker {
	return &Checker{
		Version: version, Endpoint: f.srv.URL, insecure: true,
		CacheFile: filepath.Join(t.TempDir(), "sub", "update-check.json"),
	}
}

const rel = `{"tag_name":"v1.2.0","draft":false,"prerelease":false,"published_at":"2026-10-20T10:00:00Z"}`

func TestNewerSameOlder(t *testing.T) {
	for _, tc := range []struct {
		current string
		newer   bool
	}{{"1.1.9", true}, {"1.2.0", false}, {"1.3.0", false}, {"1.2.0-rc.1", true}, {"v1.0.0", true}} {
		f := newFake(t, 200, rel)
		in := f.checker(t, tc.current).Check(context.Background(), false)
		if in.Newer != tc.newer || in.Latest != "1.2.0" || in.Error != "" {
			t.Errorf("%s: %+v", tc.current, in)
		}
		if in.URL != "https://github.com/fpellizz/dialga/releases/tag/v1.2.0" || in.PublishedAt == "" || in.CheckedAt == "" {
			t.Errorf("%s: %+v", tc.current, in)
		}
		if f.agent != "dialga/"+tc.current || f.acc != "application/vnd.github+json" {
			t.Errorf("headers: %q %q", f.agent, f.acc)
		}
	}
}

func TestDevVersionNeverNewer(t *testing.T) {
	for _, v := range []string{"dev", "", "abc", "1.2"} {
		f := newFake(t, 200, rel)
		if in := f.checker(t, v).Check(context.Background(), false); in.Newer || in.Latest != "1.2.0" {
			t.Errorf("%q: %+v", v, in)
		}
	}
}

func TestPrereleaseAndDraftIgnored(t *testing.T) {
	for _, body := range []string{
		`{"tag_name":"v2.0.0","prerelease":true}`,
		`{"tag_name":"v2.0.0","draft":true}`,
		`{"tag_name":"v2.0.0-rc.1"}`,
	} {
		f := newFake(t, 200, body)
		if in := f.checker(t, "1.0.0").Check(context.Background(), false); in.Newer || in.Latest != "" || in.Error != "" {
			t.Errorf("%s: %+v", body, in)
		}
	}
}

func TestNoReleaseYet(t *testing.T) {
	f := newFake(t, 404, `{"message":"Not Found"}`)
	in := f.checker(t, "1.0.0").Check(context.Background(), false)
	if in.Newer || in.Latest != "" || in.Error != "" || in.CheckedAt == "" {
		t.Fatalf("%+v", in)
	}
}

func TestBadJSONAndStatus(t *testing.T) {
	f := newFake(t, 200, `{not json`)
	if in := f.checker(t, "1.0.0").Check(context.Background(), false); in.Error == "" || in.Newer {
		t.Errorf("bad json: %+v", in)
	}
	f = newFake(t, 500, ``)
	if in := f.checker(t, "1.0.0").Check(context.Background(), false); in.Error == "" {
		t.Errorf("500: %+v", in)
	}
}

func TestRateLimited(t *testing.T) {
	for _, code := range []int{403, 429} {
		f := newFake(t, code, `{"message":"API rate limit exceeded"}`)
		in := f.checker(t, "1.0.0").Check(context.Background(), false)
		if !strings.Contains(in.Error, "rate limit") || in.Newer {
			t.Errorf("%d: %+v", code, in)
		}
	}
}

func TestHTTPSOnly(t *testing.T) {
	f := newFake(t, 200, rel)
	c := f.checker(t, "1.0.0")
	c.insecure = false
	if in := c.Check(context.Background(), false); in.Error == "" || f.hits.Load() != 0 {
		t.Fatalf("%+v hits %d", in, f.hits.Load())
	}
}

func TestCacheHitExpiryAndForce(t *testing.T) {
	f := newFake(t, 200, rel)
	c := f.checker(t, "1.0.0")
	now := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	c.Now = func() time.Time { return now }
	ctx := context.Background()
	c.Check(ctx, false)
	now = now.Add(11 * time.Hour)
	if in := c.Check(ctx, false); !in.Newer || f.hits.Load() != 1 {
		t.Fatalf("cache hit: %+v hits %d", in, f.hits.Load())
	}
	c.Check(ctx, true)
	if f.hits.Load() != 2 {
		t.Fatalf("force: hits %d", f.hits.Load())
	}
	now = now.Add(13 * time.Hour)
	c.Check(ctx, false)
	if f.hits.Load() != 3 {
		t.Fatalf("expiry: hits %d", f.hits.Load())
	}
}

func TestErrorKeepsLastGoodCache(t *testing.T) {
	f := newFake(t, 200, rel)
	c := f.checker(t, "1.0.0")
	now := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	c.Now = func() time.Time { return now }
	c.Check(context.Background(), false)
	f.code, f.body = 403, `{}`
	now = now.Add(24 * time.Hour)
	in := c.Check(context.Background(), false)
	if !in.Newer || in.Latest != "1.2.0" || in.Error == "" {
		t.Fatalf("%+v", in)
	}
	if !strings.HasPrefix(in.CheckedAt, "2026-10-20T08:00:00") {
		t.Errorf("checkedAt moved on error: %s", in.CheckedAt)
	}
}

func TestCacheFileHasPrivateMode(t *testing.T) {
	f := newFake(t, 200, rel)
	c := f.checker(t, "1.0.0")
	c.Check(context.Background(), false)
	st, err := os.Stat(c.CacheFile)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", st, err)
	}
}

func TestCorruptCacheIgnored(t *testing.T) {
	f := newFake(t, 200, rel)
	c := f.checker(t, "1.0.0")
	_ = os.MkdirAll(filepath.Dir(c.CacheFile), 0o700)
	_ = os.WriteFile(c.CacheFile, []byte(`{"checkedAt":"2999-01-01T00:00:00Z","tag":"javascript:alert(1)"}`), 0o600)
	if in := c.Check(context.Background(), false); in.Latest != "1.2.0" || f.hits.Load() != 1 {
		t.Fatalf("%+v", in)
	}
}

func TestSemverOrder(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"1.10.0", "1.9.0", true}, {"1.0.0", "1.0.0-rc.1", true}, {"1.0.0-rc.2", "1.0.0-rc.1", true},
		{"1.0.0-rc.10", "1.0.0-rc.9", true}, {"1.0.0-rc.1", "1.0.0", false}, {"1.0.0-beta", "1.0.0-alpha", true},
		{"1.0.0", "1.0.0+build", false}, {"1.0.0", "dev", false},
	} {
		if got := Newer(tc.a, tc.b); got != tc.want {
			t.Errorf("Newer(%s,%s) = %v", tc.a, tc.b, got)
		}
	}
}
