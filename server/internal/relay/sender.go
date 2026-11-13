package relay

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"wecomrelay/internal/model"
	"wecomrelay/internal/security"
	"wecomrelay/internal/wecom"
)

// Target 一次投递所需的消费端配置快照（敏感字段已解密）。
type Target struct {
	ConsumerID          uint64
	Name                string
	CallbackURL         string
	ForwardMode         string
	AuthType            string
	AuthConfig          string // 明文 JSON；仅 json 模式使用
	WecomToken          string
	WecomEncodingAESKey string
	WecomCorpID         string
	TimeoutMS           int
}

// Payload 一条事件的投递内容。
type Payload struct {
	EventID     string
	AgentID     string
	RawXML      string
	JSONPayload string
}

// Sender 负责按转发模式构造并发送出站请求。
type Sender struct {
	transport    *http.Transport
	logger       *slog.Logger
	logRawEvents bool
}

func NewSender(logger *slog.Logger, logRawEvents bool) *Sender {
	return &Sender{
		logger:       logger,
		logRawEvents: logRawEvents,
		transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   3 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   32,
			IdleConnTimeout:       90 * time.Second,
		},
	}
}

// Deliver 按消费端配置构造 JSON 或企微兼容模式请求并发送。
func (s *Sender) Deliver(ctx context.Context, target Target, payload Payload) (int, time.Duration, error) {
	timeout := normalizeSendTimeout(target.TimeoutMS)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := s.buildRequest(ctx, target, payload)
	if err != nil {
		return 0, 0, err
	}
	return s.do(req, timeout)
}

// DeliverProxy 将企微原始加密 Body 与 Query 原样转发给消费端。
// 前提：消费端与「企微参数」使用同一套 Token/AESKey/CorpID。
func (s *Sender) DeliverProxy(ctx context.Context, target Target, rawBody, rawQuery string) (int, time.Duration, error) {
	timeout := normalizeSendTimeout(target.TimeoutMS)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := s.buildProxyRequest(ctx, target, rawBody, rawQuery)
	if err != nil {
		return 0, 0, err
	}
	return s.do(req, timeout)
}

func normalizeSendTimeout(ms int) time.Duration {
	timeout := time.Duration(ms) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if timeout > 60*time.Second {
		timeout = 60 * time.Second
	}
	return timeout
}

func (s *Sender) do(req *http.Request, timeout time.Duration) (int, time.Duration, error) {
	client := &http.Client{
		Transport: s.transport,
		Timeout:   timeout,
		// Webhook 投递不跟随重定向，3xx 视为失败，避免 POST 被降级为 GET 而丢载荷。
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	start := time.Now()
	resp, err := client.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		return 0, elapsed, fmt.Errorf("请求消费端失败: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 500 {
			snippet = snippet[:500]
		}
		if snippet != "" {
			return resp.StatusCode, elapsed, fmt.Errorf("消费端返回 HTTP %d: %s", resp.StatusCode, snippet)
		}
		return resp.StatusCode, elapsed, fmt.Errorf("消费端返回 HTTP %d", resp.StatusCode)
	}
	return resp.StatusCode, elapsed, nil
}

