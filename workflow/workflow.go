package workflow

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fangchen/tgtg-auto/tgtg"
	"github.com/joho/godotenv"
)

// Run is the top-level entry point for the interactive CLI workflow.
func Run() error {
	card, err := loadCardFromEnv()
	if err != nil {
		return err
	}

	// --- Step 1: Initialize client ---
	client := tgtg.NewClient("en-GB")
	reader := bufio.NewReader(os.Stdin)

	// --- Step 1: Login ---
	email, err := prompt(reader, "Enter your TGTG email address")
	if err != nil {
		return err
	}

	// --- Step 2: Login ---
	fmt.Println("\n[*] Sending login request...")
	pollingID, err := client.Login(email)
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	fmt.Println("[*] Check your email for a login PIN.")
	pin, err := prompt(reader, "Enter the PIN from your email")
	if err != nil {
		return err
	}

	if err := client.AuthByPin(pollingID, pin); err != nil {
		return fmt.Errorf("PIN authentication failed: %w", err)
	}
	fmt.Println("[+] Logged in successfully!")

	// --- Step 3: Fetch favorites ---
	fmt.Println("\n[*] Fetching your saved favorites...")
	favorites, err := client.GetFavorites()
	if err != nil {
		return fmt.Errorf("failed to fetch favorites: %w", err)
	}
	if len(favorites) == 0 {
		return fmt.Errorf("you have no saved favorites on TGTG")
	}

	// --- Step 4: Display & select ---
	fmt.Printf("\n  Found %d saved item(s):\n\n", len(favorites))
	fmt.Printf("  %-4s  %-30s  %-30s  %-10s  %s\n", "#", "Store", "Item", "Price", "Stock")
	fmt.Println("  " + strings.Repeat("-", 90))
	for i, fav := range favorites {
		name := fav.Item.Name
		if name == "" {
			name = fav.DisplayName
		}
		price := fav.Item.PriceIncludingTaxes
		fmt.Printf("  %-4d  %-30s  %-30s  %6.2f %-3s  %d\n",
			i+1,
			truncate(fav.Store.StoreName, 30),
			truncate(name, 30),
			price.Amount(),
			price.Code,
			fav.ItemsAvailable,
		)
	}

	choiceStr, err := prompt(reader, fmt.Sprintf("\nSelect an item [1-%d]", len(favorites)))
	if err != nil {
		return err
	}
	choice, err := strconv.Atoi(strings.TrimSpace(choiceStr))
	if err != nil || choice < 1 || choice > len(favorites) {
		return fmt.Errorf("invalid selection: %s", choiceStr)
	}
	selected := favorites[choice-1]
	selectedName := selected.Item.Name
	if selectedName == "" {
		selectedName = selected.DisplayName
	}
	fmt.Printf("\n[+] Selected: %s — %s\n", selected.Store.StoreName, selectedName)

	// --- Step 5: Schedule time ---
	timeStr, err := prompt(reader, "Enter order time (HH:MM:SS), or press Enter to order now")
	if err != nil {
		return err
	}

	scheduled := timeStr != ""
	if scheduled {
		target, err := parseTargetTime(timeStr)
		if err != nil {
			return err
		}
		waitDuration := time.Until(target)
		fmt.Printf("[*] Order scheduled for %s (in %s)\n", target.Format("15:04:05"), formatDuration(waitDuration))
		fmt.Println("[*] Waiting...")

		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for remaining := waitDuration; remaining > 0; remaining = time.Until(target) {
			fmt.Printf("\r    Countdown: %s  ", formatDuration(remaining))
			<-ticker.C
		}
		fmt.Println()
	}

	// Enable burst mode: no rate-limiting during the critical order+pay window
	if scheduled {
		client.Session.SetBurstMode(true)
		defer client.Session.SetBurstMode(false)
	}

	// --- Step 6: Place order (with retry) ---
	//
	// SALE_CLOSED = the store's sale window hasn't opened yet (or closed).
	//   -> poll every 100ms for up to 2 minutes to catch the exact opening.
	// SOLD_OUT = no stock left — fail immediately, no point retrying.
	// Other errors -> retry up to 5 times with 2s gaps.
	const saleClosedTimeout = 2 * time.Minute
	const saleClosedPollInterval = 100 * time.Millisecond
	const maxOtherRetries = 5
	const otherRetryInterval = 2 * time.Second

	var order *tgtg.Order
	fmt.Println("\n[*] Placing order...")

	deadline := time.Now().Add(saleClosedTimeout)
	otherFailures := 0
	attempt := 0
	for {
		attempt++
		order, err = client.CreateOrder(selected.Item.ItemID, 1)
		if err == nil {
			break
		}

		var oe *tgtg.OrderError
		if errors.As(err, &oe) {
			switch oe.State {
			case "SALE_CLOSED":
				if time.Now().Before(deadline) {
					if attempt == 1 {
						fmt.Printf("[-] Sale not open yet — polling every %v (timeout %v)...\n", saleClosedPollInterval, saleClosedTimeout)
					}
					if attempt%100 == 0 {
						fmt.Printf("    still waiting... (%d attempts, %s remaining)\n", attempt, time.Until(deadline).Round(time.Second))
					}
					time.Sleep(saleClosedPollInterval)
					continue
				}
				return fmt.Errorf("sale did not open within %v — gave up after %d attempts", saleClosedTimeout, attempt)
			case "SOLD_OUT":
				return fmt.Errorf("item is sold out — no stock available")
			}
		}

		otherFailures++
		if otherFailures >= maxOtherRetries {
			return fmt.Errorf("order creation failed after %d non-SALE_CLOSED attempts: %w", otherFailures, err)
		}
		fmt.Printf("[-] Order attempt %d failed: %v — retrying in %s...\n", attempt, err, otherRetryInterval)
		time.Sleep(otherRetryInterval)
	}
	fmt.Printf("[+] Order created! ID: %s\n", order.ID)

	// --- Step 7: Pay (with retry) ---
	const maxPayRetries = 3
	const payRetryInterval = 2 * time.Second
	var payResp *tgtg.PayOrderResponse
	fmt.Println("[*] Processing payment...")
	for attempt := 1; attempt <= maxPayRetries; attempt++ {
		payResp, err = client.PayOrder(order.ID, card)
		if err == nil {
			break
		}
		if attempt < maxPayRetries {
			fmt.Printf("[-] Payment attempt %d failed: %v — retrying in %s...\n", attempt, err, payRetryInterval)
			time.Sleep(payRetryInterval)
		}
	}
	if err != nil {
		fmt.Printf("[-] Payment failed after %d attempts: %v\n", maxPayRetries, err)
		fmt.Println("[*] Attempting to abort order...")
		if abortErr := client.AbortOrder(order.ID); abortErr != nil {
			fmt.Printf("[-] Abort also failed: %v\n", abortErr)
		} else {
			fmt.Println("[+] Order aborted successfully.")
		}
		return fmt.Errorf("payment failed: %w", err)
	}
	fmt.Printf("[+] Payment submitted! State: %s\n", payResp.State)

	// --- Step 8: Check status ---
	fmt.Println("[*] Checking order status...")
	status, err := client.GetOrderStatus(order.ID)
	if err != nil {
		fmt.Printf("[-] Could not fetch status: %v\n", err)
	} else {
		fmt.Printf("[+] Order state: %s\n", status.State)
	}

	fmt.Println("\n========================================")
	fmt.Println("  Order complete!")
	fmt.Printf("  Store:    %s\n", selected.Store.StoreName)
	fmt.Printf("  Item:     %s\n", selectedName)
	fmt.Printf("  Price:    %.2f %s\n", selected.Item.PriceIncludingTaxes.Amount(), selected.Item.PriceIncludingTaxes.Code)
	fmt.Printf("  Order ID: %s\n", order.ID)
	fmt.Println("========================================")

	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func loadCardFromEnv() (tgtg.CardData, error) {
	_ = godotenv.Load() // best-effort; ignore if missing

	card := tgtg.CardData{
		Number: os.Getenv("CARD_NUMBER"),
		CVV:    os.Getenv("CVV"),
		Month:  os.Getenv("MONTH"),
		Year:   os.Getenv("YEAR"),
	}
	if card.Number == "" || card.CVV == "" || card.Month == "" || card.Year == "" {
		return card, fmt.Errorf(
			"missing card details — set CARD_NUMBER, CVV, MONTH, YEAR in .env or environment",
		)
	}
	return card, nil
}

func prompt(reader *bufio.Reader, label string) (string, error) {
	fmt.Printf("%s: ", label)
	text, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(text), nil
}

func parseTargetTime(s string) (time.Time, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return time.Time{}, fmt.Errorf("invalid time format %q — expected HH:MM:SS", s)
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	sec, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil ||
		h < 0 || h > 23 || m < 0 || m > 59 || sec < 0 || sec > 59 {
		return time.Time{}, fmt.Errorf("invalid time %q", s)
	}

	now := time.Now()
	target := time.Date(now.Year(), now.Month(), now.Day(), h, m, sec, 0, now.Location())
	if target.Before(now) {
		target = target.Add(24 * time.Hour)
	}
	return target, nil
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
