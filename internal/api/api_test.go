package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"domieface/com/internal/api"
	"domieface/com/internal/auth"
	"domieface/com/internal/config"
	"domieface/com/internal/httpx"
	"domieface/com/internal/store"
	"domieface/com/internal/store/memory"
	"domieface/com/internal/uploads"
)

// fakePresigner stands in for object storage. Handler tests care about which
// key gets attached to which resource, not about talking to a bucket.
type fakePresigner struct{}

func (fakePresigner) Presign(_ context.Context, purpose uploads.Purpose, _ string, _ int64) (*uploads.Presigned, error) {
	key := fmt.Sprintf("uploads/%s/%s.jpg", store.NewID(), purpose)
	return &uploads.Presigned{
		UploadURL: "https://storage.test/" + key + "?signature=stub",
		Key:       key,
		ExpiresAt: time.Now().Add(15 * time.Minute),
	}, nil
}

func (fakePresigner) PublicURL(key string) string { return "https://cdn.test/" + key }

var testSecret = []byte("a-test-secret-that-is-long-enough-for-hs256")

type env struct {
	t      *testing.T
	server *httptest.Server
	store  *memory.Store
	api    *api.Server
}

func newEnv(t *testing.T) *env { return newEnvWithDocs(t, true) }

// unreachableStore stands in for a database that is down, so the readiness
// failure path can be exercised without stopping a container mid-test.
type unreachableStore struct{ *memory.Store }

func (unreachableStore) Ping(context.Context) error {
	return errors.New("dial tcp 127.0.0.1:5432: connection refused")
}

// newEnvWithStore swaps in a different store, for the health failure paths.
func newEnvWithStore(t *testing.T, broken unreachableStore) *env {
	t.Helper()
	broken.Store = memory.New()
	return newEnvWith(t, true, broken)
}

func newEnvWithDocs(t *testing.T, docsEnabled bool) *env {
	return newEnvWith(t, docsEnabled, memory.New())
}

func newEnvWith(t *testing.T, docsEnabled bool, st store.Store) *env {
	t.Helper()

	cfg := &config.Config{
		Environment:     "test",
		DocsEnabled:     docsEnabled,
		JWTSecret:       testSecret,
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 30 * 24 * time.Hour,
		// Rate limiting has its own tests; here it would only make the suite
		// flaky as request counts grow.
		RateLimitPerMin:     1_000_000,
		RateLimitBurst:      1_000_000,
		AuthRateLimitPerMin: 1_000_000,
		AuthRateLimitBurst:  1_000_000,
	}

	issuer := auth.NewTokenIssuer(cfg.JWTSecret, cfg.AccessTokenTTL)
	sessions := auth.NewService(st.Tokens(), issuer, cfg.RefreshTokenTTL)
	apiServer := api.New(cfg, st, sessions, issuer, fakePresigner{})
	server := httptest.NewServer(apiServer.Handler())
	t.Cleanup(server.Close)

	inMemory, _ := st.(*memory.Store)
	return &env{t: t, server: server, store: inMemory, api: apiServer}
}

