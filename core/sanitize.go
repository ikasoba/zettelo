package core

import "net/url"

func PathEscape(name string) string {
	return url.PathEscape(name)
}

func PathUnescape(name string) (string, error) {
	return url.PathUnescape(name)
}
