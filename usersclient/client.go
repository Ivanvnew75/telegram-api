// Package usersclient — клиент микросервиса users.
//
// Зачем отдельный пакет, а не http-запросы прямо в обработчике: это
// граница между сервисами. Когда users поменяет формат ответа, править
// придётся здесь, в одном месте, а не в пяти вызовах по коду.
package usersclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Ivanvnew75/libs/common"
)

var ErrNotFound = errors.New("user not found")

type User struct {
	ID         int64  `json:"id"`
	TelegramID *int64 `json:"telegram_id,omitempty"`
	Name       string `json:"name"`
}

type Client struct {
	base string
	c    *common.Client
}

func New(baseURL string, timeout time.Duration, retries int) *Client {
	return &Client{
		base: strings.TrimRight(baseURL, "/"),
		c:    common.NewClient(timeout, retries),
	}
}

func (u *Client) GetByTelegramID(ctx context.Context, tgID int64) (User, error) {
	var out User
	err := u.c.DoJSON(ctx, http.MethodGet, fmt.Sprintf("%s/users/by-telegram/%d", u.base, tgID), nil, &out)
	if err != nil {
		return User{}, translate(err)
	}
	return out, nil
}

type createUserRequest struct {
	TelegramID int64  `json:"telegram_id"`
	Name       string `json:"name"`
}

// Register создаёт пользователя, а если он уже есть — просто возвращает его.
//
// Это делает операцию ИДЕМПОТЕНТНОЙ: повторный /start от того же человека
// не должен ни падать, ни плодить дубли. Идемпотентность здесь не роскошь:
// Telegram при сбое доставки повторяет апдейт, и обработчик обязан
// корректно пережить повтор.
func (u *Client) Register(ctx context.Context, tgID int64, name string) (User, error) {
	var out User
	err := u.c.DoJSON(ctx, http.MethodPost, u.base+"/users",
		createUserRequest{TelegramID: tgID, Name: name}, &out)
	if err == nil {
		return out, nil
	}

	var httpErr *common.HTTPError
	if errors.As(err, &httpErr) && httpErr.Code == http.StatusConflict {
		// 409 — пользователь уже зарегистрирован. Дочитываем его и выходим успешно.
		return u.GetByTelegramID(ctx, tgID)
	}
	return User{}, err
}

type answerRequest struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

func (u *Client) SaveAnswer(ctx context.Context, userID int64, question, answer string) error {
	return translate(u.c.DoJSON(ctx, http.MethodPost,
		fmt.Sprintf("%s/users/%d/answers", u.base, userID),
		answerRequest{Question: question, Answer: answer}, nil))
}

func (u *Client) List(ctx context.Context, limit, offset int) ([]User, error) {
	var out []User
	err := u.c.DoJSON(ctx, http.MethodGet,
		fmt.Sprintf("%s/users?limit=%d&offset=%d", u.base, limit, offset), nil, &out)
	return out, translate(err)
}

// Health — для readiness: сервис не готов, если не видит своей зависимости.
func (u *Client) Health(ctx context.Context) error {
	return u.c.DoJSON(ctx, http.MethodGet, u.base+"/health", nil, nil)
}

// translate превращает HTTP 404 соседа в доменную ошибку.
// Без этого вызывающий код был бы вынужден разбирать коды HTTP —
// то есть знать про транспорт.
func translate(err error) error {
	var httpErr *common.HTTPError
	if errors.As(err, &httpErr) && httpErr.Code == http.StatusNotFound {
		return ErrNotFound
	}
	return err
}