// do issues a request and decodes the JSON body, if there is one.
func (e *env) do(method, path, token string, body any) (*http.Response, map[string]any) {
	e.t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("encoding request body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, e.server.URL+path, reader)
	if err != nil {
		e.t.Fatalf("building request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	res, err := e.server.Client().Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		e.t.Fatalf("reading response body: %v", err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return res, nil
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		e.t.Fatalf("%s %s returned a body that is not a JSON object: %s", method, path, raw)
	}
	return res, decoded
}

type account struct {
	id           string
	username     string
	accessToken  string
	refreshToken string
}

// register creates an account and returns its credentials.
func (e *env) register(username string) account {
	e.t.Helper()

	res, body := e.do(http.MethodPost, "/v1/auth/register", "", map[string]any{
		"email":       username + "@example.com",
		"username":    username,
		"displayName": strings.ToUpper(username[:1]) + username[1:],
		"password":    "correct-horse-battery",
	})
	if res.StatusCode != http.StatusCreated {
		e.t.Fatalf("register %s: got %d, body %v", username, res.StatusCode, body)
	}

	user, _ := body["user"].(map[string]any)
	return account{
		id:           str(user["id"]),
		username:     username,
		accessToken:  str(body["accessToken"]),
		refreshToken: str(body["refreshToken"]),
	}
}

// presign asks for an upload key as the given account.
func (e *env) presign(a account, purpose string) string {
	e.t.Helper()

	res, body := e.do(http.MethodPost, "/v1/uploads/presign", a.accessToken, map[string]any{
		"contentType":   "image/jpeg",
		"contentLength": 843201,
		"purpose":       purpose,
	})
	if res.StatusCode != http.StatusOK {
		e.t.Fatalf("presign: got %d, body %v", res.StatusCode, body)
	}
	return str(body["key"])
}

// createPost runs the full upload-then-attach flow and returns the post ID.
func (e *env) createPost(a account, caption string) string {
	e.t.Helper()

	res, body := e.do(http.MethodPost, "/v1/posts", a.accessToken, map[string]any{
		"imageKey": e.presign(a, "post"),
		"caption":  caption,
	})
	if res.StatusCode != http.StatusCreated {
		e.t.Fatalf("create post: got %d, body %v", res.StatusCode, body)
	}
	return str(body["id"])
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// assertError checks the response carries the contract's error envelope with
// the expected status and code.
func assertError(t *testing.T, res *http.Response, body map[string]any, wantStatus int, wantCode string) map[string]any {
	t.Helper()

	if res.StatusCode != wantStatus {
		t.Fatalf("status: got %d, want %d (body %v)", res.StatusCode, wantStatus, body)
	}

	envelope, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("response has no error object: %v", body)
	}
	if got := str(envelope["code"]); got != wantCode {
		t.Errorf("code: got %q, want %q", got, wantCode)
	}
	if str(envelope["message"]) == "" {
		t.Error("message is empty; it is the string shown to the user")
	}
	return envelope
}

func details(t *testing.T, envelope map[string]any) map[string]any {
	t.Helper()
	got, ok := envelope["details"].(map[string]any)
	if !ok {
		t.Fatalf("error has no details object: %v", envelope)
	}
	return got
}

func TestRegisterReturnsTokensAndUser(t *testing.T) {
	e := newEnv(t)

	res, body := e.do(http.MethodPost, "/v1/auth/register", "", map[string]any{
		"email":       "Ada@Example.com",
		"username":    "ada",
		"displayName": "Ada Lovelace",
		"password":    "correct-horse-battery",
	})

	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status: got %d, want 201 (body %v)", res.StatusCode, body)
	}
	for _, field := range []string{"accessToken", "refreshToken", "user"} {
		if body[field] == nil {
			t.Errorf("response is missing %q", field)
		}
	}

	user := body["user"].(map[string]any)
	if got := str(user["email"]); got != "ada@example.com" {
		t.Errorf("email: got %q, want it stored lowercase", got)
	}
	if got := str(user["displayName"]); got != "Ada Lovelace" {
		t.Errorf("displayName: got %q", got)
	}
	if user["avatarUrl"] != nil {
		t.Errorf("avatarUrl: got %v, want null for a new account", user["avatarUrl"])
	}
}

// Timestamps must be ISO 8601 UTC with three fractional digits everywhere, not
// only where they are convenient.
func TestTimestampsUseTheContractFormat(t *testing.T) {
	e := newEnv(t)
	ada := e.register("ada")
	postID := e.createPost(ada, "Sunset from the office.")

	format := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)

	_, user := e.do(http.MethodGet, "/v1/users/me", ada.accessToken, nil)
	if got := str(user["createdAt"]); !format.MatchString(got) {
		t.Errorf("user.createdAt: got %q", got)
	}

	_, post := e.do(http.MethodGet, "/v1/posts/"+postID, ada.accessToken, nil)
	if got := str(post["createdAt"]); !format.MatchString(got) {
		t.Errorf("post.createdAt: got %q", got)
	}
}

func TestRegisterValidationFailuresNameTheFields(t *testing.T) {
	e := newEnv(t)

	res, body := e.do(http.MethodPost, "/v1/auth/register", "", map[string]any{
		"email":       "not-an-email",
		"username":    "Ada Lovelace",
		"displayName": "",
		"password":    "short",
	})

	envelope := assertError(t, res, body, http.StatusBadRequest, httpx.CodeValidationFailed)
	got := details(t, envelope)

	for _, field := range []string{"email", "username", "displayName", "password"} {
		if got[field] == nil {
			t.Errorf("details is missing %q, so the client cannot mark that input", field)
		}
	}
}

