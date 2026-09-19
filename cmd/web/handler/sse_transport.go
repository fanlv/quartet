package handler

import (
	"github.com/cloudwego/hertz/pkg/app"
	hertzConsts "github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/protocol/sse"
	"github.com/hertz-contrib/http2"
)

// isHTTP2 reports whether this request arrived on an HTTP/2 stream. The h2
// server tags every request it builds with protocol HTTP/2.0 before the
// handler runs, so this is valid from the first line of any handler.
func isHTTP2(c *app.RequestContext) bool {
	return string(c.Request.Header.GetProtocol()) == hertzConsts.HTTP20
}

// newSSEWriter builds the SSE writer for the current request's protocol.
//
// hertz' sse.NewWriter installs an HTTP/1.1 chunked body writer whenever the
// response has no hijack writer yet, and that writer emits chunk framing
// straight onto the connection. Under HTTP/2 the connection is the shared TLS
// socket carrying every stream of every other tab, so those chunk headers
// would be injected between h2 frames and break the whole connection. Install
// the h2 stream writer first; sse.NewWriter then adopts it and each event goes
// out as DATA frames on this stream alone.
//
// On HTTP/1.1 NewResponseWriter rejects the connection and we fall through to
// the unchanged chunked path.
func newSSEWriter(c *app.RequestContext) *sse.Writer {
	if c.Response.GetHijackWriter() == nil {
		if w, err := http2.NewResponseWriter(c.GetConn()); err == nil {
			c.Response.HijackWriter(w)
		}
	}
	return sse.NewWriter(c)
}
