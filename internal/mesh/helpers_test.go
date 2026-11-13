package mesh

import "net/url"

type urlT = url.URL

func parseURL(s string) (*url.URL, error) { return url.Parse(s) }
