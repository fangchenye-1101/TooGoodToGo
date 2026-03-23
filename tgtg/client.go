package tgtg

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

const (
	authByEmailEndpoint  = "auth/v5/authByEmail"
	authByPinEndpoint    = "auth/v5/authByRequestPin"
	authPollingEndpoint  = "auth/v5/authByRequestPollingId"
	refreshEndpoint      = "token/v1/refresh"
	itemEndpoint         = "item/v8/"
	createOrderEndpoint  = "order/v8/create/"
	abortOrderEndpoint   = "order/v8/%s/abort"
	orderStatusEndpoint  = "order/v8/%s/status"
	payOrderEndpoint     = "order/v8/%s/pay"

	deviceType             = "ANDROID"
	accessTokenLifetime    = 4 * time.Hour
	maxPollingTries        = 24
	pollingWait            = 5 * time.Second
)

// Client communicates with the TooGoodToGo API.
type Client struct {
	Session     *Session
	Credentials Credentials

	lastTokenRefresh time.Time
}

func NewClient(language string) *Client {
	return &Client{
		Session: NewSession(language),
	}
}

// ---------------------------------------------------------------------------
// Auth
// ---------------------------------------------------------------------------

// Login initiates email-based authentication. Returns the polling ID
// so the caller can collect the PIN and call AuthByPin.
func (c *Client) Login(email string) (pollingID string, err error) {
	c.Credentials.Email = email
	c.Session.ResetCorrelationID()

	resp, body, err := c.Session.Post(authByEmailEndpoint, AuthByEmailRequest{
		DeviceType: deviceType,
		Email:      email,
	}, "")
	if err != nil {
		return "", fmt.Errorf("login request: %w", err)
	}

	var authResp AuthByEmailResponse
	if err := json.Unmarshal(body, &authResp); err != nil {
		return "", fmt.Errorf("parse login response: %w", err)
	}

	switch authResp.State {
	case "WAIT":
		return authResp.PollingID, nil
	case "TERMS":
		return "", fmt.Errorf("email %s is not linked to a TGTG account — please sign up first", email)
	default:
		return "", fmt.Errorf("unexpected login state %q (HTTP %d): %s", authResp.State, resp.StatusCode, string(body))
	}
}

