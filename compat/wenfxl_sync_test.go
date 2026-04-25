package compat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"goproxy/config"
	"goproxy/storage"
)

func TestDefaultConfigIncludesWenfxlSyncDefaults(t *testing.T) {
	cfg := config.DefaultConfig()
	if cfg.WenfxlSyncEnabled {
		t.Fatal("expected sync disabled by default")
	}
	if cfg.WenfxlSyncTargetURL != "http://127.0.0.1:8000" {
		t.Fatalf("unexpected target url: %s", cfg.WenfxlSyncTargetURL)
	}
	if !cfg.WenfxlSyncEnableRawPool {
		t.Fatal("expected raw pool sync enabled by default")
	}
	if !cfg.WenfxlSyncDisableDefault {
		t.Fatal("expected default proxy disable flag enabled by default")
	}
	if cfg.WenfxlSyncProxyLimit != 20 {
		t.Fatalf("unexpected proxy limit: %d", cfg.WenfxlSyncProxyLimit)
	}
}

func TestLoadAndSaveRoundTripWenfxlSyncSettings(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.Setenv("DATA_DIR", tmpDir); err != nil {
		t.Fatalf("set DATA_DIR: %v", err)
	}
	defer os.Unsetenv("DATA_DIR")

	cfg := config.DefaultConfig()
	loaded := config.Load()
	if loaded == nil {
		t.Fatal("expected loaded config")
	}

	cfg.WenfxlSyncEnabled = true
	cfg.WenfxlSyncTargetURL = "http://127.0.0.1:9527"
	cfg.WenfxlSyncPassword = "demo-pass"
	cfg.WenfxlSyncEnableRawPool = true
	cfg.WenfxlSyncDisableDefault = false
	cfg.WenfxlSyncProxyLimit = 8

	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	reloaded := config.Load()
	if !reloaded.WenfxlSyncEnabled {
		t.Fatal("expected sync enabled after reload")
	}
	if reloaded.WenfxlSyncTargetURL != "http://127.0.0.1:9527" {
		t.Fatalf("unexpected target url after reload: %s", reloaded.WenfxlSyncTargetURL)
	}
	if reloaded.WenfxlSyncPassword != "demo-pass" {
		t.Fatalf("unexpected sync password after reload: %s", reloaded.WenfxlSyncPassword)
	}
	if reloaded.WenfxlSyncDisableDefault {
		t.Fatal("expected disable-default false after reload")
	}
	if reloaded.WenfxlSyncProxyLimit != 8 {
		t.Fatalf("unexpected proxy limit after reload: %d", reloaded.WenfxlSyncProxyLimit)
	}

	configPath := filepath.Join(tmpDir, "config.json")
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("expected config file at %s: %v", configPath, err)
	}
}

func TestListActiveProxiesForExportSkipsDisabledAndFailed(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "proxy.db"))
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}
	defer store.Close()

	seed := []struct {
		address      string
		protocol     string
		source       string
		status       string
		failCount    int
		latency      int
		qualityGrade string
	}{
		{"1.1.1.1:8080", "http", "free", "active", 0, 120, "S"},
		{"2.2.2.2:1080", "socks5", "custom", "degraded", 2, 80, "S"},
		{"3.3.3.3:8080", "http", "free", "disabled", 0, 60, "S"},
		{"4.4.4.4:1080", "socks5", "free", "active", 3, 40, "S"},
	}

	for _, item := range seed {
		if err := store.AddProxyWithSource(item.address, item.protocol, item.source); err != nil {
			t.Fatalf("seed proxy %s: %v", item.address, err)
		}
		if _, err := store.GetDB().Exec(
			`UPDATE proxies SET status = ?, fail_count = ?, latency = ?, quality_grade = ? WHERE address = ?`,
			item.status, item.failCount, item.latency, item.qualityGrade, item.address,
		); err != nil {
			t.Fatalf("update proxy %s: %v", item.address, err)
		}
	}

	proxies, err := store.ListActiveProxiesForExport(10, "")
	if err != nil {
		t.Fatalf("list export proxies: %v", err)
	}
	if len(proxies) != 2 {
		t.Fatalf("expected 2 exportable proxies, got %d", len(proxies))
	}
	if proxies[0].Address != "2.2.2.2:1080" {
		t.Fatalf("expected lowest latency export proxy first, got %s", proxies[0].Address)
	}
	if proxies[1].Address != "1.1.1.1:8080" {
		t.Fatalf("unexpected second export proxy: %s", proxies[1].Address)
	}
}