func (s *Sender) buildRequest(ctx context.Context, target Target, payload Payload) (*http.Request, error) {
	callbackURL, err := url.Parse(target.CallbackURL)
	if err != nil || (callbackURL.Scheme != "http" && callbackURL.Scheme != "https") || callbackURL.Host == "" {
		return nil, fmt.Errorf("消费端回调地址不合法: %s", target.CallbackURL)
	}

	if target.ForwardMode == model.ForwardModeWecomXML {
		crypt, err := wecom.NewCrypto(target.WecomToken, target.WecomEncodingAESKey, target.WecomCorpID)
		if err != nil {
			return nil, fmt.Errorf("消费端企微兼容模式密钥配置错误: %w", err)
		}
		encrypted, err := crypt.EncryptMsg(payload.RawXML, "", "")
		if err != nil {
			return nil, fmt.Errorf("企微兼容模式重加密失败: %w", err)
		}
		// 与企微真实回调的 Query 顺序保持一致：msg_signature → timestamp → nonce。
		// 保留消费端 URL 上已有的其他查询参数。
		query := callbackURL.Query()
		query.Del("msg_signature")
		query.Del("timestamp")
		query.Del("nonce")
		pairs := make([]string, 0, len(query)+3)
		for key, values := range query {
			for _, value := range values {
				pairs = append(pairs, url.QueryEscape(key)+"="+url.QueryEscape(value))
			}
		}
		pairs = append(pairs,
			"msg_signature="+url.QueryEscape(encrypted.Signature),
			"timestamp="+url.QueryEscape(encrypted.Timestamp),
			"nonce="+url.QueryEscape(encrypted.Nonce),
		)
		callbackURL.RawQuery = strings.Join(pairs, "&")

		// 与企微真实回调的 POST 报文保持一致：正文只含 ToUserName + Encrypt，
		// 签名/时间戳/随机串放在 URL Query 中，最大限度兼容存量消费端。
		body := buildWecomCallbackBody(target.WecomCorpID, payload.AgentID, encrypted.Encrypt)
		s.logger.Debug("企微兼容模式出站请求",
			"consumer_id", target.ConsumerID,
			"consumer", target.Name,
			"event_id", payload.EventID,
			"url", callbackURL.String(),
			"corp_id", target.WecomCorpID,
			"agent_id", payload.AgentID,
			"timestamp", encrypted.Timestamp,
			"nonce", encrypted.Nonce,
			"msg_signature", encrypted.Signature,
			"body_len", len(body),
			"body", body,
		)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, callbackURL.String(), strings.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("构造请求失败: %w", err)
		}
		// 与企微回调实际请求头保持一致。
		req.Header.Set("Content-Type", "text/xml")
		req.Header.Set("User-Agent", "wecom-callback-relay/1.0")
		req.Header.Set("X-Event-Id", payload.EventID)
		return req, nil
	}

	body := []byte(payload.JSONPayload)
	if len(body) == 0 {
		body = []byte("{}")
	}
	if !json.Valid(body) {
		return nil, fmt.Errorf("JSON 载荷不合法")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, callbackURL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	attrs := []any{
		"consumer_id", target.ConsumerID,
		"consumer", target.Name,
		"event_id", payload.EventID,
		"url", callbackURL.String(),
		"auth_type", target.AuthType,
		"body_len", len(body),
		"body_sha256", security.ContentHash(body),
	}
	if s.logRawEvents {
		attrs = append(attrs, "body", string(body))
	}
	s.logger.Debug("JSON 模式出站请求", attrs...)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("User-Agent", "wecom-callback-relay/1.0")
	req.Header.Set("X-Event-Id", payload.EventID)
	if err := applyJSONAuth(req, target.AuthType, target.AuthConfig, body); err != nil {
		return nil, err
	}
	return req, nil
}

// buildProxyRequest 构造原样透传请求：保留原始加密 Body 与 WeCom Query。
func (s *Sender) buildProxyRequest(ctx context.Context, target Target, rawBody, rawQuery string) (*http.Request, error) {
	callbackURL, err := url.Parse(target.CallbackURL)
	if err != nil || (callbackURL.Scheme != "http" && callbackURL.Scheme != "https") || callbackURL.Host == "" {
		return nil, fmt.Errorf("消费端回调地址不合法: %s", target.CallbackURL)
	}
	if rawQuery != "" {
		if callbackURL.RawQuery == "" {
			callbackURL.RawQuery = rawQuery
		} else {
			callbackURL.RawQuery = strings.TrimSuffix(callbackURL.RawQuery, "&") + "&" + rawQuery
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, callbackURL.String(), strings.NewReader(rawBody))
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "text/xml")
	req.Header.Set("User-Agent", "wecom-callback-relay/1.0")

	s.logger.Debug("企微原样透传出站请求",
		"consumer_id", target.ConsumerID,
		"consumer", target.Name,
		"url", callbackURL.String(),
		"body_len", len(rawBody),
		"body_md5", security.ContentMD5([]byte(rawBody)),
		"body_sha256", security.ContentHash([]byte(rawBody)),
		"query_md5", security.ContentMD5([]byte(rawQuery)),
		"query_sha256", security.ContentHash([]byte(rawQuery)),
		"content_length", req.ContentLength,
		"transfer_encoding", req.TransferEncoding,
		"content_type", req.Header.Get("Content-Type"),
		"timestamp_age_seconds", proxyTimestampAge(rawQuery),
		"body", rawBody,
	)
	return req, nil
}

