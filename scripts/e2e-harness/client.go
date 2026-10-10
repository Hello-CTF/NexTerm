package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type client struct {
	port   int
	cookie string
	csrf   string
	userID string
	hc     *http.Client
}

func newClient(port int) *client {
	return &client{port: port, hc: &http.Client{Timeout: 30 * time.Second}}
}

func (c *client) request(method, path string, payload interface{}, csrf bool) (int, map[string]interface{}, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", c.port, path), body)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cookie != "" {
		req.Header.Set("Cookie", sessionCookie+"="+c.cookie)
	}
	if csrf && c.csrf != "" {
		req.Header.Set(csrfHeader, c.csrf)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}
	decoded := map[string]interface{}{}
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded == nil {
		text := string(raw)
		if len(text) > 500 {
			text = text[:500]
		}
		decoded = map[string]interface{}{"raw": text}
	}
	return resp.StatusCode, decoded, nil
}

func (c *client) login(username, password string) error {
	payload, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/auth/login", c.port), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("login: HTTP %d %s", resp.StatusCode, truncate(string(raw), 200))
	}
	for _, line := range resp.Header.Values("Set-Cookie") {
		if strings.HasPrefix(line, sessionCookie+"=") {
			c.cookie = strings.SplitN(strings.SplitN(line, ";", 2)[0], "=", 2)[1]
		}
	}
	if c.cookie == "" {
		return fmt.Errorf("login response has no session cookie")
	}
	body := map[string]interface{}{}
	if err := json.Unmarshal(raw, &body); err != nil {
		return err
	}
	c.csrf = getStr(body, "csrf_token")
	c.userID = getStr(getMap(body, "user"), "id")
	return nil
}

func (c *client) me() error {
	status, body, err := c.request("GET", "/auth/me", nil, false)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("me: HTTP %d %v", status, body)
	}
	c.userID = getStr(getMap(body, "user"), "id")
	return nil
}

func plainGet(port int, path string) (int, string, string, error) {
	resp, err := newClient(port).hc.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
	if err != nil {
		return 0, "", "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", "", err
	}
	if resp.StatusCode != 200 {
		return resp.StatusCode, "", "", fmt.Errorf("plain GET %s: HTTP %d", path, resp.StatusCode)
	}
	return resp.StatusCode, resp.Header.Get("Cache-Control"), string(raw), nil
}

func postStatus(port int, path string) (int, error) {
	resp, err := newClient(port).hc.Post(fmt.Sprintf("http://127.0.0.1:%d%s", port, path), "application/json", strings.NewReader("{}"))
	if err != nil {
		return 0, err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode, nil
}

func getMap(m map[string]interface{}, key string) map[string]interface{} {
	v, _ := m[key].(map[string]interface{})
	return v
}

func getList(m map[string]interface{}, key string) []interface{} {
	v, _ := m[key].([]interface{})
	return v
}

func getStr(m map[string]interface{}, key string) string {
	v, _ := m[key].(string)
	return v
}

func getBool(m map[string]interface{}, key string) (bool, bool) {
	v, ok := m[key].(bool)
	return v, ok
}

func getFloat(m map[string]interface{}, key string) float64 {
	v, _ := m[key].(float64)
	return v
}
