// Package telegram — тонкий клиент Telegram Bot API.
//
// Фактор 4 (Backing services) относится и к внешним API, не только к базам.
// Telegram здесь — такой же подключаемый ресурс: его адрес приходит
// переменной TELEGRAM_API_URL. Благодаря этому в тестах и в локальной
// разработке можно подставить заглушку, не трогая код.
package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New. pollTimeout нужен, чтобы посчитать таймаут HTTP-клиента.
//
// Тонкость long polling: сервер Telegram держит соединение открытым
// до pollTimeout секунд, ожидая событие. Если таймаут HTTP-клиента
// меньше — клиент сам оборвёт запрос, и опрос будет вечно падать
// по context deadline exceeded. Поэтому запас обязателен.
func New(baseURL, token string, pollTimeout time.Duration) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: pollTimeout + 15*time.Second},
	}
}

type User struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

type Chat struct {
	ID int64 `json:"id"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
	Date      int64  `json:"date"`
}

type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
}

// GetMe — проверка токена. Используется в readiness-пробе.
func (c *Client) GetMe(ctx context.Context) (User, error) {
	var u User
	err := c.call(ctx, "getMe", nil, &u)
	return u, err
}

// GetUpdates — long polling.
//
// offset — идентификатор ПЕРВОГО непрочитанного апдейта. Передавая
// offset = последний_обработанный + 1, мы одновременно подтверждаем
// Telegram, что предыдущие обработаны, и он их удаляет. Без этого
// каждый опрос возвращал бы одни и те же сообщения снова и снова.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]Update, error) {
	params := url.Values{}
	params.Set("offset", strconv.FormatInt(offset, 10))
	params.Set("timeout", strconv.Itoa(int(timeout.Seconds())))
	// Просим только сообщения. Меньше лишнего трафика и меньше шансов
	// подавиться типом апдейта, который мы не умеем разбирать.
	params.Set("allowed_updates", `["message"]`)

	var updates []Update
	if err := c.call(ctx, "getUpdates", params, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

func (c *Client) SendMessage(ctx context.Context, chatID int64, text string) error {
	params := url.Values{}
	params.Set("chat_id", strconv.FormatInt(chatID, 10))
	params.Set("text", text)
	return c.call(ctx, "sendMessage", params, nil)
}

func (c *Client) call(ctx context.Context, method string, params url.Values, out any) error {
	// Токен уходит в ПУТИ URL — так устроен Telegram Bot API.
	// Практическое следствие: этот URL нельзя логировать целиком,
	// иначе токен окажется в логах. Поэтому в ошибках ниже фигурирует
	// только имя метода.
	endpoint := fmt.Sprintf("%s/bot%s/%s", c.baseURL, c.token, method)

	var body io.Reader
	contentType := ""
	if params != nil {
		body = strings.NewReader(params.Encode())
		contentType = "application/x-www-form-urlencoded"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return fmt.Errorf("telegram %s: build request: %w", method, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("telegram %s: read body: %w", method, err)
	}

	var r apiResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("telegram %s: bad json (http %d)", method, resp.StatusCode)
	}
	if !r.OK {
		// Возвращаем структурированную ошибку: код 409 (Conflict) при
		// getUpdates означает «этот бот уже опрашивается другим процессом»,
		// и обрабатывать её надо иначе, чем сетевой сбой.
		return &APIError{Method: method, Code: r.ErrorCode, Description: r.Description}
	}

	if out != nil {
		if err := json.Unmarshal(r.Result, out); err != nil {
			return fmt.Errorf("telegram %s: decode result: %w", method, err)
		}
	}
	return nil
}

type APIError struct {
	Method      string
	Code        int
	Description string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram %s: api error %d: %s", e.Method, e.Code, e.Description)
}
