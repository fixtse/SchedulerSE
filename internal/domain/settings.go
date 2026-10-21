package domain

// AppSettings are installation-wide defaults managed through the web interface.
type AppSettings struct {
	Timezone     string `json:"timezone"`
	TimeoutMS    int    `json:"request_timeout_ms"`
	CookieSecure bool   `json:"cookie_secure"`
}