func TestRegisterConflicts(t *testing.T) {
	e := newEnv(t)
	e.register("ada")

	t.Run("duplicate email", func(t *testing.T) {
		res, body := e.do(http.MethodPost, "/v1/auth/register", "", map[string]any{
			"email":       "ada@example.com",
			"username":    "ada2",
			"displayName": "Ada Again",
			"password":    "correct-horse-battery",
		})
		envelope := assertError(t, res, body, http.StatusConflict, httpx.CodeConflict)
		if details(t, envelope)["email"] == nil {
			t.Error("details should name the email field")
		}
	})

	t.Run("duplicate username", func(t *testing.T) {
		res, body := e.do(http.MethodPost, "/v1/auth/register", "", map[string]any{
			"email":       "other@example.com",
			"username":    "ada",
			"displayName": "Someone Else",
			"password":    "correct-horse-battery",
		})
		envelope := assertError(t, res, body, http.StatusConflict, httpx.CodeConflict)
		if details(t, envelope)["username"] == nil {
			t.Error("details should name the username field")
		}
	})
}

func TestLogin(t *testing.T) {
	e := newEnv(t)
	e.register("ada")

	t.Run("succeeds with the right password", func(t *testing.T) {
		res, body := e.do(http.MethodPost, "/v1/auth/login", "", map[string]any{
			"email": "ada@example.com", "password": "correct-horse-battery",
		})
		if res.StatusCode != http.StatusOK {
			t.Fatalf("got %d, want 200 (body %v)", res.StatusCode, body)
		}
		if str(body["accessToken"]) == "" {
			t.Error("no access token returned")
		}
	})

	t.Run("email is matched case insensitively", func(t *testing.T) {
		res, _ := e.do(http.MethodPost, "/v1/auth/login", "", map[string]any{
			"email": "ADA@EXAMPLE.COM", "password": "correct-horse-battery",
		})
		if res.StatusCode != http.StatusOK {
			t.Errorf("got %d, want 200", res.StatusCode)
		}
	})

	t.Run("wrong password", func(t *testing.T) {
		res, body := e.do(http.MethodPost, "/v1/auth/login", "", map[string]any{
			"email": "ada@example.com", "password": "wrong-horse-battery",
		})
		assertError(t, res, body, http.StatusUnauthorized, httpx.CodeInvalidCredentials)
	})

	// An unknown email and a wrong password must be indistinguishable, or the
	// response tells an attacker which addresses are registered.
	t.Run("unknown email is indistinguishable from a wrong password", func(t *testing.T) {
		res, body := e.do(http.MethodPost, "/v1/auth/login", "", map[string]any{
			"email": "nobody@example.com", "password": "correct-horse-battery",
		})
		envelope := assertError(t, res, body, http.StatusUnauthorized, httpx.CodeInvalidCredentials)
		if strings.Contains(strings.ToLower(str(envelope["message"])), "email") &&
			strings.Contains(strings.ToLower(str(envelope["message"])), "not found") {
			t.Error("the message discloses whether the account exists")
		}
	})
}

func TestProtectedRoutesRejectMissingAndMalformedTokens(t *testing.T) {
	e := newEnv(t)

	cases := map[string]string{
		"no header":     "",
		"garbage token": "not-a-jwt",
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			res, body := e.do(http.MethodGet, "/v1/users/me", token, nil)
			assertError(t, res, body, http.StatusUnauthorized, httpx.CodeTokenInvalid)
		})
	}
}

// TOKEN_EXPIRED tells the client to refresh and retry once; TOKEN_INVALID tells
// it to log the user out. Returning the wrong one strands the user.
func TestExpiredAccessTokenIsDistinctFromAnInvalidOne(t *testing.T) {
	e := newEnv(t)
	ada := e.register("ada")

	expiredIssuer := auth.NewTokenIssuer(testSecret, -time.Minute)
	expired, _, err := expiredIssuer.IssueAccess(ada.id)
	if err != nil {
		t.Fatalf("issuing an expired token: %v", err)
	}

	res, body := e.do(http.MethodGet, "/v1/users/me", expired, nil)
	assertError(t, res, body, http.StatusUnauthorized, httpx.CodeTokenExpired)
}

