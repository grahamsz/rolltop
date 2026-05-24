package bimi

import (
	"net"
	"testing"
)

func TestDomainFromAddress(t *testing.T) {
	got := DomainFromAddress(`Brand Team <news@example.com>`)
	if got != "example.com" {
		t.Fatalf("domain = %q", got)
	}
}

func TestParseBIMITXT(t *testing.T) {
	fields := parseBIMITXT("v=BIMI1; l=https://brand.example/logo.svg; a=https://brand.example/vmc.pem")
	if fields["v"] != "BIMI1" || fields["l"] != "https://brand.example/logo.svg" {
		t.Fatalf("fields = %#v", fields)
	}
}

func TestValidateSVGRejectsActiveContent(t *testing.T) {
	if err := validateSVG(`<svg viewBox="0 0 1 1"><path d="M0 0h1v1z"/></svg>`); err != nil {
		t.Fatalf("safe svg rejected: %v", err)
	}
	if err := validateSVG(`<svg><script>alert(1)</script></svg>`); err == nil {
		t.Fatal("expected script-bearing svg to be rejected")
	}
}

func TestPublicIPRejectsPrivateAddresses(t *testing.T) {
	if publicIP(net.ParseIP("127.0.0.1")) {
		t.Fatal("loopback was treated as public")
	}
	if publicIP(net.ParseIP("10.0.0.1")) {
		t.Fatal("private IP was treated as public")
	}
	if !publicIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("public IP was rejected")
	}
}
