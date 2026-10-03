package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// hi box --data: the box reaches the hi server's Hugging Face proxy through
// a listener on hi's box proxy, which puts a hi data token in place of the
// box's placeholder. The token stays in the box's state folder on the host,
// mounted only into the proxy.

const boxDataTokenFile = "data-token"

// boxDataHosts are where hf fetches file contents: the CDN and Xet storage.
var boxDataHosts = []string{"cdn.hf.co", "xethub.hf.co"}

type boxData struct {
	upstream string // the server's /hf URL
	host     string // the server's host name
	address  string // its address as the host resolves it
}

// prepareBoxData asks the server for a hi data token for every repository
// the user's group may read, and stores it for the proxy.
func prepareBoxData(stateDir string) (*boxData, error) {
	client, connection, err := dataClient()
	if err != nil {
		return nil, fmt.Errorf("--data: %w", err)
	}
	token, endpoint, err := dataToken(client, connection, "*")
	if err != nil {
		return nil, fmt.Errorf("--data: %w", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, boxDataTokenFile), []byte(token+"\n"), 0o600); err != nil {
		return nil, err
	}
	setup := &boxData{upstream: endpoint}
	if parsed, err := url.Parse(endpoint); err == nil {
		setup.host = parsed.Hostname()
		if net.ParseIP(setup.host) == nil {
			if addresses, err := net.LookupHost(setup.host); err == nil && len(addresses) > 0 {
				setup.address = addresses[0]
			}
		}
	}
	return setup, nil
}

// newBoxDataInjector passes the box's Hugging Face calls on to the
// server's proxy with the hi data token, and keeps the server's links
// pointing back at this listener.
func newBoxDataInjector(tokenFile, upstream string, log *boxNetworkLog) http.Handler {
	target, err := url.Parse(strings.TrimRight(upstream, "/"))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err != nil {
			http.Error(w, "hi box: the hi server's address is not valid", http.StatusBadGateway)
			return
		}
		data, readErr := os.ReadFile(tokenFile)
		token := strings.TrimSpace(string(data))
		if readErr != nil || token == "" {
			log.record("data-problem", "no hi data token")
			http.Error(w, "hi box: no hi data token; start the box again with --data", http.StatusBadGateway)
			return
		}
		inbound := "http://" + r.Host
		proxy := &httputil.ReverseProxy{
			Rewrite: func(out *httputil.ProxyRequest) {
				out.Out.URL.Scheme, out.Out.URL.Host = target.Scheme, target.Host
				out.Out.URL.Path = target.Path + r.URL.Path
				out.Out.URL.RawPath = ""
				if r.URL.RawPath != "" {
					out.Out.URL.RawPath = target.Path + r.URL.RawPath
				}
				out.Out.Host = target.Host
				if strings.Contains(r.Header.Get("Authorization"), boxClaudePlacehold) {
					out.Out.Header.Set("Authorization", "Bearer "+token)
				}
			},
			ModifyResponse: func(response *http.Response) error {
				base := target.String()
				if location := response.Header.Get("Location"); strings.HasPrefix(location, base) {
					response.Header.Set("Location", inbound+strings.TrimPrefix(location, base))
				}
				if links := response.Header.Values("Link"); len(links) > 0 {
					response.Header.Del("Link")
					for _, link := range links {
						response.Header.Add("Link", strings.ReplaceAll(link, base, inbound))
					}
				}
				return nil
			},
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
				log.record("data-problem", err.Error())
				http.Error(w, "hi box: the hi server can't be reached: "+err.Error(), http.StatusBadGateway)
			},
			FlushInterval: -1,
		}
		log.record("data", r.URL.Path)
		proxy.ServeHTTP(w, r)
	})
}