func TestRefreshRotatesAndDetectsReplay(t *testing.T) {
	e := newEnv(t)
	ada := e.register("ada")

	res, body := e.do(http.MethodPost, "/v1/auth/refresh", "", map[string]any{
		"refreshToken": ada.refreshToken,
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("refresh: got %d, body %v", res.StatusCode, body)
	}

	rotated := str(body["refreshToken"])
	if rotated == ada.refreshToken {
		t.Fatal("refresh returned the same refresh token; the contract requires rotation")
	}

	// The old token is spent. Presenting it again looks like a leak.
	replay, replayBody := e.do(http.MethodPost, "/v1/auth/refresh", "", map[string]any{
		"refreshToken": ada.refreshToken,
	})
	assertError(t, replay, replayBody, http.StatusUnauthorized, httpx.CodeTokenInvalid)

	// ...and that revokes the successor too.
	after, afterBody := e.do(http.MethodPost, "/v1/auth/refresh", "", map[string]any{
		"refreshToken": rotated,
	})
	assertError(t, after, afterBody, http.StatusUnauthorized, httpx.CodeTokenInvalid)
}

func TestLogoutEndsTheSession(t *testing.T) {
	e := newEnv(t)
	ada := e.register("ada")

	res, _ := e.do(http.MethodPost, "/v1/auth/logout", ada.accessToken, map[string]any{
		"refreshToken": ada.refreshToken,
	})
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: got %d, want 204", res.StatusCode)
	}

	after, body := e.do(http.MethodPost, "/v1/auth/refresh", "", map[string]any{
		"refreshToken": ada.refreshToken,
	})
	assertError(t, after, body, http.StatusUnauthorized, httpx.CodeTokenInvalid)
}

// Only the owner sees their email address.
func TestPublicProfileOmitsEmail(t *testing.T) {
	e := newEnv(t)
	ada := e.register("ada")
	grace := e.register("grace")

	_, mine := e.do(http.MethodGet, "/v1/users/me", ada.accessToken, nil)
	if str(mine["email"]) == "" {
		t.Error("GET /users/me should include the email")
	}

	_, theirs := e.do(http.MethodGet, "/v1/users/"+grace.username, ada.accessToken, nil)
	if _, present := theirs["email"]; present {
		t.Errorf("GET /users/{username} leaked an email: %v", theirs)
	}
	if str(theirs["username"]) != "grace" {
		t.Errorf("username: got %q, want grace", str(theirs["username"]))
	}
}

func TestGetUnknownUserIs404(t *testing.T) {
	e := newEnv(t)
	ada := e.register("ada")

	res, body := e.do(http.MethodGet, "/v1/users/nobody", ada.accessToken, nil)
	assertError(t, res, body, http.StatusNotFound, httpx.CodeNotFound)
}

// The literal /users/me route must win over /users/{username}.
func TestMeRouteIsNotShadowedByTheUsernameRoute(t *testing.T) {
	e := newEnv(t)
	ada := e.register("ada")

	_, body := e.do(http.MethodGet, "/v1/users/me", ada.accessToken, nil)
	if str(body["id"]) != ada.id {
		t.Errorf("GET /users/me returned %v, want the authenticated user", body)
	}
}

