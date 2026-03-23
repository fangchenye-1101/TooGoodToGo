# TooGoodToGo Auto-Order

A Go CLI tool that automates placing orders and paying for surprise mystery bags saved to your favorites on the TooGoodToGo platform.

## Features

- Email + PIN authentication with the TGTG API
- Automatic DataDome cookie handling (Android SDK emulation)
- Lists all your saved favorite surprise bags with stock and pricing
- Scheduled ordering — set a precise HH:MM:SS target time
- Adyen-encrypted credit card payment (RSA + AES-CCM)
- Countdown display while waiting for scheduled time
- Automatic order abort on payment failure

## Prerequisites

- Go 1.21 or later
- A valid TooGoodToGo account with at least one saved favorite

## Setup

1. Clone the repository:

   ```bash
   git clone https://github.com/fangchen/tgtg-auto.git
   cd tgtg-auto
   ```

2. Copy the example env file and fill in your card details:

   ```bash
   cp .env.example .env
   ```

   Edit `.env`:

   ```
   CARD_NUMBER=4111111111111111
   CVV=123
   MONTH=12
   YEAR=2027
   ```

3. Install dependencies:

   ```bash
   go mod tidy
   ```

## Usage

```bash
go run main.go
```

The tool will guide you through:

1. **Login** — enter your TGTG email, then the PIN sent to your inbox
2. **Select** — pick a surprise bag from your favorites list
3. **Schedule** — enter a target time (HH:MM:SS) or press Enter to order immediately
4. **Order & Pay** — the tool creates the order and submits payment automatically

### Example session

```
Enter your TGTG email address: user@example.com

[*] Sending login request...
[*] Check your email for a login PIN.
Enter the PIN from your email: 123456
[+] Logged in successfully!

[*] Fetching your saved favorites...

  Found 3 saved item(s):

  #     Store                           Item                            Price       Stock
  ------------------------------------------------------------------------------------------
  1     Joe's Bakery                    Surprise Bag                      3.99 EUR  2
  2     Green Grocer                    Veggie Box                        4.50 EUR  0
  3     Sushi Place                     Mystery Sushi                     5.99 EUR  1

Select an item [1-3]: 1

[+] Selected: Joe's Bakery — Surprise Bag

Enter order time (HH:MM:SS), or press Enter to order now: 12:00:00
[*] Order scheduled for 12:00:00 (in 01:23:45)
[*] Waiting...
    Countdown: 01:23:44

[*] Placing order...
[+] Order created! ID: abc123
[*] Processing payment...
[+] Payment submitted! State: SUCCESS
[*] Checking order status...
[+] Order state: SUCCESS

========================================
  Order complete!
  Store:    Joe's Bakery
  Item:     Surprise Bag
  Price:    3.99 EUR
  Order ID: abc123
========================================
```

## Project Structure

```
main.go              Entry point
tgtg/
  client.go          TGTG API client (auth, favorites, orders, payment)
  session.go         HTTP session with DataDome, rate-limiting, retry
  adyen.go           Adyen RSA + AES-CCM card encryption
  models.go          Request/response data structures
workflow/
  workflow.go        Interactive CLI workflow
```

## Disclaimer

This tool is for educational and personal use only. Use it responsibly and in accordance with TooGoodToGo's terms of service.
