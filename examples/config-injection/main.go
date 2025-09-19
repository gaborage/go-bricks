package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/gaborage/go-bricks/server"
)

// ExternalAPIService demonstrates the new config injection pattern.
// It shows how a service can declare its configuration requirements
// and have them automatically injected during initialization.
type ExternalAPIService struct {
	// Configuration is injected based on struct tags
	APIKey     string        `config:"custom.api.key" required:"true"`
	BaseURL    string        `config:"custom.api.base.url" default:"https://api.example.com"`
	Timeout    time.Duration `config:"custom.api.timeout" default:"30s"`
	MaxRetries int           `config:"custom.api.max.retries" default:"3"`
	EnableAuth bool          `config:"custom.api.enable.auth" default:"true"`
	RateLimit  int           `config:"custom.api.rate.limit" default:"100"`
	UserAgent  string        `config:"custom.api.user.agent" default:"GoBricks/1.0"`
}

// CallThirdPartyAPI demonstrates calling an external API with injected config
func (s *ExternalAPIService) CallThirdPartyAPI(ctx context.Context, endpoint string, data map[string]interface{}) (map[string]interface{}, error) {
	// Service can now use its configuration directly without needing it passed as parameters
	fmt.Printf("Making API call to %s%s\n", s.BaseURL, endpoint)
	fmt.Printf("Using API Key: %s (first 10 chars)\n", s.APIKey[:minInt(10, len(s.APIKey))])
	fmt.Printf("Timeout: %v, Max Retries: %d, Rate Limit: %d\n", s.Timeout, s.MaxRetries, s.RateLimit)
	if len(data) > 0 {
		fmt.Printf("Payload: %v\n", data)
	}

	// Simulate API call while respecting context cancellation
	select {
	case <-time.After(100 * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	return map[string]interface{}{
		"status":  "success",
		"message": "API call completed",
		"config_used": map[string]interface{}{
			"base_url":    s.BaseURL,
			"timeout":     s.Timeout.String(),
			"max_retries": s.MaxRetries,
			"enable_auth": s.EnableAuth,
			"rate_limit":  s.RateLimit,
			"user_agent":  s.UserAgent,
		},
	}, nil
}

// PaymentServiceConfig shows a focused config struct for a specific service domain
type PaymentServiceConfig struct {
	ProviderURL    string        `config:"custom.payment.provider.url" required:"true"`
	APIKey         string        `config:"custom.payment.api.key" required:"true"`
	WebhookSecret  string        `config:"custom.payment.webhook.secret" required:"true"`
	ConnectTimeout time.Duration `config:"custom.payment.connect.timeout" default:"10s"`
	RequestTimeout time.Duration `config:"custom.payment.request.timeout" default:"30s"`
	MaxRetries     int           `config:"custom.payment.max.retries" default:"3"`
	EnableSandbox  bool          `config:"custom.payment.enable.sandbox" default:"false"`
	MinAmount      int64         `config:"custom.payment.min.amount" default:"100"`     // in cents
	MaxAmount      int64         `config:"custom.payment.max.amount" default:"1000000"` // in cents
}

// PaymentService demonstrates service-specific config injection
type PaymentService struct {
	config PaymentServiceConfig
}

// NewPaymentService creates a payment service with injected configuration
func NewPaymentService(cfg *PaymentServiceConfig) *PaymentService {
	return &PaymentService{config: *cfg}
}

// ProcessPayment demonstrates using the injected configuration
func (p *PaymentService) ProcessPayment(ctx context.Context, amount int64, currency string) error {
	if amount < p.config.MinAmount || amount > p.config.MaxAmount {
		return fmt.Errorf("amount %d is outside allowed range [%d, %d]", amount, p.config.MinAmount, p.config.MaxAmount)
	}

	fmt.Printf("Processing payment: %d %s\n", amount, currency)
	fmt.Printf("Provider: %s (sandbox: %v)\n", p.config.ProviderURL, p.config.EnableSandbox)
	fmt.Printf("Using API Key: %s... (masked)\n", p.config.APIKey[:minInt(8, len(p.config.APIKey))])
	fmt.Printf("Timeouts - Connect: %v, Request: %v\n", p.config.ConnectTimeout, p.config.RequestTimeout)

	// Simulate payment processing while respecting context cancellation
	select {
	case <-time.After(200 * time.Millisecond):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ConfigInjectionModule demonstrates the config injection pattern in a module
type ConfigInjectionModule struct {
	deps           *app.ModuleDeps
	apiService     *ExternalAPIService
	paymentService *PaymentService
}

// NewConfigInjectionModule creates a new module instance
func NewConfigInjectionModule() *ConfigInjectionModule {
	return &ConfigInjectionModule{}
}

// Name returns the module name
func (m *ConfigInjectionModule) Name() string {
	return "config-injection"
}

// Init demonstrates the new pattern where services get their config injected during initialization
func (m *ConfigInjectionModule) Init(deps *app.ModuleDeps) error {
	m.deps = deps

	// Initialize API service with config injection
	apiService := &ExternalAPIService{}
	if err := deps.Config.InjectInto(apiService); err != nil {
		return fmt.Errorf("failed to inject config into API service: %w", err)
	}
	m.apiService = apiService

	// Initialize payment service with focused config struct
	var paymentConfig PaymentServiceConfig
	if err := deps.Config.InjectInto(&paymentConfig); err != nil {
		return fmt.Errorf("failed to inject config into payment config: %w", err)
	}
	m.paymentService = NewPaymentService(&paymentConfig)

	m.deps.Logger.Info().
		Str("api_base_url", apiService.BaseURL).
		Dur("api_timeout", apiService.Timeout).
		Int("api_rate_limit", apiService.RateLimit).
		Str("payment_provider", paymentConfig.ProviderURL).
		Interface("payment_sandbox", paymentConfig.EnableSandbox).
		Msg("Config injection module initialized with injected configurations")

	return nil
}

// Request/Response types
type APICallRequest struct {
	Endpoint string                 `json:"endpoint" validate:"required"`
	Data     map[string]interface{} `json:"data"`
}

type APICallResponse struct {
	Result map[string]interface{} `json:"result"`
}

type PaymentRequest struct {
	Amount   int64  `json:"amount" validate:"min=1"`
	Currency string `json:"currency" validate:"required,len=3"`
}

type PaymentResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// RegisterRoutes registers module routes
func (m *ConfigInjectionModule) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	server.POST(hr, r, "/api-call", m.handleAPICall)
	server.POST(hr, r, "/payment", m.handlePayment)
	server.GET(hr, r, "/config", m.handleGetConfig)
}

// RegisterMessaging registers messaging handlers (none for this example)
func (m *ConfigInjectionModule) RegisterMessaging(_ *messaging.Registry) {
	// No messaging for this example
}

// Shutdown cleans up module resources
func (m *ConfigInjectionModule) Shutdown() error {
	return nil
}

// Handler methods that can now call services without passing config

func (m *ConfigInjectionModule) handleAPICall(req APICallRequest, ctx server.HandlerContext) (APICallResponse, server.IAPIError) {
	// Notice: No need to pass config to the service - it was injected during initialization
	result, err := m.apiService.CallThirdPartyAPI(ctx.Echo.Request().Context(), req.Endpoint, req.Data)
	if err != nil {
		return APICallResponse{}, server.NewInternalServerError("Failed to call external API")
	}

	return APICallResponse{Result: result}, nil
}

func (m *ConfigInjectionModule) handlePayment(req PaymentRequest, ctx server.HandlerContext) (PaymentResponse, server.IAPIError) {
	// Clean service call without config parameter passing
	err := m.paymentService.ProcessPayment(ctx.Echo.Request().Context(), req.Amount, req.Currency)
	if err != nil {
		return PaymentResponse{}, server.NewBadRequestError("Payment processing failed")
	}

	return PaymentResponse{
		Status:  "success",
		Message: "Payment processed successfully",
	}, nil
}

func (m *ConfigInjectionModule) handleGetConfig(_ struct{}, _ server.HandlerContext) (map[string]interface{}, server.IAPIError) {
	return map[string]interface{}{
		"api_config": map[string]interface{}{
			"base_url":    m.apiService.BaseURL,
			"timeout":     m.apiService.Timeout.String(),
			"max_retries": m.apiService.MaxRetries,
			"enable_auth": m.apiService.EnableAuth,
			"rate_limit":  m.apiService.RateLimit,
			"user_agent":  m.apiService.UserAgent,
		},
		"payment_config": map[string]interface{}{
			"provider_url":    m.paymentService.config.ProviderURL,
			"connect_timeout": m.paymentService.config.ConnectTimeout.String(),
			"request_timeout": m.paymentService.config.RequestTimeout.String(),
			"max_retries":     m.paymentService.config.MaxRetries,
			"enable_sandbox":  m.paymentService.config.EnableSandbox,
			"min_amount":      m.paymentService.config.MinAmount,
			"max_amount":      m.paymentService.config.MaxAmount,
		},
	}, nil
}

func main() {
	// Set up environment variables for demonstration
	envVars := map[string]string{
		"CUSTOM_API_KEY":                 "sk_live_abc123xyz789",
		"CUSTOM_API_BASE_URL":            "https://api.production.com",
		"CUSTOM_API_TIMEOUT":             "45s",
		"CUSTOM_API_MAX_RETRIES":         "5",
		"CUSTOM_API_ENABLE_AUTH":         "true",
		"CUSTOM_API_RATE_LIMIT":          "200",
		"CUSTOM_API_USER_AGENT":          "MyApp/2.0",
		"CUSTOM_PAYMENT_PROVIDER_URL":    "https://payment.stripe.com",
		"CUSTOM_PAYMENT_API_KEY":         "pk_live_payment_key_123",
		"CUSTOM_PAYMENT_WEBHOOK_SECRET":  "whsec_webhook_secret_456",
		"CUSTOM_PAYMENT_CONNECT_TIMEOUT": "5s",
		"CUSTOM_PAYMENT_REQUEST_TIMEOUT": "60s",
		"CUSTOM_PAYMENT_MAX_RETRIES":     "2",
		"CUSTOM_PAYMENT_ENABLE_SANDBOX":  "false",
		"CUSTOM_PAYMENT_MIN_AMOUNT":      "50",
		"CUSTOM_PAYMENT_MAX_AMOUNT":      "5000000",
	}

	// Set environment variables for this example
	for key, value := range envVars {
		if err := os.Setenv(key, value); err != nil {
			log.Fatalf("Failed to set environment variable %s: %v", key, err)
		}
	}

	// Clean up environment variables when done
	defer func() {
		for key := range envVars {
			os.Unsetenv(key)
		}
	}()

	// Create and run the application
	application, err := app.New()
	if err != nil {
		log.Printf("Failed to create app: %v", err)
		return
	}

	// Register the module that demonstrates config injection
	if err := application.RegisterModule(NewConfigInjectionModule()); err != nil {
		log.Printf("Failed to register module: %v", err)
		return
	}

	fmt.Println("=== Config Injection Example ===")
	fmt.Println("Server starting on :8080")
	fmt.Println()
	fmt.Println("Try these endpoints:")
	fmt.Println("  POST /api-call    - Call external API service")
	fmt.Println("  POST /payment     - Process payment")
	fmt.Println("  GET  /config      - View injected configuration")
	fmt.Println()
	fmt.Println("Example requests:")
	fmt.Println(`  curl -X POST http://localhost:8080/api-call -H "Content-Type: application/json" -d '{"endpoint": "/users", "data": {"limit": 10}}'`)
	fmt.Println(`  curl -X POST http://localhost:8080/payment -H "Content-Type: application/json" -d '{"amount": 1999, "currency": "USD"}'`)
	fmt.Println(`  curl http://localhost:8080/config`)

	if err := application.Run(); err != nil && err != http.ErrServerClosed {
		log.Printf("Application failed: %v", err)
	}
}

// Helper functions
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