func TestPatchMePartialUpdate(t *testing.T) {
	e := newEnv(t)
	ada := e.register("ada")

	res, body := e.do(http.MethodPatch, "/v1/users/me", ada.accessToken, map[string]any{
		"bio": "Building things.",
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("patch: got %d, body %v", res.StatusCode, body)
	}
	if got := str(body["bio"]); got != "Building things." {
		t.Errorf("bio: got %q", got)
	}
	// displayName was not in the body, so it must be untouched.
	if got := str(body["displayName"]); got != "Ada" {
		t.Errorf("displayName: got %q, want it left alone", got)
	}
}

// The contract makes username immutable after registration, so a username in a
// PATCH body must not take effect.
func TestPatchMeCannotChangeUsername(t *testing.T) {
	e := newEnv(t)
	ada := e.register("ada")

	_, body := e.do(http.MethodPatch, "/v1/users/me", ada.accessToken, map[string]any{
		"username": "someone_else",
	})
	if got := str(body["username"]); got != "ada" {
		t.Errorf("username: got %q, want it unchanged", got)
	}
}

func TestAvatarLifecycle(t *testing.T) {
	e := newEnv(t)
	ada := e.register("ada")
	key := e.presign(ada, "avatar")

	res, body := e.do(http.MethodPatch, "/v1/users/me", ada.accessToken, map[string]any{
		"avatarKey": key,
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("setting the avatar: got %d, body %v", res.StatusCode, body)
	}
	if got := str(body["avatarUrl"]); !strings.HasSuffix(got, key) {
		t.Errorf("avatarUrl: got %q, want a URL ending in the uploaded key", got)
	}

	// An explicit null removes it.
	_, cleared := e.do(http.MethodPatch, "/v1/users/me", ada.accessToken, map[string]any{
		"avatarKey": nil,
	})
	if cleared["avatarUrl"] != nil {
		t.Errorf("avatarUrl: got %v, want null after sending an explicit null", cleared["avatarUrl"])
	}
}

func TestAvatarKeyMustBelongToTheCaller(t *testing.T) {
	e := newEnv(t)
	ada := e.register("ada")
	grace := e.register("grace")

	stolen := e.presign(grace, "avatar")

	res, body := e.do(http.MethodPatch, "/v1/users/me", ada.accessToken, map[string]any{
		"avatarKey": stolen,
	})
	assertError(t, res, body, http.StatusForbidden, httpx.CodeForbidden)
}

// A key presigned for an avatar must not be attachable to a post, or the size
// limit for the wrong purpose has been applied.
func TestUploadKeyIsBoundToItsPurpose(t *testing.T) {
	e := newEnv(t)
	ada := e.register("ada")
	avatarKey := e.presign(ada, "avatar")

	res, body := e.do(http.MethodPost, "/v1/posts", ada.accessToken, map[string]any{
		"imageKey": avatarKey,
		"caption":  "Wrong purpose.",
	})
	assertError(t, res, body, http.StatusForbidden, httpx.CodeForbidden)
}

func TestUploadKeyCannotBeReused(t *testing.T) {
	e := newEnv(t)
	ada := e.register("ada")
	key := e.presign(ada, "post")

	first, body := e.do(http.MethodPost, "/v1/posts", ada.accessToken, map[string]any{"imageKey": key})
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first post: got %d, body %v", first.StatusCode, body)
	}

	second, secondBody := e.do(http.MethodPost, "/v1/posts", ada.accessToken, map[string]any{"imageKey": key})
	assertError(t, second, secondBody, http.StatusForbidden, httpx.CodeForbidden)
}

func TestPresignConstraints(t *testing.T) {
	e := newEnv(t)
	ada := e.register("ada")

	t.Run("returns a url, key and expiry", func(t *testing.T) {
		res, body := e.do(http.MethodPost, "/v1/uploads/presign", ada.accessToken, map[string]any{
			"contentType": "image/jpeg", "contentLength": 843201, "purpose": "post",
		})
		if res.StatusCode != http.StatusOK {
			t.Fatalf("got %d, body %v", res.StatusCode, body)
		}
		for _, field := range []string{"uploadUrl", "key", "expiresAt"} {
			if str(body[field]) == "" {
				t.Errorf("response is missing %q", field)
			}
		}
	})

	t.Run("rejects a disallowed media type", func(t *testing.T) {
		res, body := e.do(http.MethodPost, "/v1/uploads/presign", ada.accessToken, map[string]any{
			"contentType": "image/gif", "contentLength": 1000, "purpose": "post",
		})
		assertError(t, res, body, http.StatusUnsupportedMediaType, httpx.CodeUnsupportedMediaType)
	})

	t.Run("rejects an avatar over 5 MB", func(t *testing.T) {
		res, body := e.do(http.MethodPost, "/v1/uploads/presign", ada.accessToken, map[string]any{
			"contentType": "image/jpeg", "contentLength": 5<<20 + 1, "purpose": "avatar",
		})
		assertError(t, res, body, http.StatusRequestEntityTooLarge, httpx.CodeFileTooLarge)
	})

	t.Run("allows a post image between the two limits", func(t *testing.T) {
		res, body := e.do(http.MethodPost, "/v1/uploads/presign", ada.accessToken, map[string]any{
			"contentType": "image/jpeg", "contentLength": 7 << 20, "purpose": "post",
		})
		if res.StatusCode != http.StatusOK {
			t.Errorf("7 MB post image: got %d, want 200 (body %v)", res.StatusCode, body)
		}
	})

	t.Run("rejects a post image over 10 MB", func(t *testing.T) {
		res, body := e.do(http.MethodPost, "/v1/uploads/presign", ada.accessToken, map[string]any{
			"contentType": "image/jpeg", "contentLength": 10<<20 + 1, "purpose": "post",
		})
		assertError(t, res, body, http.StatusRequestEntityTooLarge, httpx.CodeFileTooLarge)
	})

	t.Run("rejects an unknown purpose", func(t *testing.T) {
		res, body := e.do(http.MethodPost, "/v1/uploads/presign", ada.accessToken, map[string]any{
			"contentType": "image/jpeg", "contentLength": 1000, "purpose": "banner",
		})
		envelope := assertError(t, res, body, http.StatusBadRequest, httpx.CodeValidationFailed)
		if details(t, envelope)["purpose"] == nil {
			t.Error("details should name the purpose field")
		}
	})
}

func TestCreatePostRequiresAnImageKey(t *testing.T) {
	e := newEnv(t)
	ada := e.register("ada")

	res, body := e.do(http.MethodPost, "/v1/posts", ada.accessToken, map[string]any{
		"caption": "No image attached.",
	})
	envelope := assertError(t, res, body, http.StatusBadRequest, httpx.CodeValidationFailed)
	if details(t, envelope)["imageKey"] == nil {
		t.Error("details should name imageKey")
	}
}

func TestCreatePostRejectsAnOversizedCaption(t *testing.T) {
	e := newEnv(t)
	ada := e.register("ada")

	res, body := e.do(http.MethodPost, "/v1/posts", ada.accessToken, map[string]any{
		"imageKey": e.presign(ada, "post"),
		"caption":  strings.Repeat("a", 2201),
	})
	envelope := assertError(t, res, body, http.StatusBadRequest, httpx.CodeValidationFailed)
	if details(t, envelope)["caption"] == nil {
		t.Error("details should name caption")
	}
}

func TestPostResponseShape(t *testing.T) {
	e := newEnv(t)
	ada := e.register("ada")
	postID := e.createPost(ada, "Sunset from the office.")

	_, body := e.do(http.MethodGet, "/v1/posts/"+postID, ada.accessToken, nil)

	if str(body["imageUrl"]) == "" {
		t.Error("post is missing imageUrl")
	}
	if _, present := body["imageKey"]; present {
		t.Error("post exposes the internal storage key; clients should only see a URL")
	}

	author, ok := body["author"].(map[string]any)
	if !ok {
		t.Fatalf("post has no author object: %v", body)
	}
	if str(author["username"]) != "ada" {
		t.Errorf("author.username: got %q", str(author["username"]))
	}
	if _, present := author["email"]; present {
		t.Error("the post author leaked an email address")
	}
}

// The feed is the endpoint the client scrolls, so paging has to reach every
// post exactly once even when many share a timestamp.
func TestFeedPaginationVisitsEveryPostExactlyOnce(t *testing.T) {
	e := newEnv(t)
	ada := e.register("ada")

	const total = 60
	for i := range total {
		e.createPost(ada, fmt.Sprintf("post %d", i))
	}

	seen := make(map[string]int)
	cursor := ""
	pages := 0

	for {
		path := "/v1/posts?limit=20"
		if cursor != "" {
			path += "&cursor=" + cursor
		}

		res, body := e.do(http.MethodGet, path, ada.accessToken, nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("feed page %d: got %d, body %v", pages, res.StatusCode, body)
		}

		items, ok := body["items"].([]any)
		if !ok {
			t.Fatalf("feed page has no items array: %v", body)
		}
		for _, item := range items {
			seen[str(item.(map[string]any)["id"])]++
		}

		pages++
		if body["nextCursor"] == nil {
			break
		}
		cursor = str(body["nextCursor"])

		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
	}

	if len(seen) != total {
		t.Errorf("saw %d distinct posts across %d pages, want %d", len(seen), pages, total)
	}
	for id, count := range seen {
		if count > 1 {
			t.Errorf("post %s appeared %d times across pages", id, count)
		}
	}
}

func TestFeedIsNewestFirst(t *testing.T) {
	e := newEnv(t)
	ada := e.register("ada")

	e.createPost(ada, "older")
	time.Sleep(5 * time.Millisecond)
	e.createPost(ada, "newer")

	_, body := e.do(http.MethodGet, "/v1/posts", ada.accessToken, nil)
	items := body["items"].([]any)

	if len(items) != 2 {
		t.Fatalf("got %d posts, want 2", len(items))
	}
	if got := str(items[0].(map[string]any)["caption"]); got != "newer" {
		t.Errorf("first item caption: got %q, want the newest post", got)
	}
}

func TestEmptyFeedReturnsAnArrayNotNull(t *testing.T) {
	e := newEnv(t)
	ada := e.register("ada")

	_, body := e.do(http.MethodGet, "/v1/posts", ada.accessToken, nil)

	items, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("items is %T, want an array even when empty", body["items"])
	}
	if len(items) != 0 {
		t.Errorf("got %d items, want none", len(items))
	}
	if body["nextCursor"] != nil {
		t.Errorf("nextCursor: got %v, want null on the last page", body["nextCursor"])
	}
}