// AuthByPin completes login with the PIN received via email.
func (c *Client) AuthByPin(pollingID, pin string) error {
	resp, body, err := c.Session.Post(authByPinEndpoint, AuthByPinRequest{
		DeviceType:       deviceType,
		Email:            c.Credentials.Email,
		RequestPin:       pin,
		RequestPollingID: pollingID,
	}, "")
	if err != nil {
		return fmt.Errorf("auth by pin: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("auth by pin failed (HTTP %d): %s", resp.StatusCode, string(body))
	}

	return c.parseLoginResponse(body, resp)
}

// PollForLogin polls the auth endpoint waiting for the user to click the
// email link (legacy flow). Prefer AuthByPin for the PIN-based flow.
func (c *Client) PollForLogin(pollingID string) error {
	for i := 0; i < maxPollingTries; i++ {
		resp, body, err := c.Session.Post(authPollingEndpoint, AuthPollingRequest{
			DeviceType:       deviceType,
			Email:            c.Credentials.Email,
			RequestPollingID: pollingID,
		}, "")
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusAccepted {
			log.Println("[TGTG] Waiting for email verification...")
			time.Sleep(pollingWait)
			continue
		}
		if resp.StatusCode == http.StatusOK {
			return c.parseLoginResponse(body, resp)
		}
		return fmt.Errorf("polling failed (HTTP %d): %s", resp.StatusCode, string(body))
	}
	return fmt.Errorf("login polling timed out after %d attempts", maxPollingTries)
}

func (c *Client) parseLoginResponse(body []byte, resp *http.Response) error {
	var lr LoginResponse
	if err := json.Unmarshal(body, &lr); err != nil {
		return fmt.Errorf("parse login response: %w", err)
	}
	c.Credentials.AccessToken = lr.AccessToken
	c.Credentials.RefreshToken = lr.RefreshToken
	c.Credentials.UserID = lr.StartupData.User.UserID
	c.lastTokenRefresh = time.Now()

	if cookie := resp.Header.Get("Set-Cookie"); cookie != "" {
		c.Credentials.Cookie = cookie
	}
	return nil
}

// ---------------------------------------------------------------------------
// Token refresh
// ---------------------------------------------------------------------------

func (c *Client) ensureAuth() error {
	if c.Credentials.AccessToken == "" {
		return fmt.Errorf("not logged in")
	}
	if time.Since(c.lastTokenRefresh) > accessTokenLifetime {
		return c.RefreshToken()
	}
	return nil
}

func (c *Client) RefreshToken() error {
	resp, body, err := c.Session.Post(refreshEndpoint, RefreshRequest{
		RefreshToken: c.Credentials.RefreshToken,
	}, c.Credentials.AccessToken)
	if err != nil {
		return fmt.Errorf("refresh token: %w", err)
	}

	var rr RefreshResponse
	if err := json.Unmarshal(body, &rr); err != nil {
		return fmt.Errorf("parse refresh response: %w", err)
	}
	c.Credentials.AccessToken = rr.AccessToken
	c.Credentials.RefreshToken = rr.RefreshToken
	c.lastTokenRefresh = time.Now()

	if cookie := resp.Header.Get("Set-Cookie"); cookie != "" {
		c.Credentials.Cookie = cookie
	}
	return nil
}

// ---------------------------------------------------------------------------
// Favorites / Items
// ---------------------------------------------------------------------------

// GetFavorites returns all items the user has saved as favorites.
func (c *Client) GetFavorites() ([]FavoriteItem, error) {
	if err := c.ensureAuth(); err != nil {
		return nil, err
	}

	var all []FavoriteItem
	page := 1
	pageSize := 50
	for {
		req := GetItemsRequest{
			UserID:         c.Credentials.UserID,
			Origin:         Origin{Latitude: 0, Longitude: 0},
			Radius:         21,
			PageSize:       pageSize,
			Page:           page,
			Discover:       false,
			FavoritesOnly:  true,
			ItemCategories: []string{},
			DietCategories: []string{},
			WithStockOnly:  false,
			HiddenOnly:     false,
			WeCareOnly:     false,
		}
		_, body, err := c.Session.Post(itemEndpoint, req, c.Credentials.AccessToken)
		if err != nil {
			return nil, fmt.Errorf("get favorites page %d: %w", page, err)
		}

		var ir ItemsResponse
		if err := json.Unmarshal(body, &ir); err != nil {
			return nil, fmt.Errorf("parse items response: %w", err)
		}
		all = append(all, ir.Items...)
		if len(ir.Items) < pageSize {
			break
		}
		page++
	}
	return all, nil
}

// ---------------------------------------------------------------------------
// Orders
// ---------------------------------------------------------------------------

// OrderError carries the API state so callers can distinguish
// retriable conditions like SALE_CLOSED from permanent failures.
type OrderError struct {
	State   string
	RawBody string
}

func (e *OrderError) Error() string {
	return fmt.Sprintf("create order failed: state=%s, body=%s", e.State, e.RawBody)
}

// CreateOrder reserves a surprise bag. Returns the order details.
func (c *Client) CreateOrder(itemID string, count int) (*Order, error) {
	if err := c.ensureAuth(); err != nil {
		return nil, err
	}

	path := createOrderEndpoint + itemID
	_, body, err := c.Session.Post(path, CreateOrderRequest{ItemCount: count}, c.Credentials.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("create order: %w", err)
	}

	var cor CreateOrderResponse
	if err := json.Unmarshal(body, &cor); err != nil {
		return nil, fmt.Errorf("parse create order response: %w", err)
	}
	if cor.State != "SUCCESS" {
		return nil, &OrderError{State: cor.State, RawBody: string(body)}
	}
	return &cor.Order, nil
}

// PayOrder submits an Adyen-encrypted card payment for the given order.
func (c *Client) PayOrder(orderID string, card CardData) (*PayOrderResponse, error) {
	if err := c.ensureAuth(); err != nil {
		return nil, err
	}

	enc, err := NewAdyenEncryptor(c.Session)
	if err != nil {
		return nil, fmt.Errorf("init adyen encryptor: %w", err)
	}

	payload, err := enc.BuildPayOrderPayload(card)
	if err != nil {
		return nil, fmt.Errorf("build payment payload: %w", err)
	}

	path := fmt.Sprintf(payOrderEndpoint, orderID)
	_, body, err := c.Session.Post(path, payload, c.Credentials.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("pay order: %w", err)
	}

	var por PayOrderResponse
	if err := json.Unmarshal(body, &por); err != nil {
		return nil, fmt.Errorf("parse pay response: %w", err)
	}
	return &por, nil
}

// GetOrderStatus checks the current status of an order.
func (c *Client) GetOrderStatus(orderID string) (*OrderStatusResponse, error) {
	if err := c.ensureAuth(); err != nil {
		return nil, err
	}

	path := fmt.Sprintf(orderStatusEndpoint, orderID)
	_, body, err := c.Session.Post(path, nil, c.Credentials.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("get order status: %w", err)
	}

	var osr OrderStatusResponse
	if err := json.Unmarshal(body, &osr); err != nil {
		return nil, fmt.Errorf("parse order status: %w", err)
	}
	return &osr, nil
}

// AbortOrder cancels an unpaid order.
func (c *Client) AbortOrder(orderID string) error {
	if err := c.ensureAuth(); err != nil {
		return err
	}

	path := fmt.Sprintf(abortOrderEndpoint, orderID)
	_, body, err := c.Session.Post(path, AbortOrderRequest{CancelReasonID: 1}, c.Credentials.AccessToken)
	if err != nil {
		return fmt.Errorf("abort order: %w", err)
	}

	var ar AbortOrderResponse
	if err := json.Unmarshal(body, &ar); err != nil {
		return fmt.Errorf("parse abort response: %w", err)
	}
	if ar.State != "SUCCESS" {
		return fmt.Errorf("abort order failed: state=%s, body=%s", ar.State, string(body))
	}
	return nil
}