// proxyTimestampAge 返回原样透传 Query 中 timestamp 距当前时间的秒数；无法解析时返回 -1。
func proxyTimestampAge(rawQuery string) int64 {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return -1
	}
	ts, err := strconv.ParseInt(values.Get("timestamp"), 10, 64)
	if err != nil || ts <= 0 {
		return -1
	}
	if ts > 1_000_000_000_000 {
		ts /= 1000
	}
	return time.Now().Unix() - ts
}

// BuildWecomCallbackBody 供 API 层生成与企微入站一致的测试报文。
func BuildWecomCallbackBody(toUserName, agentID, encrypt string) string {
	return buildWecomCallbackBody(toUserName, agentID, encrypt)
}

// buildWecomCallbackBody 构造与企微入站回调一致的 XML 报文。
// 真实企微报文的节点顺序为 ToUserName → Encrypt → AgentID；
// AgentID 即使为空也保留空节点，避免消费端因缺少节点而解析失败。
func buildWecomCallbackBody(toUserName, agentID, encrypt string) string {
	return "<xml><ToUserName>" + wrapCDATA(toUserName) + "</ToUserName>" +
		"<Encrypt>" + wrapCDATA(encrypt) + "</Encrypt>" +
		"<AgentID>" + wrapCDATA(agentID) + "</AgentID></xml>"
}

// wrapCDATA 生成 CDATA 节点，并处理内容中可能出现的 "]]>"。
func wrapCDATA(value string) string {
	return "<![CDATA[" + strings.ReplaceAll(value, "]]>", "]]]]><![CDATA[>") + "]]>"
}

type jsonAuthConfig struct {
	Token    string `json:"token"`
	Username string `json:"username"`
	Password string `json:"password"`
	Secret   string `json:"secret"`
}

func applyJSONAuth(req *http.Request, authType, rawConfig string, body []byte) error {
	var cfg jsonAuthConfig
	if strings.TrimSpace(rawConfig) != "" {
		if err := json.Unmarshal([]byte(rawConfig), &cfg); err != nil {
			return fmt.Errorf("认证配置解析失败: %w", err)
		}
	}

	switch authType {
	case "", model.AuthTypeNone:
		return nil
	case model.AuthTypeBearer:
		if cfg.Token == "" {
			return fmt.Errorf("bearer 认证缺少 token")
		}
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	case model.AuthTypeBasic:
		if cfg.Username == "" {
			return fmt.Errorf("basic 认证缺少 username")
		}
		credential := base64.StdEncoding.EncodeToString([]byte(cfg.Username + ":" + cfg.Password))
		req.Header.Set("Authorization", "Basic "+credential)
	case model.AuthTypeHMAC:
		if cfg.Secret == "" {
			return fmt.Errorf("hmac 认证缺少 secret")
		}
		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		mac := hmac.New(sha256.New, []byte(cfg.Secret))
		mac.Write([]byte(timestamp + "." + string(body)))
		req.Header.Set("X-Timestamp", timestamp)
		req.Header.Set("X-Signature", hex.EncodeToString(mac.Sum(nil)))
	default:
		return fmt.Errorf("不支持的认证方式: %s", authType)
	}
	return nil
}
