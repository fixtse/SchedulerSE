package adapters

import (
	"errors"
	"testing"

	"github.com/fixtse/SchedulerSE/internal/domain"
)

func TestCredentialOrigins(t *testing.T) {
	for _, tc := range []struct {
		old, next, auth string
		reject          bool
	}{
		{"https://one.example/a", "https://one.example/b", "secret", false},
		{"https://one.example/a", "https://other.example/a", "secret", true},
		{"https://one.example/a", "http://one.example/a", "secret", true},
		{"https://one.example:443/a", "https://one.example:444/a", "secret", true},
		{"https://one.example/a", "https://other.example/a", "", false},
		{"https://one.example/a", "https://other.example/a", "replacement", false},
	} {
		err := ValidateCredentialDestination(tc.old, tc.next, "secret", tc.auth)
		if (err != nil) != tc.reject {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
}

func TestStoredCredentialsCannotMoveAcrossOrigins(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	if err := s.SaveAgentWebhook("https://one.example/hook", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAgentWebhook("https://other.example/hook", "secret"); err == nil {
		t.Fatal("webhook secret moved")
	}
	job := domain.Job{ID: "bound", Name: "Bound", URL: "https://one.example", Authorization: "secret", Body: []byte(`{}`), Policy: "generic", FallbackURL: "https://backup.example", FallbackAuth: "fallback-secret"}
	if err := s.Save(job, true); err != nil {
		t.Fatal(err)
	}
	job, _ = s.Job(job.ID)
	job.URL = "https://other.example"
	if err := s.Save(job, false); err == nil {
		t.Fatal("job secret moved")
	}
	job.URL = "https://one.example"
	job.FallbackURL = "https://other.example"
	if err := s.Save(job, false); err == nil {
		t.Fatal("fallback secret moved")
	}
}

func TestStorageQuotas(t *testing.T) {
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	if _, err := s.DB.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<999)
 INSERT INTO jobs(id,name,url,authorization,body,cron,enabled,policy) SELECT 'j-'||x,'Quota','','','{}','',0,'generic' FROM n`); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(domain.Job{ID: "last-allowed", Name: "Quota", Body: []byte(`{}`)}, true); err != nil {
		t.Fatal("last available slot rejected", err)
	}
	if err := s.Save(domain.Job{ID: "overflow", Body: []byte(`{}`)}, true); !errors.Is(err, ErrCapacity) {
		t.Fatal("job quota missing", err)
	}
	if _, err := s.DB.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1000) INSERT INTO agent_tasks(id,kind,title,message,context,run_at,interval_seconds) SELECT 't-'||x,'timer','quota','','{}',1,0 FROM n`); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAgentTask(domain.AgentTask{ID: "overflow", Context: []byte(`{}`)}); !errors.Is(err, ErrCapacity) {
		t.Fatal("schedule quota missing", err)
	}
	var pages int
	if err := s.DB.QueryRow("PRAGMA max_page_count").Scan(&pages); err != nil || pages != 32768 {
		t.Fatal("database growth unbounded", pages, err)
	}
}

func TestSecurityConfigDefaultsAndValidation(t *testing.T) {
	t.Setenv("ENABLE_BASH", "false")
	t.Setenv("ENABLE_AGENT_BASH", "false")
	t.Setenv("TRUSTED_PROXY_CIDRS", "")
	c, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.EnableBash || c.EnableAgentBash {
		t.Fatal("unsafe defaults")
	}
	t.Setenv("ENABLE_AGENT_BASH", "true")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("agent Bash enabled without service Bash")
	}
	t.Setenv("ENABLE_AGENT_BASH", "invalid")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("invalid switch accepted")
	}
	t.Setenv("ENABLE_AGENT_BASH", "false")
	t.Setenv("TRUSTED_PROXY_CIDRS", "10.0.0.0/24,127.0.0.1/32")
	c, err = LoadConfig()
	if err != nil || len(c.TrustedProxies) != 2 {
		t.Fatal(c.TrustedProxies, err)
	}
	t.Setenv("TRUSTED_PROXY_CIDRS", "not-a-cidr")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("invalid proxy network accepted")
	}
}
