package tgtg

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	BaseURL        = "https://api.toogoodtogo.com/api/"
	DefaultAPKVer  = "24.11.0"
	MinRequestGap  = 15 * time.Second
	DataDomeTTL    = 5 * time.Minute
	DataDomeSDKURL = "https://api-sdk.datadome.co/sdk/"
	MaxCaptchaErr  = 10

	// Fixed UA template matching the DataDome device fingerprint (Pixel 7 Pro, Android 14)
	userAgentTemplate = "TGTG/%s Dalvik/2.1.0 (Linux; U; Android 14; Pixel 7 Pro Build/UQ1A.240105.004)"
)

var datadomeValueRe = regexp.MustCompile(`datadome=([^;]+)`)

// DataDomeError is returned when the API responds with 403, indicating
// DataDome anti-bot detection. Callers can check for this to decide how
// long to wait before retrying.
type DataDomeError struct {
	Attempt int
}

func (e *DataDomeError) Error() string {
	return fmt.Sprintf("DataDome 403 (attempt %d)", e.Attempt)
}

// Session wraps http.Client with TGTG-specific behaviour:
// DataDome cookie management, rate-limiting, and 403 retry.
type Session struct {
	client        *http.Client
	baseURL       string
	userAgent     string
	apkVer        string
	language      string
	correlationID string

	mu            sync.Mutex
	lastRequest   time.Time
	ddCookie      string
	ddExpiresAt   time.Time
	captchaErrCnt int
	burstMode     bool
}

func NewSession(language string) *Session {
	apk := DefaultAPKVer
	ua := buildUserAgent(apk)
	s := &Session{
		client:        &http.Client{Timeout: 30 * time.Second},
		baseURL:       BaseURL,
		apkVer:        apk,
		language:      language,
		userAgent:     ua,
		correlationID: generateUUID(),
	}
	return s
}

func (s *Session) ResetCorrelationID() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.correlationID = generateUUID()
}

func (s *Session) SetUserAgent(ua string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.userAgent = ua
}

func (s *Session) RotateUserAgent() {
	s.SetUserAgent(buildUserAgent(s.apkVer))
}

// Post performs a JSON POST to baseURL+path, injecting auth and DataDome headers.
// It handles 403 retries with DataDome refresh and user-agent rotation.
func (s *Session) Post(path string, body any, accessToken string) (*http.Response, []byte, error) {
	fullURL := s.baseURL + path
	return s.doPost(fullURL, body, accessToken)
}

func (s *Session) doPost(fullURL string, body any, accessToken string) (*http.Response, []byte, error) {
	s.rateLimit()
	s.ensureDataDome(fullURL)

	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, nil, fmt.Errorf("marshal body: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}

	req, err := http.NewRequest(http.MethodPost, fullURL, reqBody)
	if err != nil {
		return nil, nil, fmt.Errorf("build request: %w", err)
	}
	s.setHeaders(req, accessToken)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := readResponseBody(resp)
	if err != nil {
		return resp, nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted {
		s.ResetCaptchaCount()
		return resp, respBody, nil
	}

	if resp.StatusCode == http.StatusForbidden {
		return s.handle403(fullURL, body, accessToken)
	}

	return resp, respBody, fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
}

func (s *Session) handle403(fullURL string, body any, accessToken string) (*http.Response, []byte, error) {
	s.mu.Lock()
	s.captchaErrCnt++
	cnt := s.captchaErrCnt
	burst := s.burstMode
	s.mu.Unlock()

	log.Printf("[TGTG] 403 DataDome block (consecutive: %d, burst=%v)", cnt, burst)
	s.invalidateDataDome()
	s.RotateUserAgent()

	// In burst mode (snipe phase), return error to let the caller control
	// retry timing so it doesn't waste the critical window on backoff.
	if burst {
		return nil, nil, &DataDomeError{Attempt: cnt}
	}

	// Normal mode: retry automatically with backoff.
	if cnt >= MaxCaptchaErr {
		log.Printf("[TGTG] Too many 403s, sleeping 10 minutes...")
		time.Sleep(10 * time.Minute)
		s.mu.Lock()
		s.captchaErrCnt = 0
		s.mu.Unlock()
	}

	backoff := time.Duration(cnt) * 5 * time.Second
	if backoff > 30*time.Second {
		backoff = 30 * time.Second
	}
	log.Printf("[TGTG] Retrying in %s...", backoff)
	time.Sleep(backoff)

	return s.doPost(fullURL, body, accessToken)
}

// ResetCaptchaCount resets the consecutive 403 counter after a successful request.
func (s *Session) ResetCaptchaCount() {
	s.mu.Lock()
	s.captchaErrCnt = 0
	s.mu.Unlock()
}

func (s *Session) Get(rawURL string) (*http.Response, []byte, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", s.userAgent)
	req.Header.Set("Accept", "*/*")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := readResponseBody(resp)
	return resp, data, err
}

// ---------------------------------------------------------------------------
// internal helpers
// ---------------------------------------------------------------------------

func readResponseBody(resp *http.Response) ([]byte, error) {
	var reader io.Reader = resp.Body
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("gzip reader: %w", err)
		}
		defer gz.Close()
		reader = gz
	}
	return io.ReadAll(reader)
}