func TestFeedRejectsACursorWeDidNotIssue(t *testing.T) {
	e := newEnv(t)
	ada := e.register("ada")

	res, body := e.do(http.MethodGet, "/v1/posts?cursor=not-a-real-cursor", ada.accessToken, nil)
	assertError(t, res, body, http.StatusBadRequest, httpx.CodeValidationFailed)
}

func TestFeedLimitIsClampedRatherThanRejected(t *testing.T) {
	e := newEnv(t)
	ada := e.register("ada")

	for range 55 {
		e.createPost(ada, "post")
	}

	res, body := e.do(http.MethodGet, "/v1/posts?limit=500", ada.accessToken, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200 (body %v)", res.StatusCode, body)
	}
	if got := len(body["items"].([]any)); got != store.MaxPageLimit {
		t.Errorf("got %d items, want the maximum of %d", got, store.MaxPageLimit)
	}
}

func TestUserPostsAreScopedToThatUser(t *testing.T) {
	e := newEnv(t)
	ada := e.register("ada")
	grace := e.register("grace")

	e.createPost(ada, "from ada")
	e.createPost(ada, "also from ada")
	e.createPost(grace, "from grace")

	_, body := e.do(http.MethodGet, "/v1/users/ada/posts", grace.accessToken, nil)
	items := body["items"].([]any)

	if len(items) != 2 {
		t.Fatalf("got %d posts for ada, want 2", len(items))
	}
	for _, item := range items {
		author := item.(map[string]any)["author"].(map[string]any)
		if str(author["username"]) != "ada" {
			t.Errorf("grid contains a post by %q", str(author["username"]))
		}
	}
}