func TestBuildRawProxyListNormalizesHTTPAndSOCKS5(t *testing.T) {
	proxies := []storage.Proxy{
		{Address: "1.2.3.4:8080", Protocol: "http"},
		{Address: "5.6.7.8:1080", Protocol: "socks5"},
		{Address: "", Protocol: "http"},
		{Address: "9.9.9.9:3128", Protocol: "https"},
	}

	got := buildRawProxyList(proxies)
	want := []string{"http://1.2.3.4:8080", "socks5://5.6.7.8:1080"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestExportRawProxyListAppliesLimit(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "proxy.db"))
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}
	defer store.Close()

	seed := []struct {
		address      string
		protocol     string
		latency      int
		qualityGrade string
	}{
		{"10.0.0.1:8080", "http", 200, "S"},
		{"10.0.0.2:1080", "socks5", 50, "S"},
	}

	for _, item := range seed {
		if err := store.AddProxyWithSource(item.address, item.protocol, "free"); err != nil {
			t.Fatalf("seed proxy %s: %v", item.address, err)
		}
		if _, err := store.GetDB().Exec(
			`UPDATE proxies SET latency = ?, quality_grade = ? WHERE address = ?`,
			item.latency, item.qualityGrade, item.address,
		); err != nil {
			t.Fatalf("update proxy %s: %v", item.address, err)
		}
	}

	cfg := config.DefaultConfig()
	cfg.WenfxlSyncProxyLimit = 1
	svc := NewWenfxlSyncService(store, cfg)

	got, err := svc.ExportRawProxyList()
	if err != nil {
		t.Fatalf("export raw proxy list: %v", err)
	}
	want := []string{"socks5://10.0.0.2:1080"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestExportRawProxyListOnlyIncludesSGrade(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "proxy.db"))
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}
	defer store.Close()

	seed := []struct {
		address      string
		protocol     string
		qualityGrade string
		latency      int
	}{
		{"31.0.0.1:8080", "http", "S", 120},
		{"31.0.0.2:1080", "socks5", "A", 700},
		{"31.0.0.3:1080", "socks5", "S", 300},
	}

	for _, item := range seed {
		if err := store.AddProxyWithSource(item.address, item.protocol, "free"); err != nil {
			t.Fatalf("seed proxy %s: %v", item.address, err)
		}
		if _, err := store.GetDB().Exec(
			`UPDATE proxies SET latency = ?, quality_grade = ? WHERE address = ?`,
			item.latency, item.qualityGrade, item.address,
		); err != nil {
			t.Fatalf("update proxy %s: %v", item.address, err)
		}
	}

	cfg := config.DefaultConfig()
	cfg.WenfxlSyncProxyLimit = 20
	svc := NewWenfxlSyncService(store, cfg)

	got, err := svc.ExportRawProxyList()
	if err != nil {
		t.Fatalf("export raw proxy list: %v", err)
	}
	want := []string{"http://31.0.0.1:8080", "socks5://31.0.0.3:1080"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestSyncToWenfxlLogsInFetchesConfigAndSavesPatchedRawPool(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "proxy.db"))
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}
	defer store.Close()

	if err := store.AddProxyWithSource("11.0.0.1:8080", "http", "free"); err != nil {
		t.Fatalf("seed proxy: %v", err)
	}
	if _, err := store.GetDB().Exec(
		`UPDATE proxies SET latency = ?, quality_grade = ? WHERE address = ?`,
		120, "S", "11.0.0.1:8080",
	); err != nil {
		t.Fatalf("update proxy quality grade: %v", err)
	}

	var savedPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/login":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"success","token":"demo-token"}`))
		case "/api/config":
			auth := r.Header.Get("Authorization")
			if auth != "Bearer demo-token" {
				t.Fatalf("expected bearer token, got %s", auth)
			}
			if r.Method == http.MethodGet {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"default_proxy":"http://localhost:7777","raw_proxy_pool":{"enable":false,"proxy_list":[]},"other":123}`))
				return
			}
			if r.Method == http.MethodPost {
				if err := json.NewDecoder(r.Body).Decode(&savedPayload); err != nil {
					t.Fatalf("decode saved payload: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"status":"success"}`))
				return
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.WenfxlSyncTargetURL = server.URL
	cfg.WenfxlSyncPassword = "pw"
	cfg.WenfxlSyncDisableDefault = true
	cfg.WenfxlSyncProxyLimit = 10
	svc := NewWenfxlSyncService(store, cfg)

	result, err := svc.SyncRawPoolToWenfxl()
	if err != nil {
		t.Fatalf("sync raw pool: %v", err)
	}
	if result.Count != 1 {
		t.Fatalf("expected 1 synced proxy, got %d", result.Count)
	}
	if savedPayload == nil {
		t.Fatal("expected saved payload")
	}
	if savedPayload["default_proxy"] != "" {
		t.Fatalf("expected default_proxy cleared, got %v", savedPayload["default_proxy"])
	}
	rawPool, ok := savedPayload["raw_proxy_pool"].(map[string]any)
	if !ok {
		t.Fatalf("expected raw_proxy_pool object, got %T", savedPayload["raw_proxy_pool"])
	}
	if rawPool["enable"] != true {
		t.Fatalf("expected raw_proxy_pool.enable true, got %v", rawPool["enable"])
	}
	proxyList, ok := rawPool["proxy_list"].([]any)
	if !ok {
		t.Fatalf("expected proxy_list slice, got %T", rawPool["proxy_list"])
	}
	if len(proxyList) != 1 || proxyList[0] != "http://11.0.0.1:8080" {
		t.Fatalf("unexpected proxy list: %v", proxyList)
	}
}

