package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Эти тесты — практическая польза от того, что адрес Telegram вынесен
// в конфигурацию (Фактор 4). Внешний API подменяется заглушкой,
// и тесты не ходят в интернет: они быстрые, детерминированные
// и не зависят от чужой доступности.
//
// Если бы URL был константой в коде, единственным способом это проверить
// был бы реальный запрос к api.telegram.org — с настоящим токеном,
// из CI, с непредсказуемым результатом.

func TestGetUpdatesParsesMessages(t *testing.T) {
	var gotPath, gotBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		buf := make([]byte, 512)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"result": []map[string]any{{
				"update_id": 42,
				"message": map[string]any{
					"message_id": 7,
					"from":       map[string]any{"id": 100500, "first_name": "Ivan"},
					"chat":       map[string]any{"id": 100500},
					"text":       "нормально",
				},
			}},
		})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token", time.Second)
	updates, err := c.GetUpdates(context.Background(), 10, 5*time.Second)
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}

	if len(updates) != 1 {
		t.Fatalf("получено %d апдейтов, ожидался 1", len(updates))
	}
	if updates[0].Message.Text != "нормально" {
		t.Errorf("text = %q", updates[0].Message.Text)
	}
	if updates[0].Message.From.ID != 100500 {
		t.Errorf("from.id = %d", updates[0].Message.From.ID)
	}

	// Токен обязан быть в пути — так устроен Bot API.
	if !strings.Contains(gotPath, "bottest-token/getUpdates") {
		t.Errorf("неожиданный путь запроса: %s", gotPath)
	}
	// offset должен уехать на сервер: без него Telegram будет отдавать
	// одни и те же сообщения бесконечно.
	if !strings.Contains(gotBody, "offset=10") {
		t.Errorf("offset не передан, тело: %s", gotBody)
	}
}

// Ошибка 409 — та самая «этого бота уже опрашивает другой процесс».
// Проверяем, что она доезжает до вызывающего кода структурированно,
// а не строкой: poller принимает по ней отдельное решение.
func TestAPIErrorIsTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"ok":          false,
			"error_code":  409,
			"description": "Conflict: terminated by other getUpdates request",
		})
	}))
	defer srv.Close()

	c := New(srv.URL, "t", time.Second)
	_, err := c.GetUpdates(context.Background(), 0, time.Second)

	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("ожидался *APIError, получено %T: %v", err, err)
	}
	if apiErr.Code != 409 {
		t.Errorf("code = %d, ожидался 409", apiErr.Code)
	}
}

func TestSendMessagePassesChatAndText(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 1024)
		n, _ := r.Body.Read(b)
		body = string(b[:n])
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{}})
	}))
	defer srv.Close()

	c := New(srv.URL, "t", time.Second)
	if err := c.SendMessage(context.Background(), 555, "привет"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if !strings.Contains(body, "chat_id=555") {
		t.Errorf("chat_id не передан: %s", body)
	}
	// Текст уходит url-encoded — проверяем, что он вообще там есть.
	if !strings.Contains(body, "text=") {
		t.Errorf("text не передан: %s", body)
	}
}
