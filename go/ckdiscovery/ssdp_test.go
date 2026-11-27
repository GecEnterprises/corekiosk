package ckdiscovery

import "testing"

func TestParseMSearch(t *testing.T) {
	msg := "M-SEARCH * HTTP/1.1\r\n" +
		"HOST: 239.255.255.250:1900\r\n" +
		"MAN: \"ssdp:discover\"\r\n" +
		"MX: 2\r\n" +
		"ST: urn:gec:corekiosk:server:1\r\n\r\n"
	st, ok := parseMSearch([]byte(msg))
	if !ok || st != "urn:gec:corekiosk:server:1" {
		t.Fatalf("st=%q ok=%v", st, ok)
	}
	if _, ok := parseMSearch([]byte("NOTIFY * HTTP/1.1\r\n\r\n")); ok {
		t.Fatal("NOTIFY should not parse as M-SEARCH")
	}
}

func TestParseSSDPResponse(t *testing.T) {
	msg := "HTTP/1.1 200 OK\r\n" +
		"CACHE-CONTROL: max-age=1800\r\n" +
		"LOCATION: http://192.168.1.20:8080/v1/health\r\n" +
		"ST: urn:gec:corekiosk:server:1\r\n" +
		"USN: urn:gec:corekiosk:abc\r\n" +
		"X-CK-ID: abc\r\n" +
		"X-CK-NAME: Joyland Arcade\r\n\r\n"
	host, port, id, name, ok := parseSSDPResponse([]byte(msg))
	if !ok || host != "192.168.1.20" || port != 8080 || id != "abc" || name != "Joyland Arcade" {
		t.Fatalf("host=%q port=%d id=%q name=%q ok=%v", host, port, id, name, ok)
	}
}

func TestMergeScanResults(t *testing.T) {
	got := mergeScanResults([]ScanResult{
		{ServerID: "a", Name: "Arcade", Hosts: []string{"192.168.1.2"}, Port: 8080, Methods: []string{MethodUDP}},
		{ServerID: "a", Hosts: []string{"192.168.1.2", "192.168.1.3"}, Port: 8080, Methods: []string{MethodMDNS}},
		{ServerID: "b", Hosts: []string{"10.0.0.2"}, Port: 8080, Methods: []string{MethodSSDP}},
	})
	if len(got) != 2 {
		t.Fatalf("len=%d", len(got))
	}
	if len(got[0].Hosts) != 2 || len(got[0].Methods) != 2 {
		t.Fatalf("merge failed: %+v", got[0])
	}
}
