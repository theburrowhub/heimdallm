// export_test.go exposes internal helpers for use in _test.go files in the
// external test package (package github_test). These symbols are only compiled
// when running tests — they are not part of the package's public API.
package github

import "net/http"

// DoGETForTest calls the internal do() helper with method GET so integration
// tests can exercise the ETag cache layer directly without going through a
// higher-level method that may add its own body-reading.
func (c *Client) DoGETForTest(path, accept string) (*http.Response, error) {
	return c.do("GET", path, accept)
}

// DoDELETEForTest calls the internal do() helper with method DELETE so tests
// can verify that non-GET requests bypass the cache layer entirely.
func (c *Client) DoDELETEForTest(path, accept string) (*http.Response, error) {
	return c.do("DELETE", path, accept)
}

