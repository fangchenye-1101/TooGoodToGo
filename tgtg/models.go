package tgtg

// ---------------------------------------------------------------------------
// Credentials
// ---------------------------------------------------------------------------

type Credentials struct {
	Email        string `json:"email"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	UserID       string `json:"user_id"`
	Cookie       string `json:"cookie,omitempty"`
}

// ---------------------------------------------------------------------------
// Price
// ---------------------------------------------------------------------------

type Price struct {
	Code       string `json:"code"`
	MinorUnits int    `json:"minor_units"`
	Decimals   int    `json:"decimals"`
}

func (p Price) Amount() float64 {
	divisor := 1.0
	for i := 0; i < p.Decimals; i++ {
		divisor *= 10
	}
	return float64(p.MinorUnits) / divisor
}

// ---------------------------------------------------------------------------
// Store / Location
// ---------------------------------------------------------------------------

type Address struct {
	AddressLine string `json:"address_line"`
}

type StoreLocation struct {
	Address Address `json:"address"`
}

type Store struct {
	StoreID       string        `json:"store_id"`
	StoreName     string        `json:"store_name"`
	StoreLocation StoreLocation `json:"store_location"`
}

// ---------------------------------------------------------------------------
// Item
// ---------------------------------------------------------------------------

type ItemDetail struct {
	ItemID              string `json:"item_id"`
	Name                string `json:"name"`
	PriceIncludingTaxes Price  `json:"price_including_taxes"`
}

type FavoriteItem struct {
	Item           ItemDetail `json:"item"`
	Store          Store      `json:"store"`
	ItemsAvailable int        `json:"items_available"`
	DisplayName    string     `json:"display_name"`
}

// ---------------------------------------------------------------------------
// Item list responses
// ---------------------------------------------------------------------------

type ItemsResponse struct {
	Items []FavoriteItem `json:"items"`
}

type BucketResponse struct {
	MobileBucket struct {
		Items []FavoriteItem `json:"items"`
	} `json:"mobile_bucket"`
}

// ---------------------------------------------------------------------------
// Auth
// ---------------------------------------------------------------------------

type AuthByEmailRequest struct {
	DeviceType string `json:"device_type"`
	Email      string `json:"email"`
}

type AuthByEmailResponse struct {
	State     string `json:"state"`
	PollingID string `json:"polling_id"`
}

type AuthByPinRequest struct {
	DeviceType       string `json:"device_type"`
	Email            string `json:"email"`
	RequestPin       string `json:"request_pin"`
	RequestPollingID string `json:"request_polling_id"`
}

type AuthPollingRequest struct {
	DeviceType       string `json:"device_type"`
	Email            string `json:"email"`
	RequestPollingID string `json:"request_polling_id"`
}

type LoginResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	StartupData  struct {
		User struct {
			UserID string `json:"user_id"`
		} `json:"user"`
	} `json:"startup_data"`
}

type RefreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type RefreshResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

// ---------------------------------------------------------------------------
// Items query
// ---------------------------------------------------------------------------

type Origin struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type GetItemsRequest struct {
	UserID         string   `json:"user_id"`
	Origin         Origin   `json:"origin"`
	Radius         int      `json:"radius"`
	PageSize       int      `json:"page_size"`
	Page           int      `json:"page"`
	Discover       bool     `json:"discover"`
	FavoritesOnly  bool     `json:"favorites_only"`
	ItemCategories []string `json:"item_categories"`
	DietCategories []string `json:"diet_categories"`
	WithStockOnly  bool     `json:"with_stock_only"`
	HiddenOnly     bool     `json:"hidden_only"`
	WeCareOnly     bool     `json:"we_care_only"`
}

// ---------------------------------------------------------------------------
// Order
// ---------------------------------------------------------------------------

type CreateOrderRequest struct {
	ItemCount int `json:"item_count"`
}

type Order struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

type CreateOrderResponse struct {
	State string `json:"state"`
	Order Order  `json:"order"`
}

type AbortOrderRequest struct {
	CancelReasonID int `json:"cancel_reason_id"`
}

type AbortOrderResponse struct {
	State string `json:"state"`
}

type OrderStatusResponse struct {
	State   string `json:"state"`
	Order   Order  `json:"order"`
	Payment struct {
		PaymentMethod struct {
			PaymentMethodType string `json:"payment_method_type"`
		} `json:"payment_method"`
	} `json:"payment"`
}

// ---------------------------------------------------------------------------
// Payment (Adyen)
// ---------------------------------------------------------------------------

type AdyenAuthPayload struct {
	Payload           string `json:"payload"`
	PaymentType       string `json:"payment_type"`
	SavePaymentMethod bool   `json:"save_payment_method"`
	Type              string `json:"type"`
}

type PaymentAuthorization struct {
	AuthorizationPayload AdyenAuthPayload `json:"authorization_payload"`
	PaymentProvider      string           `json:"payment_provider"`
	ReturnURL            string           `json:"return_url"`
}

type PayOrderRequest struct {
	Authorization PaymentAuthorization `json:"authorization"`
}

type PayOrderResponse struct {
	State   string `json:"state"`
	OrderID string `json:"order_id"`
}

// ---------------------------------------------------------------------------
// Card data (user input, never sent in plaintext)
// ---------------------------------------------------------------------------

type CardData struct {
	Number string
	CVV    string
	Month  string
	Year   string
}
