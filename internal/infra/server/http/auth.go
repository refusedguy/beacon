// Package httproute holds the HTTP transport of the Beacon server. It owns the
// router, middleware and handlers and keeps them separate from the domain.
package httproute

import (
	"crypto/subtle"
	nethttp "net/http"
)

// bearerScheme is the HTTP authentication scheme accepted by the middleware.
const bearerScheme = "Bearer"

// Bearer returns middleware that lets a request through only when its
// Authorization header matches "Bearer <token>" exactly. Other requests get
// 401 and never reach next. The token is compared in constant time, so a caller
// cannot recover it from the time the server takes to reject a bad header.
func Bearer(token string) func(nethttp.Handler) nethttp.Handler {
	want := bearerScheme + " " + token
	return func(next nethttp.Handler) nethttp.Handler {
		return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
			got := r.Header.Get("Authorization")
			if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
				w.Header().Set("WWW-Authenticate", bearerScheme)
				nethttp.Error(w, "unauthorized", nethttp.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