func TestSyncToWenfxlFailsOnLoginError(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "proxy.db"))
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}
	defer store.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/login" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"status":"error"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.WenfxlSyncTargetURL = server.URL
	cfg.WenfxlSyncPassword = "pw"
	svc := NewWenfxlSyncService(store, cfg)

	if _, err := svc.SyncRawPoolToWenfxl(); err == nil {
		t.Fatal("expected login error")
	}
}

func TestSyncToWenfxlFailsOnConfigFetchError(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "proxy.db"))
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}
	defer store.Close()

	if err := store.AddProxyWithSource("12.0.0.1:8080", "http", "free"); err != nil {
		t.Fatalf("seed proxy: %v", err)
	}
	if _, err := store.GetDB().Exec(
		`UPDATE proxies SET latency = ?, quality_grade = ? WHERE address = ?`,
		120, "S", "12.0.0.1:8080",
	); err != nil {
		t.Fatalf("update proxy quality grade: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/login":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"success","token":"demo-token"}`))
		case "/api/config":
			if r.Method == http.MethodGet {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"status":"error"}`))
				return
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.WenfxlSyncTargetURL = server.URL
	cfg.WenfxlSyncPassword = "pw"
	svc := NewWenfxlSyncService(store, cfg)

	if _, err := svc.SyncRawPoolToWenfxl(); err == nil {
		t.Fatal("expected config fetch error")
	}
}

func TestSyncToWenfxlFailsOnConfigSaveError(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storage.New(filepath.Join(tmpDir, "proxy.db"))
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}
	defer store.Close()

	if err := store.AddProxyWithSource("13.0.0.1:8080", "http", "free"); err != nil {
		t.Fatalf("seed proxy: %v", err)
	}
	if _, err := store.GetDB().Exec(
		`UPDATE proxies SET latency = ?, quality_grade = ? WHERE address = ?`,
		120, "S", "13.0.0.1:8080",
	); err != nil {
		t.Fatalf("update proxy quality grade: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/login":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"success","token":"demo-token"}`))
		case "/api/config":
			if r.Method == http.MethodGet {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"default_proxy":"http://localhost:7777","raw_proxy_pool":{"enable":false,"proxy_list":[]}}`))
				return
			}
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"status":"error"}`))
				return
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.WenfxlSyncTargetURL = server.URL
	cfg.WenfxlSyncPassword = "pw"
	svc := NewWenfxlSyncService(store, cfg)

	if _, err := svc.SyncRawPoolToWenfxl(); err == nil {
		t.Fatal("expected config save error")
	}
}

func TestSyncToWenfxlFailsOnEmptyExportList(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "proxy.db"))
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}
	defer store.Close()

	cfg := config.DefaultConfig()
	cfg.WenfxlSyncTargetURL = "http://127.0.0.1:8000"
	cfg.WenfxlSyncPassword = "pw"
	svc := NewWenfxlSyncService(store, cfg)

	if _, err := svc.SyncRawPoolToWenfxl(); err == nil {
		t.Fatal("expected empty export error")
	}
}
