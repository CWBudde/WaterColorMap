package datasource

import (
	"net/http"
)

// DefaultUserAgent identifies this project to Overpass servers.
//
// It is not cosmetic. The public overpass-api.de rejects Go's default
// `User-Agent: Go-http-client/1.1` with 406 Not Acceptable — the same query
// from curl returns 200 in about half a second. go-overpass never sets a UA
// (its httpPost sets only Content-Type), so every request this project made to
// the public API arrived with the rejected default. That 406 was previously
// attributed to rate limiting; it was not.
//
// Overpass's usage policy also asks for a contactable identifier, so the URL is
// part of the string rather than decoration.
const DefaultUserAgent = "WaterColorMap/1.0 (+https://github.com/cwbudde/watercolormap)"

// userAgentTransport sets a User-Agent on every outgoing request.
//
// It lives at the RoundTripper layer for the same reason limitedTransport and
// cachingTransport do: the request is built inside the go-overpass client, not
// at the call site, so there is nowhere else to reach it. Doing it here also
// covers every client the repo builds, including the per-server clients
// MultiOverpassDataSource constructs, without needing a release of the
// dependency.
type userAgentTransport struct {
	base      http.RoundTripper
	userAgent string
}

func (t *userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}

	// A caller that already set a UA meant it; leave it alone. Note that
	// net/http fills in its own default at write time, not here, so an absent
	// header at this point genuinely means "nobody chose one".
	if t.userAgent == "" || req.Header.Get("User-Agent") != "" {
		return base.RoundTrip(req)
	}

	// RoundTrippers must not modify the request they are given, so clone it.
	// The clone is shallow apart from the header map, which is exactly what
	// needs to differ.
	clone := req.Clone(req.Context())
	clone.Header.Set("User-Agent", t.userAgent)
	return base.RoundTrip(clone)
}

// withUserAgent returns a copy of client that identifies itself with ua.
// An empty ua leaves the client unchanged.
func withUserAgent(client *http.Client, ua string) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	if ua == "" {
		return client
	}

	identified := *client
	identified.Transport = &userAgentTransport{base: client.Transport, userAgent: ua}
	return &identified
}
