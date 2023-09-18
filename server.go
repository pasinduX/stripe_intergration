package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/joho/godotenv"
	"github.com/stripe/stripe-go/v72"
	"github.com/stripe/stripe-go/v72/checkout/session"
	"github.com/stripe/stripe-go/v72/price"
	"github.com/stripe/stripe-go/v72/webhook"
)

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}
	checkEnv()

	stripe.Key = os.Getenv("STRIPE_SECRET_KEY")

	http.Handle("/", http.FileServer(http.Dir(os.Getenv("STATIC_DIR"))))
	http.HandleFunc("/config", handleConfig)
	http.HandleFunc("/checkout-session", handleCheckoutSession)
	http.HandleFunc("/create-checkout-session", handleCreateCheckoutSession)
	http.HandleFunc("/webhook", handleWebhook)
	http.HandleFunc("/html/success.html", handleSuccessPage)

	log.Println("server running at 0.0.0.0:4242")
	http.ListenAndServe("0.0.0.0:4242", nil)
}

type ErrorResponseMessage struct {
	Message string `json:"message"`
}

type ErrorResponse struct {
	Error *ErrorResponseMessage `json:"error"`
}

func handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	p, _ := price.Get(
		os.Getenv("PRICE"),
		nil,
	)
	writeJSON(w, struct {
		PublicKey  string `json:"publicKey"`
		UnitAmount int64  `json:"unitAmount"`
		Currency   string `json:"currency"`
	}{
		PublicKey:  os.Getenv("STRIPE_SECRET_KEY"),
		UnitAmount: p.UnitAmount,
		Currency:   string(p.Currency),
	})
}

func handleCheckoutSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	sessionID := r.URL.Query().Get("sessionId")
	s, _ := session.Get(sessionID, nil)
	writeJSON(w, s)
}

func handleCreateCheckoutSession(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	quantity, err := strconv.ParseInt(r.PostFormValue("quantity")[0:], 10, 64)
	if err != nil {
		http.Error(w, fmt.Sprintf("error parsing quantity %v", err.Error()), http.StatusInternalServerError)
		return
	}
	domainURL := os.Getenv("DOMAIN")

	params := &stripe.CheckoutSessionParams{
		SuccessURL: stripe.String(domainURL + "/html/success.html?session_id={CHECKOUT_SESSION_ID}"),
		CancelURL:  stripe.String(domainURL + "/canceled.html"),
		Mode:       stripe.String(string(stripe.CheckoutSessionModePayment)),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				Quantity: stripe.Int64(quantity),
				Price:    stripe.String(os.Getenv("PRICE")),
			},
		},
	}
	s, err := session.New(params)
	if err != nil {
		http.Error(w, fmt.Sprintf("error while creating session %v", err.Error()), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, s.URL, http.StatusSeeOther)
}
func handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	const MaxBodyBytes = int64(65536)
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
	payload, err := ioutil.ReadAll(r.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading request body: %v\n", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	event := stripe.Event{}

	if err := json.Unmarshal(payload, &event); err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  Webhook error while parsing basic request. %v\n", err.Error())
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	signatureHeader := r.Header.Get("Stripe-Signature")
	fmt.Println(signatureHeader)
	fmt.Println(os.Getenv("STRIPE_WEBHOOK_SECRET"))

	err = webhook.ValidatePayload(payload, signatureHeader, os.Getenv("STRIPE_WEBHOOK_SECRET"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  Webhook error while validating signature. %v\n", err.Error())
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	event, err = webhook.ConstructEvent(payload, signatureHeader, os.Getenv("STRIPE_WEBHOOK_SECRET"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		log.Printf("webhook.ConstructEvent: %v", err)
		return
	}

	if event.Type == "checkout.session.completed" {
		fmt.Println("Checkout Session completed!")

		var sessionObj stripe.CheckoutSession
		if err := json.Unmarshal(event.Data.Raw, &sessionObj); err != nil {
			fmt.Fprintln(os.Stderr, "Failed to parse session object:", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		fmt.Println("Payment Intent ID:", sessionObj.PaymentIntent.ID)
		fmt.Println("Payment Status:", sessionObj.PaymentStatus)
		fmt.Println("Payment Amount:", sessionObj.AmountTotal)
		fmt.Println("Currency:", sessionObj.Currency)

		confirmationEmailData := map[string]interface{}{
			"paymentIntentID": sessionObj.PaymentIntent.ID,
			"paymentStatus":   sessionObj.PaymentStatus,
			"paymentAmount":   sessionObj.AmountTotal,
			"currency":        sessionObj.Currency,
		}

		sendConfirmationEmail(confirmationEmailData)
		updatePaymentStatus(confirmationEmailData)

		writeJSON(w, map[string]interface{}{
			"success": true,
			"message": "Payment success",
		})
	} else {
		fmt.Printf("Received event of type: %s\n", event.Type)
	}
}

func sendConfirmationEmail(sessionObject map[string]interface{}) {
	fmt.Println("Sending confirmation email...")
}

func updatePaymentStatus(sessionObject map[string]interface{}) {
	fmt.Println("Updating payment status...")
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		log.Printf("json.NewEncoder.Encode: %v", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if _, err := io.Copy(w, &buf); err != nil {
		log.Printf("io.Copy: %v", err)
		return
	}
}

func writeJSONError(w http.ResponseWriter, v interface{}, code int) {
	w.WriteHeader(code)
	writeJSON(w, v)
	return
}

func writeJSONErrorMessage(w http.ResponseWriter, message string, code int) {
	resp := &ErrorResponse{
		Error: &ErrorResponseMessage{
			Message: message,
		},
	}
	writeJSONError(w, resp, code)
}

func checkEnv() {
	price := os.Getenv("PRICE")
	fmt.Println("price: " + price)
	if price == "price_12345" || price == "" {
		log.Fatal("You must set a Price ID from your Stripe account. See the README for instructions.")
	}
}

func handleSuccessPage(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "html/success.html")
}