func TestDeletePost(t *testing.T) {
	e := newEnv(t)
	ada := e.register("ada")
	grace := e.register("grace")

	t.Run("someone else's post is forbidden", func(t *testing.T) {
		postID := e.createPost(ada, "mine")

		res, body := e.do(http.MethodDelete, "/v1/posts/"+postID, grace.accessToken, nil)
		assertError(t, res, body, http.StatusForbidden, httpx.CodeForbidden)

		// ...and it is still there.
		check, _ := e.do(http.MethodGet, "/v1/posts/"+postID, ada.accessToken, nil)
		if check.StatusCode != http.StatusOK {
			t.Errorf("the post was removed by a forbidden delete: got %d", check.StatusCode)
		}
	})

	t.Run("own post succeeds and is then gone", func(t *testing.T) {
		postID := e.createPost(ada, "to be deleted")

		res, _ := e.do(http.MethodDelete, "/v1/posts/"+postID, ada.accessToken, nil)
		if res.StatusCode != http.StatusNoContent {
			t.Fatalf("delete: got %d, want 204", res.StatusCode)
		}

		after, body := e.do(http.MethodGet, "/v1/posts/"+postID, ada.accessToken, nil)
		assertError(t, after, body, http.StatusNotFound, httpx.CodeNotFound)
	})

	t.Run("unknown post is not found", func(t *testing.T) {
		res, body := e.do(http.MethodDelete, "/v1/posts/does-not-exist", ada.accessToken, nil)
		assertError(t, res, body, http.StatusNotFound, httpx.CodeNotFound)
	})
}

// Even an unrouted path answers in the error envelope, so the client never has
// to cope with an HTML error page.
func TestUnknownRouteReturnsTheErrorEnvelope(t *testing.T) {
	e := newEnv(t)

	res, body := e.do(http.MethodGet, "/v1/nope", "", nil)
	assertError(t, res, body, http.StatusNotFound, httpx.CodeNotFound)

	if got := res.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type: got %q, want JSON", got)
	}
}

func TestMalformedJSONIsAValidationFailureNotACrash(t *testing.T) {
	e := newEnv(t)

	req, err := http.NewRequest(http.MethodPost, e.server.URL+"/v1/auth/login",
		strings.NewReader(`{"email": "ada@example.com",`))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := e.server.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("got %d, want 400", res.StatusCode)
	}
}

