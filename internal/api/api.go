package api

import (
	"net/http"
	"time"

	"domieface/com/internal/auth"
	"domieface/com/internal/config"
	"domieface/com/internal/httpx"
	"domieface/com/internal/store"
)

// Server holds everything the handlers need. It is constructed once at startup.
type Server struct {
	cfg       *config.Config
	store     store.Store
	sessions  *auth.Service
	issuer    *auth.TokenIssuer
	presigner Presigner

	limiter     *httpx.RateLimiter
	authLimiter *httpx.RateLimiter

	// startedAt backs the uptime reported by the health endpoints.
	startedAt time.Time
}

// New builds the API server.
func New(cfg *config.Config, st store.Store, sessions *auth.Service, issuer *auth.TokenIssuer, presigner Presigner) *Server {
	return &Server{
		cfg:       cfg,
		store:     st,
		sessions:  sessions,
		issuer:    issuer,
		presigner: presigner,

		limiter: httpx.NewRateLimiter(cfg.RateLimitPerMin, cfg.RateLimitBurst),
		// Credential endpoints get a far tighter budget than the rest of the
		// API: they are the ones worth brute-forcing.
		authLimiter: httpx.NewRateLimiter(cfg.AuthRateLimitPerMin, cfg.AuthRateLimitBurst),

		startedAt: time.Now(),
	}
}

// Limiters exposes the rate limiters so the process can reap idle buckets.
func (s *Server) Limiters() []*httpx.RateLimiter {
	return []*httpx.RateLimiter{s.limiter, s.authLimiter}
}

// Route is one entry in the routing table.
//
// The table is exported rather than being built inline in Handler so that the
// OpenAPI spec can be checked against it: TestOpenAPIMatchesRoutingTable fails
// if a route is added without documenting it, or documented without existing.
type Route struct {
	// Method is the HTTP method, e.g. http.MethodGet.
	Method string
	// Pattern is the path, with net/http wildcards, e.g. "/v1/posts/{id}".
	Pattern string
	// Public marks a route reachable without a bearer token.
	Public bool

	handler http.Handler
}

// Routes returns every route the server serves, in the order they read best.
func (s *Server) Routes() []Route {
	// Credential endpoints get a far tighter budget than the rest of the API:
	// they are the ones worth brute-forcing.
	credentials := s.authLimiter.Middleware(s.clientKey)

	routes := []Route{
		// Liveness and readiness sit outside /v1 and outside auth so an
		// orchestrator can reach them.
		{http.MethodGet, "/healthz", true, httpx.Handler(s.handleHealth)},
		{http.MethodGet, "/readyz", true, httpx.Handler(s.handleReady)},

		// Authentication. Register, login and refresh are public; on refresh the
		// refresh token is itself the credential.
		{http.MethodPost, "/v1/auth/register", true, credentials(httpx.Handler(s.handleRegister))},
		{http.MethodPost, "/v1/auth/login", true, credentials(httpx.Handler(s.handleLogin))},
		{http.MethodPost, "/v1/auth/refresh", true, credentials(httpx.Handler(s.handleRefresh))},
		{http.MethodPost, "/v1/auth/logout", false, s.protected(s.handleLogout)},

		// Users. The literal /v1/users/me pattern wins over /v1/users/{username},
		// so a user who somehow held the username "me" could not shadow it.
		{http.MethodGet, "/v1/users/me", false, s.protected(s.handleGetMe)},
		{http.MethodPatch, "/v1/users/me", false, s.protected(s.handleUpdateMe)},
		{http.MethodGet, "/v1/users/{username}", false, s.protected(s.handleGetUser)},
		{http.MethodGet, "/v1/users/{username}/posts", false, s.protected(s.handleUserPosts)},

		// Uploads.
		{http.MethodPost, "/v1/uploads/presign", false, s.protected(s.handlePresign)},

		// Posts.
		{http.MethodGet, "/v1/posts", false, s.protected(s.handleFeed)},
		{http.MethodPost, "/v1/posts", false, s.protected(s.handleCreatePost)},
		{http.MethodGet, "/v1/posts/{id}", false, s.protected(s.handleGetPost)},
		{http.MethodDelete, "/v1/posts/{id}", false, s.protected(s.handleDeletePost)},
	}

	// Documentation is public and can be switched off entirely for a deployment
	// that would rather not advertise its surface.
	if s.cfg.DocsEnabled {
		routes = append(routes,
			Route{http.MethodGet, DocsPath, true, httpx.Handler(s.handleDocs)},
			Route{http.MethodGet, OpenAPIPath, true, httpx.Handler(s.handleOpenAPISpec)},
		)
	}

	return routes
}

// Handler builds the mux from the routing table.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	for _, route := range s.Routes() {
		mux.Handle(route.Method+" "+route.Pattern, route.handler)
	}

	// Anything unmatched still answers in the error envelope, so a client's
	// error handling never has to cope with an HTML 404 page.
	mux.Handle("/", httpx.Handler(func(http.ResponseWriter, *http.Request) error {
		return httpx.ErrNotFound("That endpoint does not exist.")
	}))

	return httpx.Chain(mux,
		httpx.WithRequestID,
		httpx.WithRecovery,
		httpx.WithLogging,
		s.limiter.Middleware(s.clientKey),
	)
}

// clientKey decides what counts as one caller for rate limiting.
func (s *Server) clientKey(r *http.Request) string {
	return httpx.ClientIP(r, s.cfg.TrustProxyHeader)
}
