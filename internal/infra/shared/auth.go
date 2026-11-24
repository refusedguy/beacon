package shared

import (
	"net/http"
	"strings"
)

// ApplyAuth sets the Authorization header on a request. A token of the form
// user:pass uses HTTP Basic; any other token uses a Bearer header.
func ApplyAuth(req *http.Request, token string) {
	if user, pass, ok := strings.Cut(token, ":"); ok {
		req.SetBasicAuth(user, pass)
	} else {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}