func TestHealthAndReadinessAreOpen(t *testing.T) {
	e := newEnv(t)

	for _, path := range []string{"/healthz", "/readyz"} {
		res, body := e.do(http.MethodGet, path, "", nil)
		if res.StatusCode != http.StatusOK {
			t.Errorf("%s: got %d, want 200 (body %v)", path, res.StatusCode, body)
		}
	}
}

// The health body is what an on-call engineer reads first, so every field it
// promises has to actually be there.
func TestHealthReportsTheFullBody(t *testing.T) {
	e := newEnv(t)

	for _, path := range []string{"/healthz", "/readyz"} {
		t.Run(path, func(t *testing.T) {
			_, body := e.do(http.MethodGet, path, "", nil)

			for _, field := range []string{"status", "service", "version", "goVersion", "environment", "time", "uptimeSeconds"} {
				if _, present := body[field]; !present {
					t.Errorf("missing %q: %v", field, body)
				}
			}

			if got := str(body["status"]); got != "ok" {
				t.Errorf("status: got %q, want ok", got)
			}
			if got := str(body["service"]); got != "domieface-server" {
				t.Errorf("service: got %q", got)
			}
			if got := str(body["environment"]); got != "test" {
				t.Errorf("environment: got %q, want the configured environment", got)
			}
			if got := str(body["version"]); got == "" {
				t.Error("version is empty; it should fall back to a revision or dev")
			}

			format := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)
			if got := str(body["time"]); !format.MatchString(got) {
				t.Errorf("time: got %q, want the contract timestamp format", got)
			}
			if _, ok := body["uptimeSeconds"].(float64); !ok {
				t.Errorf("uptimeSeconds: got %T, want a number", body["uptimeSeconds"])
			}
		})
	}
}

// Liveness must not report dependency checks: it deliberately makes none, and
// claiming otherwise would invite someone to alert on them.
func TestLivenessReportsNoChecks(t *testing.T) {
	e := newEnv(t)

	_, body := e.do(http.MethodGet, "/healthz", "", nil)
	if checks, present := body["checks"]; present {
		t.Errorf("healthz reported checks it never ran: %v", checks)
	}
}

func TestReadinessReportsTheDatabaseCheck(t *testing.T) {
	e := newEnv(t)

	_, body := e.do(http.MethodGet, "/readyz", "", nil)

	checks, ok := body["checks"].([]any)
	if !ok || len(checks) == 0 {
		t.Fatalf("readyz reported no checks: %v", body)
	}

	database, ok := checks[0].(map[string]any)
	if !ok {
		t.Fatalf("check is not an object: %v", checks[0])
	}
	if got := str(database["name"]); got != "database" {
		t.Errorf("check name: got %q, want database", got)
	}
	if got := str(database["status"]); got != "ok" {
		t.Errorf("check status: got %q, want ok", got)
	}
	if _, ok := database["latencyMs"].(float64); !ok {
		t.Errorf("latencyMs: got %T, want a number", database["latencyMs"])
	}
	if _, present := database["error"]; present {
		t.Errorf("a passing check should carry no error field: %v", database)
	}
}

// The failure path is the one that matters: a readiness probe that cannot go
// red is decoration. 503 is what actually removes the instance from rotation.
func TestReadinessGoes503WhenTheDatabaseIsDown(t *testing.T) {
	e := newEnvWithStore(t, unreachableStore{})

	res, body := e.do(http.MethodGet, "/readyz", "", nil)

	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d, want 503 (body %v)", res.StatusCode, body)
	}
	if got := str(body["status"]); got != "degraded" {
		t.Errorf("status: got %q, want degraded", got)
	}

	checks, ok := body["checks"].([]any)
	if !ok || len(checks) == 0 {
		t.Fatalf("no checks reported: %v", body)
	}
	database := checks[0].(map[string]any)
	if got := str(database["status"]); got != "degraded" {
		t.Errorf("check status: got %q, want degraded", got)
	}
	if str(database["error"]) == "" {
		t.Error("a failed check should say what went wrong")
	}
}

// Liveness must stay green while a dependency is down, or the orchestrator
// restarts healthy instances during a database outage.
func TestLivenessStaysGreenWhenTheDatabaseIsDown(t *testing.T) {
	e := newEnvWithStore(t, unreachableStore{})

	res, body := e.do(http.MethodGet, "/healthz", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200 (body %v)", res.StatusCode, body)
	}
	if got := str(body["status"]); got != "ok" {
		t.Errorf("status: got %q, want ok", got)
	}
}
