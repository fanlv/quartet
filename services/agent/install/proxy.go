package install

import (
	"net/http"
	"net/url"

	"golang.org/x/net/http/httpproxy"
)

// Internal steps do their own outbound requests, so they have to resolve the
// agent's proxy themselves instead of inheriting it like a child process. The
// lookup follows the convention every CLI in this project follows (curl and
// npm included): lower-case variables win, https falls back to http_proxy,
// and all_proxy covers both schemes. no_proxy is matched with the same rules
// net/http uses for the process environment.
func httpClientForEnv(env map[string]string, checkRedirect func(*http.Request, []*http.Request) error) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if proxy := proxyFunc(env); proxy != nil {
		transport.Proxy = func(request *http.Request) (*url.URL, error) {
			return proxy(request.URL)
		}
	}
	return &http.Client{Transport: transport, CheckRedirect: checkRedirect}
}

func proxyFunc(env map[string]string) func(*url.URL) (*url.URL, error) {
	httpProxy := envValue(env, "http_proxy", "HTTP_PROXY", "all_proxy", "ALL_PROXY")
	httpsProxy := envValue(env, "https_proxy", "HTTPS_PROXY", "all_proxy", "ALL_PROXY")
	if httpsProxy == "" {
		httpsProxy = httpProxy
	}
	if httpProxy == "" && httpsProxy == "" {
		return nil
	}
	config := &httpproxy.Config{
		HTTPProxy:  httpProxy,
		HTTPSProxy: httpsProxy,
		NoProxy:    envValue(env, "no_proxy", "NO_PROXY"),
	}
	return config.ProxyFunc()
}

func envValue(env map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := env[key]; value != "" {
			return value
		}
	}
	return ""
}
