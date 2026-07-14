package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const deviceIDAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"

func main() {
	if len(os.Args) == 3 && os.Args[1] == "probe" {
		probe(os.Args[2])
		return
	}
	if len(os.Args) == 3 && os.Args[1] == "spki" {
		printSPKI(os.Args[2])
		return
	}
	if len(os.Args) != 1 {
		os.Exit(2)
	}

	apiKey := os.Getenv("MOCK_API_KEY")
	if apiKey == "" {
		os.Exit(2)
	}
	listenAddress := os.Getenv("MOCK_LISTEN_ADDRESS")
	if listenAddress == "" {
		listenAddress = ":8080"
	}
	deviceID := canonicalDeviceID(bytes.Repeat([]byte{0x8b}, 32))
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/system/status", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.Header.Get("X-API-Key") != apiKey {
			http.Error(writer, "", http.StatusForbidden)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]string{"myID": deviceID})
	})
	mux.HandleFunc("/rest/events", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.Header.Get("X-API-Key") != apiKey {
			http.Error(writer, "", http.StatusForbidden)
			return
		}
		select {
		case <-request.Context().Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte("[]\n"))
	})
	mux.HandleFunc("/api/v1/health", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(writer, "", http.StatusMethodNotAllowed)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte("{\"status\":\"ok\"}\n"))
	})
	mux.HandleFunc("/api/v1/trigger", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(writer, "", http.StatusMethodNotAllowed)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte("{\"status\":\"ok\",\"devices_notified\":1}\n"))
	})
	server := &http.Server{
		Addr:              listenAddress,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       10 * time.Second,
	}
	log.Fatal(server.ListenAndServe())
}

func probe(address string) {
	connection, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		os.Exit(1)
	}
	_ = connection.Close()
}

func printSPKI(address string) {
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	connection, err := tls.DialWithDialer(dialer, "tcp", address, &tls.Config{
		// #nosec G402 -- this isolated proof reads the self-signed endpoint's
		// public-key pin; production clients verify the configured SPKI pin.
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS13,
		MaxVersion:         tls.VersionTLS13,
	})
	if err != nil {
		os.Exit(1)
	}
	defer connection.Close()
	certificates := connection.ConnectionState().PeerCertificates
	if len(certificates) != 1 {
		os.Exit(1)
	}
	digest := sha256.Sum256(certificates[0].RawSubjectPublicKeyInfo)
	_, _ = os.Stdout.WriteString(hex.EncodeToString(digest[:]) + "\n")
}

func canonicalDeviceID(raw []byte) string {
	encoded := strings.TrimRight(base32.StdEncoding.EncodeToString(raw), "=")
	withChecks := make([]byte, 0, 56)
	for group := 0; group < 4; group++ {
		chunk := encoded[group*13 : (group+1)*13]
		withChecks = append(withChecks, chunk...)
		withChecks = append(withChecks, luhn32(chunk))
	}
	chunks := make([]string, 0, 8)
	for offset := 0; offset < len(withChecks); offset += 7 {
		chunks = append(chunks, string(withChecks[offset:offset+7]))
	}
	return strings.Join(chunks, "-")
}

func luhn32(value string) byte {
	factor := 1
	sum := 0
	for index := range value {
		codepoint := strings.IndexByte(deviceIDAlphabet, value[index])
		addend := factor * codepoint
		if factor == 2 {
			factor = 1
		} else {
			factor = 2
		}
		addend = addend/32 + addend%32
		sum += addend
	}
	return deviceIDAlphabet[(32-sum%32)%32]
}
