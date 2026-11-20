package httproute

import "net/http"

// Handler serves one route. A non-nil error makes the router write the
// matching JSON error response through WriteError.
type Handler func(http.ResponseWriter, *http.Request) error

type route struct {
	method string
	path   string
}

// Router dispatches requests to handlers registered under one path prefix.
// Routes are matched by method and exact path.
type Router struct {
	prefix string
	routes map[route]Handler
}

// New returns an empty Router whose routes are mounted under prefix. An empty
// prefix mounts routes at the root. A trailing slash is removed.
func New(prefix string) *Router {
	if len(prefix) > 0 && prefix[len(prefix)-1] == '/' {
		prefix = prefix[:len(prefix)-1]
	}
	return &Router{prefix: prefix, routes: make(map[route]Handler)}
}

// Handle registers h for method at path. The full path is prefix + path.
func (r *Router) Handle(method, path string, h Handler) {
	r.routes[route{method: method, path: r.prefix + path}] = h
}

// GET registers a handler for GET requests.
func (r *Router) GET(path string, h Handler) {
	r.Handle(http.MethodGet, path, h)
}

// POST registers a handler for POST requests.
func (r *Router) POST(path string, h Handler) {
	r.Handle(http.MethodPost, path, h)
}

// ServeHTTP implements http.Handler. A known path with an unknown method gets
// a JSON 405; an unknown path gets a JSON 404.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	h, ok := r.routes[route{method: req.Method, path: req.URL.Path}]
	if ok {
		if err := h(w, req); err != nil {
			WriteError(w, err)
		}
		return
	}
	if r.pathKnown(req.URL.Path) {
		WriteError(w, NewError(CodeMethodNotAllowed, "method %s not allowed for %s", req.Method, req.URL.Path))
		return
	}
	WriteError(w, NewError(CodeNotFound, "no route for %s", req.URL.Path))
}

func (r *Router) pathKnown(path string) bool {
	for rt := range r.routes {
		if rt.path == path {
			return true
		}
	}
	return false
}