// SetBurstMode disables rate-limiting for time-critical operations
// like order creation and payment during a timed snipe.
func (s *Session) SetBurstMode(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.burstMode = on
}

func (s *Session) rateLimit() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.burstMode {
		return
	}
	if !s.lastRequest.IsZero() {
		elapsed := time.Since(s.lastRequest)
		if elapsed < MinRequestGap {
			time.Sleep(MinRequestGap - elapsed)
		}
	}
	s.lastRequest = time.Now()
}

func (s *Session) setHeaders(req *http.Request, accessToken string) {
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", s.language)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("User-Agent", s.userAgent)
	req.Header.Set("x-correlation-id", s.correlationID)
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
	s.mu.Lock()
	if s.ddCookie != "" && time.Now().Before(s.ddExpiresAt) {
		req.Header.Set("Cookie", "datadome="+s.ddCookie)
	}
	s.mu.Unlock()
}

func (s *Session) ensureDataDome(requestURL string) {
	s.mu.Lock()
	valid := s.ddCookie != "" && time.Now().Before(s.ddExpiresAt)
	s.mu.Unlock()
	if valid {
		return
	}

	cid := generateCID()
	cookie := s.fetchDataDomeCookie(requestURL, cid)
	if cookie != "" {
		s.mu.Lock()
		s.ddCookie = cookie
		s.ddExpiresAt = time.Now().Add(DataDomeTTL)
		s.mu.Unlock()
	}
}

func (s *Session) invalidateDataDome() {
	s.mu.Lock()
	s.ddCookie = ""
	s.ddExpiresAt = time.Time{}
	s.mu.Unlock()
}

func (s *Session) fetchDataDomeCookie(requestURL, cid string) string {
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	eventsJSON := `[{"id":1,"message":"response validation","source":"sdk","date":` + ts + `}]`
	dIFV := generateHexID(32)

	form := url.Values{
		"cid":      {cid},
		"ddk":      {"1D42C2CA6131C526E09F294FE96F94"},
		"request":  {requestURL},
		"ua":       {s.userAgent},
		"events":   {eventsJSON},
		"inte":     {"android-java-okhttp"},
		"ddv":      {"3.0.4"},
		"ddvc":     {s.apkVer},
		"os":       {"Android"},
		"osr":      {"14"},
		"osn":      {"UPSIDE_DOWN_CAKE"},
		"osv":      {"34"},
		"screen_x": {"1440"},
		"screen_y": {"3120"},
		"screen_d": {"3.5"},
		"camera":   {`{"auth":"true", "info":"{\"front\":\"2000x1500\",\"back\":\"5472x3648\"}"}`},
		"mdl":      {"Pixel 7 Pro"},
		"prd":      {"Pixel 7 Pro"},
		"mnf":      {"Google"},
		"dev":      {"cheetah"},
		"hrd":      {"GS201"},
		"fgp":      {"google/cheetah/cheetah:14/UQ1A.240105.004/10814564:user/release-keys"},
		"tgs":      {"release-keys"},
		"d_ifv":    {dIFV},
	}

	req, err := http.NewRequest(http.MethodPost, DataDomeSDKURL, strings.NewReader(form.Encode()))
	if err != nil {
		log.Printf("[DataDome] build request: %v", err)
		return ""
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "okhttp/5.1.0")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		log.Printf("[DataDome] request failed: %v", err)
		return ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var result struct {
		Status int    `json:"status"`
		Cookie string `json:"cookie"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("[DataDome] parse error: %v (body: %s)", err, string(body))
		return ""
	}
	if result.Cookie != "" {
		if m := datadomeValueRe.FindStringSubmatch(result.Cookie); len(m) > 1 {
			log.Printf("[DataDome] cookie obtained successfully")
			return m[1]
		}
	}
	log.Printf("[DataDome] no cookie in response (status=%d)", result.Status)
	return ""
}

func buildUserAgent(apkVersion string) string {
	return fmt.Sprintf(userAgentTemplate, apkVersion)
}

func generateUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 2
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]),
	)
}

func generateCID() string {
	return generateHexID(64)
}

func generateHexID(length int) string {
	const hexChars = "0123456789abcdef"
	b := make([]byte, length)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(hexChars))))
		b[i] = hexChars[n.Int64()]
	}
	return string(b)
}
