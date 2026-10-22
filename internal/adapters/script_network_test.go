package adapters

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestScriptCanCallHTTPAndHTTPS(t *testing.T) {
	requireSandbox(t)
	if _, err := exec.LookPath("curl"); err != nil {
		if os.Getenv("REQUIRE_SCRIPT_SANDBOX") == "true" {
			t.Fatal("curl is required for network regression tests")
		}
		t.Skip("curl is not installed")
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("unexpected method %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("missing JSON header")
		}
		_, _ = w.Write([]byte("network-ok"))
	})
	plain := httptest.NewServer(handler)
	defer plain.Close()
	secure := httptest.NewTLSServer(handler)
	defer secure.Close()
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: secure.Certificate().Raw})
	for _, tc := range []struct{ name, url, prefix, extra string }{
		{name: "http-with-name-resolution", url: strings.Replace(plain.URL, "127.0.0.1", "localhost", 1)},
		{name: "https-with-certificate-verification", url: secure.URL, prefix: "cat > /tmp/test-ca.pem <<'TEST_CERT'\n" + string(cert) + "TEST_CERT\n", extra: "--cacert /tmp/test-ca.pem"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			script := tc.prefix + "curl --fail --silent --show-error --noproxy '*' --max-time 3 " + tc.extra + " --request POST --header 'Content-Type: application/json' --data '{}' '" + tc.url + "'"
			cmd, err := scriptCommand(ctx, script)
			if err != nil {
				t.Fatal(err)
			}
			var diagnostic bytes.Buffer
			cmd.Stderr = &diagnostic
			output, err := cmd.Output()
			if err != nil {
				t.Fatalf("network call failed: %v: %s", err, diagnostic.String())
			}
			if string(output) != "network-ok" {
				t.Fatalf("unexpected response %q", output)
			}
		})
	}
}

func TestScriptReceivesResolverSettingsAndPublicCABundles(t *testing.T) {
	requireSandbox(t)
	var script, expected strings.Builder
	count := 0
	for _, path := range scriptNetworkFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		count++
		fmt.Fprintf(&script, "sha256sum '%s'\n", path)
		fmt.Fprintf(&expected, "%x  %s\n", sha256.Sum256(data), path)
	}
	if count == 0 {
		t.Skip("no system resolver or CA files on this host")
	}
	script.WriteString("test ! -e /etc/shadow; test ! -e /etc/ssl/private\n")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd, err := scriptCommand(ctx, script.String())
	if err != nil {
		t.Fatal(err)
	}
	var diagnostic bytes.Buffer
	cmd.Stderr = &diagnostic
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("resolver/CA files unavailable: %v: %s", err, diagnostic.String())
	}
	if string(output) != expected.String() {
		t.Fatal("sandbox resolver or public trust files differ from service files")
	}
}
