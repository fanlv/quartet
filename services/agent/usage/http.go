package usage

import (
	"io"
	"net/http"
	"net/url"
)

// maxUsageBody caps how much of a usage response we read into memory. These
// responses are small JSON documents; the cap guards against a proxy error
// page or a misrouted response streaming without bound.
const maxUsageBody = 1 << 20 // 1 MiB

// proxyTransport builds an http.Transport whose Proxy is taken from the given
// ACP env map (https_proxy, falling back to http_proxy; both case variants).
// The single Codex usage host is never in no_proxy, so the proxy always
// applies when configured. Returns a proxy-less transport when no proxy is set.
func proxyTransport(env map[string]string) *http.Transport {
	tr := &http.Transport{}
	proxyStr := firstNonEmpty(
		env["https_proxy"], env["HTTPS_PROXY"],
		env["http_proxy"], env["HTTP_PROXY"],
	)
	if proxyStr == "" {
		return tr
	}
	u, err := url.Parse(proxyStr)
	if err != nil || u.Host == "" {
		return tr
	}
	tr.Proxy = http.ProxyURL(u)
	return tr
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func readAllLimited(resp *http.Response) ([]byte, error) {
	return io.ReadAll(io.LimitReader(resp.Body, maxUsageBody))
}
