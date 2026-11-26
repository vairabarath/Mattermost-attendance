package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vairabarath/Mattermost-attendance/internal/attendance"
)

const testOrg = "inkyank-org"

var (
	keyOnce sync.Once
	testKey *rsa.PrivateKey
)

func key(t *testing.T) *rsa.PrivateKey {
	keyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		testKey = k
	})
	return testKey
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeGitHub serves recorded responses. Handlers for App endpoints check
// the JWT signature; everything else checks the installation token.
type fakeGitHub struct {
	t            *testing.T
	mu           sync.Mutex
	calls        map[string]int
	fields       string            // fixture for the fields query
	graphqlFails int               // first N /graphql calls return 502
	rateLimited  bool              // /graphql answers 403 rate limited
	members      map[string]int    // login → status for /orgs/x/members/login
	users        map[string]string // lower(login) → fixture
	queries      []map[string]any  // /graphql variables seen
}

func newFake(t *testing.T) (*fakeGitHub, *httptest.Server) {
	f := &fakeGitHub{t: t, calls: map[string]int{}, fields: "project_fields.json",
		members: map[string]int{}, users: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeGitHub) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[path]
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls[r.URL.Path]++
	n := f.calls[r.URL.Path]
	f.mu.Unlock()

	auth := r.Header.Get("Authorization")
	write := func(status int, name string) {
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.WriteHeader(status)
		if name != "" {
			_, _ = w.Write(fixture(f.t, name))
		}
	}
	switch {
	case r.URL.Path == "/orgs/"+testOrg+"/installation" || strings.HasPrefix(r.URL.Path, "/app/installations/"):
		if err := verifyJWT(strings.TrimPrefix(auth, "Bearer "), &key(f.t).PublicKey); err != nil {
			f.t.Errorf("%s: bad app JWT: %v", r.URL.Path, err)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/app/installations/777/access_tokens" && r.Method == http.MethodPost {
			write(http.StatusCreated, "access_token.json")
			return
		}
		if strings.HasSuffix(r.URL.Path, "/installation") {
			write(http.StatusOK, "installation.json")
			return
		}
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if auth != "token ghs_testtoken" {
		f.t.Errorf("%s: wrong auth header", r.URL.Path)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case r.URL.Path == "/graphql":
		if f.rateLimited {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", "4102444800")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"message":"API rate limit exceeded"}`)
			return
		}
		if n <= f.graphqlFails {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.queries = append(f.queries, body.Variables)
		f.mu.Unlock()
		switch {
		case strings.Contains(body.Query, "projectV2(number"):
			write(http.StatusOK, f.fields)
		case strings.Contains(body.Query, "closed: search("):
			write(http.StatusOK, "eod_tasks.json")
		case strings.Contains(body.Query, "search("):
			if body.Variables["after"] == nil {
				write(http.StatusOK, "search_page1.json")
			} else {
				write(http.StatusOK, "search_page2.json")
			}
		default:
			f.t.Errorf("unexpected query %q", body.Query)
		}
	case strings.HasPrefix(r.URL.Path, "/users/"):
		name, ok := f.users[strings.ToLower(strings.TrimPrefix(r.URL.Path, "/users/"))]
		if !ok {
			write(http.StatusNotFound, "")
			return
		}
		write(http.StatusOK, name)
	case strings.HasPrefix(r.URL.Path, "/orgs/"+testOrg+"/members/"):
		status, ok := f.members[strings.TrimPrefix(r.URL.Path, "/orgs/"+testOrg+"/members/")]
		if !ok {
			status = http.StatusNotFound
		}
		if status == http.StatusFound {
			w.Header().Set("Location", "/orgs/"+testOrg+"/public_members/x")
		}
		w.WriteHeader(status)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func verifyJWT(tok string, pub *rsa.PublicKey) error {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return errors.New("not a JWT")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	return rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig)
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newTestApp(t *testing.T, srv *httptest.Server) *App {
	a := NewApp(srv.URL, 12345, key(t), quietLog())
	a.retryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	return a
}

func newTestTasks(t *testing.T, srv *httptest.Server) *Tasks {
	return NewTasks(newTestApp(t, srv), map[string]TeamProject{
		"A": {Org: testOrg, Number: 1},
		"B": {}, // no GitHub
	}, quietLog())
}

func TestAppJWT(t *testing.T) {
	a := NewApp("http://x", 12345, key(t), quietLog())
	now := time.Unix(1_800_000_000, 0)
	a.now = func() time.Time { return now }
	tok, err := a.appJWT()
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyJWT(tok, &key(t).PublicKey); err != nil {
		t.Fatalf("signature: %v", err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(strings.Split(tok, ".")[1])
	var c struct{ Iat, Exp, Iss int64 }
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if c.Iss != 12345 || c.Iat != now.Unix()-60 || c.Exp != now.Add(9*time.Minute).Unix() {
		t.Errorf("claims %+v", c)
	}
}

func TestParsePrivateKey(t *testing.T) {
	k := key(t)
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	for name, raw := range map[string][]byte{"pkcs1": pkcs1, "pkcs8": pkcs8} {
		got, err := ParsePrivateKey(raw)
		if err != nil || !got.Equal(k) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := ParsePrivateKey([]byte("not a key")); err == nil {
		t.Error("garbage accepted")
	}

	dir := t.TempDir()
	p := filepath.Join(dir, "app.pem")
	if err := os.WriteFile(p, pkcs1, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, warn, err := LoadPrivateKey(p); err != nil || warn == "" {
		t.Errorf("0644 key should load with a warning: %q %v", warn, err)
	}
	_ = os.Chmod(p, 0o400)
	if _, warn, err := LoadPrivateKey(p); err != nil || warn != "" {
		t.Errorf("0400 key: %q %v", warn, err)
	}
}

func TestInstallationTokenCached(t *testing.T) {
	f, srv := newFake(t)
	a := newTestApp(t, srv)
	for i := 0; i < 3; i++ {
		if tok, err := a.token(context.Background(), testOrg); err != nil || tok != "ghs_testtoken" {
			t.Fatalf("token: %q %v", tok, err)
		}
	}
	if n := f.count("/app/installations/777/access_tokens"); n != 1 {
		t.Errorf("token minted %d times, want 1", n)
	}
	if n := f.count("/orgs/" + testOrg + "/installation"); n != 1 {
		t.Errorf("installation looked up %d times, want 1", n)
	}
}

func TestFetchSchema(t *testing.T) {
	_, srv := newFake(t)
	s, err := newTestApp(t, srv).FetchSchema(context.Background(), testOrg, 1)
	if err != nil {
		t.Fatal(err)
	}
	if s.ProjectID != "PVT_kwDOinkyank" || s.Status.ID != "PVTSSF_status" || s.Status.Options["Blocked"] != "b1c2d3e4" ||
		s.Priority.Options["High"] != "p1" || s.DueDate.ID != "PVTF_due" || s.Estimate == nil {
		t.Errorf("schema %+v", s)
	}
}

func TestFetchSchemaMissingFields(t *testing.T) {
	f, srv := newFake(t)
	f.fields = "project_fields_missing.json"
	_, err := newTestApp(t, srv).FetchSchema(context.Background(), testOrg, 1)
	if !errors.Is(err, ErrSchema) {
		t.Fatalf("want ErrSchema, got %v", err)
	}
	for _, want := range []string{
		`missing field "Priority"`,
		`no option "In progress"`, // "In Progress" (capital P) doesn't count
		`no option "Blocked"`,
		`field "Due date" is TEXT, must be DATE`,
		"fields found: Due date, Status, Title",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q:\n%v", want, err)
		}
	}

	f.fields = "project_not_found.json"
	if _, err := newTestApp(t, srv).FetchSchema(context.Background(), testOrg, 9); !errors.Is(err, ErrSchema) ||
		!strings.Contains(err.Error(), "Could not resolve") {
		t.Errorf("missing project: %v", err)
	}
}

func TestSchemaCache(t *testing.T) {
	f, srv := newFake(t)
	tasks := newTestTasks(t, srv)
	for i := 0; i < 2; i++ {
		if _, err := tasks.Schema(context.Background(), "A"); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.queries) != 1 {
		t.Errorf("schema fetched %d times, want 1 (cached)", len(f.queries))
	}
	tasks.InvalidateSchema("A")
	if _, err := tasks.Schema(context.Background(), "A"); err != nil || len(f.queries) != 2 {
		t.Errorf("invalidate should refetch: %v, %d", err, len(f.queries))
	}

	// A misconfigured project is reported as ErrGitHubNotSetUp and cached.
	f.fields = "project_fields_missing.json"
	tasks.InvalidateSchema("A")
	for i := 0; i < 2; i++ {
		if _, err := tasks.Schema(context.Background(), "A"); !errors.Is(err, attendance.ErrGitHubNotSetUp) {
			t.Fatalf("want ErrGitHubNotSetUp, got %v", err)
		}
	}
	if len(f.queries) != 3 {
		t.Errorf("failed lookup should be cached for SchemaRetry: %d fetches", len(f.queries))
	}
	if _, err := tasks.Schema(context.Background(), "B"); !errors.Is(err, attendance.ErrGitHubNotSetUp) {
		t.Errorf("team without GitHub: %v", err)
	}
}

func TestAssignedTasks(t *testing.T) {
	f, srv := newFake(t)
	got, err := newTestTasks(t, srv).AssignedTasks(context.Background(), "A", "Jana-Dev")
	if err != nil {
		t.Fatal(err)
	}
	// #46 is Done, #50 isn't on the project, {} isn't an issue.
	if len(got) != 3 {
		t.Fatalf("got %d tasks: %+v", len(got), got)
	}
	byNum := map[int]attendance.Task{}
	for _, task := range got {
		byNum[task.Number] = task
	}
	if t42 := byNum[42]; t42.Status != "In progress" || t42.Priority != "High" || t42.Due != "2026-11-24" || t42.Repo != "inkyank-org/app" {
		t.Errorf("#42: %+v", t42)
	}
	if t45 := byNum[45]; t45.Status != "Todo" || t45.Priority != "Low" || t45.Due != "" {
		t.Errorf("#45 should use this project's item, not the other one: %+v", t45)
	}
	if t7 := byNum[7]; t7.Status != "Blocked" || t7.Repo != "inkyank-org/operations" {
		t.Errorf("#7 (page 2): %+v", t7)
	}
	// 1 fields query + 2 search pages; the search is scoped to org + assignee.
	if len(f.queries) != 3 || f.queries[1]["q"] != "org:inkyank-org assignee:Jana-Dev is:issue is:open archived:false" ||
		f.queries[2]["after"] != "Y3Vyc29yOjUw" {
		t.Errorf("queries: %v", f.queries)
	}
	if _, err := newTestTasks(t, srv).AssignedTasks(context.Background(), "A", "bad login!"); err == nil {
		t.Error("invalid login should be refused before searching")
	}
}

func TestVerifyLogin(t *testing.T) {
	f, srv := newFake(t)
	f.users["jana-dev"] = "user_jana.json"
	f.users["some-org"] = "user_org.json"
	f.members["Jana-Dev"] = http.StatusNoContent
	tasks := newTestTasks(t, srv)
	ctx := context.Background()

	if login, err := tasks.VerifyLogin(ctx, "A", "jana-dev"); err != nil || login != "Jana-Dev" {
		t.Errorf("member: %q %v (want canonical spelling)", login, err)
	}
	if _, err := tasks.VerifyLogin(ctx, "A", "nobody"); !errors.Is(err, attendance.ErrGitHubUserNotFound) {
		t.Errorf("unknown user: %v", err)
	}
	if _, err := tasks.VerifyLogin(ctx, "A", "some-org"); !errors.Is(err, attendance.ErrGitHubUserNotFound) {
		t.Errorf("organization account: %v", err)
	}
	f.members["Jana-Dev"] = http.StatusNotFound
	if _, err := tasks.VerifyLogin(ctx, "A", "jana-dev"); !errors.Is(err, attendance.ErrNotOrgMember) {
		t.Errorf("not a member: %v", err)
	}
	f.members["Jana-Dev"] = http.StatusFound // App lacks Members (read)
	if _, err := tasks.VerifyLogin(ctx, "A", "jana-dev"); err == nil || !strings.Contains(err.Error(), "Members (read)") {
		t.Errorf("redirect should explain the missing permission: %v", err)
	}
	if f.count("/orgs/"+testOrg+"/public_members/x") != 0 {
		t.Error("redirect was followed")
	}
}

func TestRetryOnServerError(t *testing.T) {
	f, srv := newFake(t)
	f.graphqlFails = 2
	if _, err := newTestApp(t, srv).FetchSchema(context.Background(), testOrg, 1); err != nil {
		t.Fatalf("should succeed on the 3rd attempt: %v", err)
	}
	f2, srv2 := newFake(t)
	f2.graphqlFails = 3
	if _, err := newTestApp(t, srv2).FetchSchema(context.Background(), testOrg, 1); err == nil {
		t.Fatal("3 failures should give up")
	}
	if n := f2.count("/graphql"); n != 3 {
		t.Errorf("%d attempts, want 3", n)
	}
}

func TestRateLimitNotRetried(t *testing.T) {
	f, srv := newFake(t)
	f.rateLimited = true
	_, err := newTestApp(t, srv).FetchSchema(context.Background(), testOrg, 1)
	var rl *RateLimitError
	if !errors.As(err, &rl) || rl.Reset.Year() != 2100 {
		t.Fatalf("want RateLimitError, got %v", err)
	}
	if n := f.count("/graphql"); n != 1 {
		t.Errorf("rate-limited call retried %d times", n-1)
	}
}

func TestEODTasks(t *testing.T) {
	f, srv := newFake(t)
	ist := time.FixedZone("IST", 5*3600+1800)
	day := time.Date(2026, 11, 26, 0, 0, 0, 0, ist)
	open, closed, err := newTestTasks(t, srv).EODTasks(context.Background(), "A", "Jana-Dev", day)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].Number != 45 || open[0].Status != "In progress" {
		t.Errorf("open (other project filtered out): %+v", open)
	}
	// Closed issues are kept even though their Status is Done.
	if len(closed) != 1 || closed[0].Number != 42 || closed[0].Status != "Done" {
		t.Errorf("closed: %+v", closed)
	}
	// One round trip for both lists (plus the schema lookup); the closed
	// range is the IST day.
	if len(f.queries) != 2 {
		t.Fatalf("queries: %d", len(f.queries))
	}
	q := f.queries[1]
	if q["open"] != "org:inkyank-org assignee:Jana-Dev is:issue is:open archived:false" ||
		q["closed"] != "org:inkyank-org assignee:Jana-Dev is:issue is:closed closed:2026-11-26T00:00:00+05:30..2026-11-26T23:59:59+05:30" {
		t.Errorf("vars: %v", q)
	}
}
