package compat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"goproxy/config"
	"goproxy/storage"
)

type WenfxlSyncService struct {
	store *storage.Storage
	cfg   *config.Config
}

type WenfxlSyncResult struct {
	Count  int
	Target string
}

func NewWenfxlSyncService(store *storage.Storage, cfg *config.Config) *WenfxlSyncService {
	return &WenfxlSyncService{store: store, cfg: cfg}
}

func buildRawProxyList(proxies []storage.Proxy) []string {
	result := make([]string, 0, len(proxies))
	seen := make(map[string]struct{})
	for _, proxy := range proxies {
		if proxy.Address == "" {
			continue
		}

		var value string
		switch proxy.Protocol {
		case "http":
			value = "http://" + proxy.Address
		case "socks5":
			value = "socks5://" + proxy.Address
		default:
			continue
		}

		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (s *WenfxlSyncService) ExportRawProxyList() ([]string, error) {
	limit := s.cfg.WenfxlSyncProxyLimit
	proxies, err := s.store.ListActiveProxiesForExport(limit, "")
	if err != nil {
		return nil, err
	}
	return buildRawProxyList(proxies), nil
}

func (s *WenfxlSyncService) SyncRawPoolToWenfxl() (*WenfxlSyncResult, error) {
	proxyList, err := s.ExportRawProxyList()
	if err != nil {
		return nil, err
	}
	if len(proxyList) == 0 {
		return nil, fmt.Errorf("no exportable proxies available")
	}

	token, err := s.login()
	if err != nil {
		return nil, err
	}

	configPayload, err := s.fetchConfig(token)
	if err != nil {
		return nil, err
	}

	configPayload["raw_proxy_pool"] = map[string]any{
		"enable":     s.cfg.WenfxlSyncEnableRawPool,
		"proxy_list": proxyList,
	}
	if s.cfg.WenfxlSyncDisableDefault {
		configPayload["default_proxy"] = ""
	}

	if err := s.saveConfig(token, configPayload); err != nil {
		return nil, err
	}

	return &WenfxlSyncResult{Count: len(proxyList), Target: strings.TrimRight(s.cfg.WenfxlSyncTargetURL, "/")}, nil
}

func (s *WenfxlSyncService) login() (string, error) {
	payload, err := json.Marshal(map[string]string{"password": s.cfg.WenfxlSyncPassword})
	if err != nil {
		return "", err
	}

	resp, err := s.httpClient().Post(strings.TrimRight(s.cfg.WenfxlSyncTargetURL, "/")+"/api/login", "application/json", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("wenfxl login failed: http %d", resp.StatusCode)
	}

	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.Token == "" {
		return "", fmt.Errorf("wenfxl login returned empty token")
	}
	return body.Token, nil
}

func (s *WenfxlSyncService) fetchConfig(token string) (map[string]any, error) {
	url := strings.TrimRight(s.cfg.WenfxlSyncTargetURL, "/") + "/api/config"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wenfxl fetch config failed: http %d", resp.StatusCode)
	}

	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func (s *WenfxlSyncService) saveConfig(token string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	url := strings.TrimRight(s.cfg.WenfxlSyncTargetURL, "/") + "/api/config"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("wenfxl save config failed: http %d", resp.StatusCode)
	}
	return nil
}

func (s *WenfxlSyncService) httpClient() *http.Client {
	return &http.Client{Timeout: 15 * time.Second}
}
