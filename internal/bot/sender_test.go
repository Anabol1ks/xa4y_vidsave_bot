package bot

import (
	"testing"
	"time"

	"go.uber.org/zap"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type alwaysRateLimitedAPI struct{}

func (alwaysRateLimitedAPI) Send(tgbotapi.Chattable) (tgbotapi.Message, error) {
	return tgbotapi.Message{}, &tgbotapi.Error{Code: 429, Message: "Too Many Requests"}
}

func (alwaysRateLimitedAPI) Request(tgbotapi.Chattable) (*tgbotapi.APIResponse, error) {
	return nil, nil
}

func TestSendWithResponseReturnsErrorAfterRateLimitRetries(t *testing.T) {
	sender := &Sender{
		api:   alwaysRateLimitedAPI{},
		log:   zap.NewNop(),
		sleep: func(_ time.Duration) {},
	}

	msg, err := sender.SendWithResponse(tgbotapi.NewMessage(1, "test"))
	if err == nil {
		t.Fatal("SendWithResponse() error = nil after exhausting rate-limit retries")
	}
	if msg != nil {
		t.Fatal("SendWithResponse() message != nil after exhausting rate-limit retries")
	}
}
